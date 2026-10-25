package browser

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha1" //nolint:gosec // Chrome's KDF; not our choice
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"golang.org/x/crypto/pbkdf2"
	//nolint:revive // registers the "sqlite" driver
	_ "modernc.org/sqlite"
)

const (
	keychainService = "Chrome Safe Storage"
	keychainAccount = "Chrome"
	pbkdf2Salt      = "saltysalt"
	pbkdf2Rounds    = 1003
	keyLen          = 16
	valuePrefix     = "v10"
)

type Profile struct {
	Name        string // e.g. "Default", "Profile 1"
	CookiesPath string
	Cookies     int   // Kroger cookies found
	LastUpdate  int64 // newest Kroger cookie, in Chrome's epoch
}

var ErrUnsupported = errors.New("reading browser cookies is only implemented for Chrome on macOS")

var ErrNoProfile = errors.New("no Chrome profile has any kroger.com cookies; sign in to kroger.com in Chrome first")

func ChromeJar(profileName string) (jar string, chosen Profile, err error) {
	if runtime.GOOS != "darwin" {
		return "", Profile{}, ErrUnsupported
	}
	profiles, err := ChromeProfiles()
	if err != nil {
		return "", Profile{}, err
	}
	if len(profiles) == 0 {
		return "", Profile{}, ErrNoProfile
	}
	chosen = profiles[0]
	if profileName != "" {
		found := false
		for _, p := range profiles {
			if p.Name == profileName {
				chosen, found = p, true
				break
			}
		}
		if !found {
			return "", Profile{}, fmt.Errorf("no Kroger cookies in Chrome profile %q; profiles with cookies: %s",
				profileName, profileNames(profiles))
		}
	}

	key, err := chromeKey()
	if err != nil {
		return "", chosen, err
	}
	pairs, err := readCookies(chosen.CookiesPath, key)
	if err != nil {
		return "", chosen, err
	}
	if len(pairs) == 0 {
		return "", chosen, ErrNoProfile
	}
	return strings.Join(pairs, "; "), chosen, nil
}

func ChromeProfiles() ([]Profile, error) {
	if runtime.GOOS != "darwin" {
		return nil, ErrUnsupported
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	root := filepath.Join(home, "Library", "Application Support", "Google", "Chrome")
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no Chrome installation found at %s", root)
		}
		return nil, err
	}

	var out []Profile
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if e.Name() != "Default" && !strings.HasPrefix(e.Name(), "Profile ") {
			continue
		}
		path := filepath.Join(root, e.Name(), "Cookies")
		n, last, err := countKrogerCookies(path)
		if err != nil || n == 0 {
			continue
		}
		out = append(out, Profile{Name: e.Name(), CookiesPath: path, Cookies: n, LastUpdate: last})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastUpdate > out[j].LastUpdate })
	return out, nil
}

func profileNames(ps []Profile) string {
	names := make([]string, 0, len(ps))
	for _, p := range ps {
		names = append(names, p.Name)
	}
	return strings.Join(names, ", ")
}

// Chrome encrypts cookie values with AES-128-CBC under a PBKDF2 key from this keychain item.
func chromeKey() ([]byte, error) {
	cmd := exec.Command("security", "find-generic-password",
		"-w", "-s", keychainService, "-a", keychainAccount)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("reading the Chrome key from your keychain failed. "+
			"If macOS asked for permission, click Allow and try again: %w", err)
	}
	secret := strings.TrimRight(string(out), "\n")
	if secret == "" {
		return nil, errors.New("the Chrome Safe Storage keychain item is empty")
	}
	return pbkdf2.Key([]byte(secret), []byte(pbkdf2Salt), pbkdf2Rounds, keyLen, sha1.New), nil
}

func countKrogerCookies(path string) (int, int64, error) {
	db, cleanup, err := openSnapshot(path)
	if err != nil {
		return 0, 0, err
	}
	defer cleanup()
	var n int
	var last sql.NullInt64
	err = db.QueryRow(`SELECT COUNT(*), MAX(last_update_utc) FROM cookies
	                   WHERE host_key LIKE '%kroger.com'`).Scan(&n, &last)
	if err != nil {
		return 0, 0, err
	}
	return n, last.Int64, nil
}

func readCookies(path string, key []byte) ([]string, error) {
	db, cleanup, err := openSnapshot(path)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	// login.kroger.com cookies are left out: they aren't sent to www.kroger.com and have caused 403s.
	rows, err := db.Query(`SELECT host_key, name, encrypted_value FROM cookies
	                       WHERE host_key IN ('.kroger.com', 'www.kroger.com')
	                         AND path = '/'
	                       ORDER BY last_update_utc ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	order := []string{}
	values := map[string]string{}
	for rows.Next() {
		var host, name string
		var blob []byte
		if err := rows.Scan(&host, &name, &blob); err != nil {
			return nil, err
		}
		plain, err := decrypt(blob, key, host)
		if err != nil {
			continue
		}
		if _, seen := values[name]; !seen {
			order = append(order, name)
		}
		values[name] = plain
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	pairs := make([]string, 0, len(order))
	for _, name := range order {
		pairs = append(pairs, name+"="+values[name])
	}
	return pairs, nil
}

func decrypt(blob, key []byte, host string) (string, error) {
	if len(blob) <= len(valuePrefix) || string(blob[:len(valuePrefix)]) != valuePrefix {
		return "", fmt.Errorf("not a %s value", valuePrefix)
	}
	body := blob[len(valuePrefix):]
	if len(body) == 0 || len(body)%aes.BlockSize != 0 {
		return "", errors.New("ciphertext is not a whole number of blocks")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	out := make([]byte, len(body))
	cipher.NewCBCDecrypter(block, []byte(strings.Repeat(" ", aes.BlockSize))).CryptBlocks(out, body)

	pad := int(out[len(out)-1])
	if pad == 0 || pad > aes.BlockSize || pad > len(out) {
		return "", errors.New("bad padding; the keychain key is probably wrong")
	}
	out = out[:len(out)-pad]

	if sum := sha256.Sum256([]byte(host)); len(out) >= len(sum) && string(out[:len(sum)]) == string(sum[:]) {
		out = out[len(sum):]
	}
	return string(out), nil
}

// Chrome holds the live database open, so it is copied first; the copy holds live cookies and is deleted.
func openSnapshot(path string) (*sql.DB, func(), error) {
	src, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer src.Close()

	tmp, err := os.CreateTemp("", "kroger-cookies-*.db")
	if err != nil {
		return nil, nil, err
	}
	if _, err := io.Copy(tmp, src); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return nil, nil, err
	}
	tmp.Close()

	cleanup := func() { os.Remove(tmp.Name()) }
	db, err := sql.Open("sqlite", "file:"+tmp.Name()+"?mode=ro")
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	return db, func() { db.Close(); cleanup() }, nil
}
