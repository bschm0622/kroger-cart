// Copyright 2026 bschm0622 and contributors. Licensed under Apache-2.0. See LICENSE.

package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bschm0622/kroger-cart/internal/config"
)

// Kroger is a cookie-auth API. As generated, New() passed a nil jar to
// newHTTPClient and do()'s `if authHeader != ""` branch was an empty comment
// block deferring to a LoadCookieJar that was never emitted, so every request
// went out with no credentials at all and the only symptom was a 401 that
// looked like an expired session. These tests pin the wire behavior: the
// failure mode is silent, so nothing else would catch a regression.

// TestCookieAuth_JarReachesTheWire asserts a stored session actually arrives
// as a Cookie header on the request.
func TestCookieAuth_JarReachesTheWire(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Cookie")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	cfg := &config.Config{BaseURL: srv.URL, AccessToken: "sid=abc123; _kr=zzz"}
	c := New(cfg, 5*time.Second, 0)
	c.NoCache = true

	if _, err := c.Get(context.Background(), "/probe", nil); err != nil {
		t.Fatalf("Get: %v", err)
	}

	for _, want := range []string{"sid=abc123", "_kr=zzz"} {
		if !strings.Contains(got, want) {
			t.Errorf("Cookie header %q missing %q", got, want)
		}
	}
	// Each cookie exactly once: the jar is the sole outbound source, and a
	// manual Cookie header alongside it would duplicate every value and trip
	// upstream WAFs.
	if n := strings.Count(got, "sid="); n != 1 {
		t.Errorf("cookie sent %d times, want 1: %q", n, got)
	}
	if strings.Contains(got, "Bearer") {
		t.Errorf("cookie jar leaked a bearer scheme: %q", got)
	}
}

// TestCookieAuth_ByteExact is the Akamai guard. net/http's cookie sanitizer
// silently drops bytes it dislikes — '"' among them, which Kroger's RT cookie
// carries — logging only "invalid byte '\"' in Cookie.Value; dropping invalid
// bytes". Akamai validates _abck / bm_sv byte-for-byte, so a mangled value
// reads as a forged session and comes back 403. Any future change that routes
// cookies back through a real http.CookieJar or req.AddCookie fails here.
func TestCookieAuth_ByteExact(t *testing.T) {
	// Shapes lifted from a real Kroger jar: quoted value with & and =,
	// URL-encoded '@' in the name, and base64 padding in the value.
	jar := `RT="z=1&dm=kroger.com&si=abc&ss=123"; ` +
		`AMCV_371C27E253DB0F910A490D4E%40AdobeOrg=-330|MCMID|8842; ` +
		`_abck=A1B2~-1~YAAQxyz==~-1~-1`

	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Cookie")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	cfg := &config.Config{BaseURL: srv.URL, AccessToken: jar}
	c := New(cfg, 5*time.Second, 0)
	c.NoCache = true

	if _, err := c.Get(context.Background(), "/probe", nil); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != jar {
		t.Errorf("Cookie header was altered in transit.\n got: %q\nwant: %q", got, jar)
	}
}

// TestCookieAuth_NoDuplicateOnSetCookie pins the reason the jar is inert
// rather than absent: with surf's own session jar active, Set-Cookie values
// from a response would be replayed alongside the manual header, sending some
// cookies twice on the next request.
func TestCookieAuth_NoDuplicateOnSetCookie(t *testing.T) {
	var last string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		last = r.Header.Get("Cookie")
		http.SetCookie(w, &http.Cookie{Name: "sid", Value: "server-rotated", Path: "/"})
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	cfg := &config.Config{BaseURL: srv.URL, AccessToken: "sid=abc123"}
	c := New(cfg, 5*time.Second, 0)
	c.NoCache = true

	for i := 0; i < 2; i++ {
		if _, err := c.Get(context.Background(), "/probe", nil); err != nil {
			t.Fatalf("Get %d: %v", i, err)
		}
	}
	if n := strings.Count(last, "sid="); n != 1 {
		t.Errorf("sid sent %d times after a Set-Cookie response, want 1: %q", n, last)
	}
	if last != "sid=abc123" {
		t.Errorf("Cookie header = %q, want the configured session verbatim", last)
	}
}

// TestCookieAuth_NoAuthorizationHeaderForJar guards the inverse: a cookie jar
// must not also be shipped as an Authorization bearer, which is what
// AuthHeader()'s unconditional ensureAuthScheme("Bearer", ...) used to build.
func TestCookieAuth_NoAuthorizationHeaderForJar(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	cfg := &config.Config{BaseURL: srv.URL, AccessToken: "sid=abc123; _kr=zzz"}
	c := New(cfg, 5*time.Second, 0)
	c.NoCache = true

	if _, err := c.Get(context.Background(), "/probe", nil); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "" {
		t.Errorf("Authorization header = %q, want empty for cookie auth", got)
	}
}

// TestBearerAuth_StillReachesTheWire covers the auth set-token path through
// the same branch, which was equally unwired.
func TestBearerAuth_StillReachesTheWire(t *testing.T) {
	jwt := "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9." +
		strings.Repeat("a", 80) + "." + strings.Repeat("b", 43)

	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	cfg := &config.Config{BaseURL: srv.URL, AccessToken: jwt}
	c := New(cfg, 5*time.Second, 0)
	c.NoCache = true

	if _, err := c.Get(context.Background(), "/probe", nil); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "Bearer "+jwt {
		t.Errorf("Authorization = %q, want bearer JWT", got)
	}
}

func TestLooksLikeCookieJar(t *testing.T) {
	jwt := "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9." +
		strings.Repeat("a", 80) + "." + strings.Repeat("b", 43)

	cases := []struct {
		in   string
		want bool
	}{
		{"sid=abc; other=def", true},
		{"sid=abc", true},
		{jwt, false},
		{"Bearer " + jwt, false},
		{"", false},
		{"notacookie", false},
		// A JWT's base64url padding is the one place "=" shows up in a
		// bearer token, so a bare Cut on "=" can't separate the shapes.
		{"eyJhbGciOi.abc.def==", false},
		// Regression: dots and underscores are valid cookie-name characters
		// and lead real jars constantly. Rejecting them rejected Kroger's
		// actual session, which opens with a dotted analytics cookie.
		{"_ga.1=x; sid=abc", true},
		{"AMCV_1234%40AdobeOrg=abc; sid=def", true},
		// Empty leading value must not disqualify the rest of the jar.
		{"empty=; sid=abc", true},
	}
	for _, tc := range cases {
		if got := config.LooksLikeCookieJar(tc.in); got != tc.want {
			t.Errorf("LooksLikeCookieJar(%.30q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
