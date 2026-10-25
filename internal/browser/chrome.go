// Package browser reads the Kroger session out of a local browser profile.
//
// Kroger's session is a cookie jar, and the awkward part of using this tool has
// always been getting that jar out of the browser: open DevTools, find a
// request that returned 200, right-click the Cookie header, paste. Chrome
// already stores the whole jar on disk, including the httpOnly Akamai cookies
// that a page script cannot see, so it can be read directly instead.
//
// This only ever reads. It touches the caller's own cookie database with their
// own keychain key, which is the same access the caller already has by hand.
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

// Chrome's cookie encryption on macOS. These constants are Chrome's, not ours:
// the key is PBKDF2 over a keychain secret with a fixed salt and iteration
// count, and the IV is sixteen spaces.
const (
	keychainService = "Chrome Safe Storage"
	keychainAccount = "Chrome"
	pbkdf2Salt      = "saltysalt"
	pbkdf2Rounds    = 1003
	keyLen          = 16
	valuePrefix     = "v10"
)

// Profile is one Chrome profile that holds Kroger cookies.
type Profile struct {
	Name        string // e.g. "Default", "Profile 1"
	CookiesPath string
	Cookies     int   // Kroger cookies found
	LastUpdate  int64 // newest Kroger cookie, in Chrome's epoch
}

// ErrUnsupported is returned on platforms this has not been written for.
var ErrUnsupported = errors.New("reading browser cookies is only implemented for Chrome on macOS")

// ErrNoProfile is returned when no Chrome profile holds Kroger cookies —
// usually because the user is signed in on a different browser.
var ErrNoProfile = errors.New("no Chrome profile has any kroger.com cookies; sign in to kroger.com in Chrome first")

// ChromeJar returns a Cookie header value for www.kroger.com built from a local
// Chrome profile. An empty profile name picks the profile with the most
// recently updated Kroger cookies, which is the one you were just browsing in.
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

// ChromeProfiles lists the Chrome profiles that hold Kroger cookies, most
// recently updated first.
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
		// Chrome names profiles "Default" and "Profile N"; every other
		// directory here is Chrome's own state, not a profile.
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

// chromeKey derives the AES key from the "Chrome Safe Storage" keychain item.
//
// The secret is read via the security(1) binary rather than a Keychain API
// binding, because that is what makes macOS show its own "allow access?"
// dialog naming a tool the user recognises.
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
	// SHA-1 is Chrome's choice here, not ours; it is the only thing that
	// derives the key Chrome actually encrypted with.
	return pbkdf2.Key([]byte(secret), []byte(pbkdf2Salt), pbkdf2Rounds, keyLen, sha1.New), nil
}

// countKrogerCookies reports how many Kroger cookies a profile holds without
// decrypting anything, so profile selection needs no keychain access.
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

// readCookies decrypts the cookies that a request to www.kroger.com would send.
func readCookies(path string, key []byte) ([]string, error) {
	db, cleanup, err := openSnapshot(path)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	// Host-only and domain cookies for www.kroger.com, at the root path.
	// Subdomain jars (login.kroger.com) are deliberately excluded: they are
	// not sent to www.kroger.com, and including them has produced 403s.
	//
	// Ordered oldest-first so that if a name somehow appears twice, the
	// fresher value is the one that survives into the map below.
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
			// One unreadable cookie should not sink the jar; a cookie
			// written by a different Chrome build can be skipped.
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

// decrypt reverses Chrome's v10 cookie encryption.
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

	// Chrome binds a cookie to its domain by prefixing the plaintext with
	// sha256(host_key). Older values have no prefix, so this is conditional
	// rather than assumed.
	if sum := sha256.Sum256([]byte(host)); len(out) >= len(sum) && string(out[:len(sum)]) == string(sum[:]) {
		out = out[len(sum):]
	}
	return string(out), nil
}

// openSnapshot copies the cookie database before opening it.
//
// Chrome holds the live file open, and reading it in place risks both a lock
// error and a torn read. The copy is deleted by the returned cleanup — it holds
// decryptable session cookies, so it does not outlive the call.
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
