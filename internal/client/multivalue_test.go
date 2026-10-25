package client

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/bschm0622/kroger-cart/internal/config"
)

// A MultiValue param must reach the wire as a repeated key. Comma-joining
// filter.gtin13s draws an Akamai "Access Denied" interstitial, so the encoding
// is load-bearing rather than cosmetic.
func TestMultiValueEmitsRepeatedQueryKey(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query()["filter.gtin13s"]
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c := New(&config.Config{BaseURL: srv.URL}, 5*time.Second, 0)
	if _, err := c.Get(t.Context(), "/p", map[string]string{
		"filter.gtin13s": MultiValue([]string{"111", "222", "333"}),
		"filter.plain":   "one",
	}); err != nil {
		t.Fatal(err)
	}

	want := []string{"111", "222", "333"}
	if len(got) != len(want) {
		t.Fatalf("got %d values %v, want %v", len(got), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

// A single value must not gain a repeated-key encoding, and must not carry the
// separator through to the wire.
func TestMultiValueSingleValueIsPlain(t *testing.T) {
	if v := MultiValue([]string{"111"}); v != "111" {
		t.Fatalf("got %q, want %q", v, "111")
	}
}
