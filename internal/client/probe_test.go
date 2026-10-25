package client

import (
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/bschm0622/kroger-cart/internal/config"
)

// Temporary diagnostic: PROBE=1 go test ./internal/client -run Probe -v
func TestProbe(t *testing.T) {
	if os.Getenv("PROBE") == "" {
		t.Skip("diagnostic only")
	}
	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	hc := newHTTPClient(30*time.Second, cookieJarFor(cfg), cfg.Headers)

	url := cfg.BaseURL + "/atlas/v1/customer-profile/v1/profile?projections=customerProfile.full"
	if v := os.Getenv("PROBE_URL"); v != "" {
		url = cfg.BaseURL + v
	}
	req, _ := http.NewRequest("GET", url, nil)
	for k, v := range cfg.Headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("Cookie", cfg.CookieHeader())
	req.Header.Set("Referer", "https://www.kroger.com/cart")

	resp, err := hc.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()

	t.Logf("STATUS %s  proto=%s", resp.Status, resp.Proto)
	if b, _ := io.ReadAll(io.LimitReader(resp.Body, 600)); len(b) > 0 {
		t.Logf("BODY %s", b)
	}
	for k, v := range resp.Header {
		// Set-Cookie carries live session material; log only its shape.
		if http.CanonicalHeaderKey(k) == "Set-Cookie" {
			t.Logf("  resp Set-Cookie: %d cookie(s), values elided", len(v))
			continue
		}
		t.Logf("  resp %s: %v", k, v)
	}
}
