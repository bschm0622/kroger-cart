package browser

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"strings"
	"testing"
)

func seal(t *testing.T, key []byte, plain string, bindTo string) []byte {
	t.Helper()
	body := []byte(plain)
	if bindTo != "" {
		sum := sha256.Sum256([]byte(bindTo))
		body = append(sum[:], body...)
	}
	pad := aes.BlockSize - len(body)%aes.BlockSize
	for i := 0; i < pad; i++ {
		body = append(body, byte(pad))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]byte, len(body))
	cipher.NewCBCEncrypter(block, []byte(strings.Repeat(" ", aes.BlockSize))).CryptBlocks(out, body)
	return append([]byte(valuePrefix), out...)
}

func testKey() []byte { return []byte("0123456789abcdef") }

func TestDecryptStripsDomainPrefix(t *testing.T) {
	key := testKey()
	const host = ".kroger.com"
	got, err := decrypt(seal(t, key, "abc123", host), key, host)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if got != "abc123" {
		t.Errorf("got %q, want %q", got, "abc123")
	}
}

func TestDecryptWithoutDomainPrefix(t *testing.T) {
	key := testKey()
	got, err := decrypt(seal(t, key, "abc123", ""), key, ".kroger.com")
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if got != "abc123" {
		t.Errorf("got %q, want %q", got, "abc123")
	}
}

func TestDecryptShortValue(t *testing.T) {
	key := testKey()
	got, err := decrypt(seal(t, key, "1", ""), key, ".kroger.com")
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if got != "1" {
		t.Errorf("got %q, want %q", got, "1")
	}
}

func TestDecryptWrongKeyIsAnError(t *testing.T) {
	blob := seal(t, testKey(), "abc123", ".kroger.com")
	if _, err := decrypt(blob, []byte("fedcba9876543210"), ".kroger.com"); err == nil {
		t.Fatal("want an error for the wrong key, got nil")
	}
}

func TestDecryptRejectsMalformedInput(t *testing.T) {
	key := testKey()
	cases := map[string][]byte{
		"empty":         {},
		"prefix only":   []byte(valuePrefix),
		"no v10 prefix": append([]byte("v20"), make([]byte, aes.BlockSize)...),
		"partial block": append([]byte(valuePrefix), make([]byte, aes.BlockSize-1)...),
	}
	for name, blob := range cases {
		if _, err := decrypt(blob, key, ".kroger.com"); err == nil {
			t.Errorf("%s: want an error, got nil", name)
		}
	}
}
