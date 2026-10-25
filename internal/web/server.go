package web

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/bschm0622/kroger-cart/internal/client"
	"github.com/bschm0622/kroger-cart/internal/config"
)

//go:embed assets
var assets embed.FS

// Server serves the local UI.
//
// NewClient is a factory rather than a client because the session cookie can be
// replaced from the UI while the server is running; a cached client would keep
// sending the expired jar until restart.
type Server struct {
	Addr       string
	ConfigPath string
	NewClient  func() (*client.Client, error)

	paths paths
}

// NewServer wires a server against the config file the CLI would use.
func NewServer(addr, configPath string, newClient func() (*client.Client, error)) (*Server, error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, err
	}
	return &Server{
		Addr:       addr,
		ConfigPath: configPath,
		NewClient:  newClient,
		paths:      newPaths(filepath.Dir(cfg.Path)),
	}, nil
}

// Handler builds the route table.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	ui, err := fs.Sub(assets, "assets")
	if err != nil {
		panic(err) // embedded at build time; a failure here is a build bug
	}
	// Embedded files have no modtime, so the browser gets no validator and
	// guesses — and keeps running the old app.js after a rebuild. no-cache makes
	// it ask every time; on localhost that costs nothing.
	files := http.FileServerFS(ui)
	mux.Handle("GET /", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		files.ServeHTTP(w, r)
	}))

	mux.HandleFunc("GET /api/status", s.handleStatus)
	mux.HandleFunc("POST /api/cookie", s.handleCookie)
	mux.HandleFunc("POST /api/cookie/chrome", s.handleCookieFromChrome)

	mux.HandleFunc("GET /api/cart", s.handleCartGet)
	mux.HandleFunc("POST /api/cart/add", s.handleCartAdd)
	mux.HandleFunc("POST /api/cart/clear", s.handleCartClear)
	mux.HandleFunc("POST /api/cart/update", s.handleCartUpdate)
	mux.HandleFunc("POST /api/cart/quantity", s.handleCartQuantity)

	mux.HandleFunc("GET /api/staples", s.handleStaplesGet)
	mux.HandleFunc("PUT /api/staples", s.handleStaplesPut)
	mux.HandleFunc("POST /api/staples/seed", s.handleStaplesSeed)

	mux.HandleFunc("GET /api/recipes", s.handleRecipesGet)
	mux.HandleFunc("PUT /api/recipes", s.handleRecipePut)
	mux.HandleFunc("POST /api/recipes/delete", s.handleRecipeDelete)

	mux.HandleFunc("GET /api/search", s.handleSearch)
	mux.HandleFunc("GET /api/history", s.handleHistory)

	return localOnly(mux)
}

// ListenAndServe binds and serves until ctx is cancelled.
func (s *Server) ListenAndServe(ctx context.Context) error {
	srv := &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	ln, err := net.Listen("tcp", s.Addr)
	if err != nil {
		return err
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// localOnly rejects any request that did not come from this machine, and any
// cross-origin request.
//
// The saved Kroger session is bearer-equivalent — it can read order history and
// place orders — and this server has no auth of its own. Binding to localhost
// is the primary defence; this is the check that still holds if someone passes
// --addr 0.0.0.0. The Origin check blocks a page on another site from driving
// the API through the browser, which localhost binding alone does not prevent.
func localOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}
		if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
			http.Error(w, "this server only serves localhost", http.StatusForbidden)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && !isLocalOrigin(origin) {
			http.Error(w, "cross-origin requests are not allowed", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func isLocalOrigin(origin string) bool {
	rest := origin
	for _, scheme := range []string{"http://", "https://"} {
		rest = strings.TrimPrefix(rest, scheme)
	}
	host, _, err := net.SplitHostPort(rest)
	if err != nil {
		host = rest
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// --- helpers ---

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// The response is already partly written, so there is nowhere useful
		// to report this; the client sees a truncated body.
		return
	}
}

// fail reports an error to the UI.
//
// Auth failures are flagged separately so the page can surface the paste-a-new-
// cookie box instead of a generic red banner: an expired session is the single
// most common failure here and the one with a specific fix.
func fail(w http.ResponseWriter, err error) {
	msg := err.Error()
	status := http.StatusBadGateway
	authExpired := strings.Contains(msg, "401") || strings.Contains(msg, "AUTH_REQUIRED")
	unauthorized := strings.Contains(msg, "403") || strings.Contains(msg, "FORBIDDEN_ACTION")
	// A reset HTTP/2 stream is what a stale session looks like from the outside:
	// Akamai's sensor cookies (_abck, bm_sz) go stale with the rest of the jar
	// and it kills the stream before Kroger's origin sees the request, so there
	// is no status code to read. Every endpoint this UI calls behaves this way
	// once the jar ages out, so it gets the same "paste a fresh cookie" path.
	stale := strings.Contains(msg, "INTERNAL_ERROR") || strings.Contains(msg, "stream error")
	switch {
	case authExpired:
		msg = "Your Kroger session has expired. Paste a fresh Cookie header to continue."
	case stale:
		msg = "Kroger's bot protection rejected the request before it reached the API, " +
			"which is what a stale session looks like. Paste a fresh Cookie header."
	case unauthorized:
		msg = "Kroger refused the request (403). This usually means the cookie was " +
			"copied from a request that wasn't itself a 200 — recopy from a profile " +
			"request you watched succeed."
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error":       msg,
		"needsCookie": authExpired || unauthorized || stale,
	})
}

func badRequest(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": err.Error()})
}

func (s *Server) api() (*api, error) {
	c, err := s.NewClient()
	if err != nil {
		return nil, err
	}
	return &api{c: c}, nil
}

func decode(r *http.Request, v any) error {
	// 1 MiB is far above any real request here (the largest is a recipe) and
	// bounds what a stray client can make the server allocate.
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("parsing request body: %w", err)
	}
	return nil
}
