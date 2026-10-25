package web

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/bschm0622/kroger-cart/internal/client"
	"github.com/bschm0622/kroger-cart/internal/cliutil"
	"github.com/bschm0622/kroger-cart/internal/config"
)

//go:embed assets
var assets embed.FS

type Server struct {
	Addr       string
	ConfigPath string
	NewClient  func() (*client.Client, error)

	paths paths

	mu        sync.Mutex
	limiter   *cliutil.AdaptiveLimiter
	coolUntil time.Time

	staplesMu sync.Mutex
}

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

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	ui, err := fs.Sub(assets, "assets")
	if err != nil {
		panic(err)
	}
	// Embedded files have no modtime; without no-cache the browser keeps an old app.js.
	files := http.FileServerFS(ui)
	mux.Handle("GET /", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		files.ServeHTTP(w, r)
	}))

	mux.HandleFunc("GET /api/status", s.handleStatus)
	mux.HandleFunc("POST /api/cookie", s.handleCookie)
	mux.HandleFunc("POST /api/cookie/chrome", s.handleCookieFromChrome)

	mux.HandleFunc("GET /api/cart", s.handleCartGet)
	mux.HandleFunc("POST /api/cart/update", s.handleCartUpdate)

	mux.HandleFunc("GET /api/staples", s.handleStaplesGet)
	mux.HandleFunc("PUT /api/staples", s.handleStaplesPut)
	mux.HandleFunc("POST /api/staples/seed", s.handleStaplesSeed)

	mux.HandleFunc("GET /api/recipes", s.handleRecipesGet)
	mux.HandleFunc("PUT /api/recipes", s.handleRecipePut)
	mux.HandleFunc("POST /api/recipes/delete", s.handleRecipeDelete)

	mux.HandleFunc("GET /api/search", s.handleSearch)
	mux.HandleFunc("POST /api/products", s.handleProducts)
	mux.HandleFunc("GET /api/history", s.handleHistory)

	return localOnly(mux)
}

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

// localOnly serves only this machine's own page: loopback, addressed to localhost
// (DNS rebinding), same origin, JSON writes (no preflight-free cross-site posts).
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
		if !isLocalHost(r.Host) {
			http.Error(w, "this server only answers to localhost", http.StatusForbidden)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && origin != "http://"+r.Host {
			http.Error(w, "cross-origin requests are not allowed", http.StatusForbidden)
			return
		}
		if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
			http.Error(w, "cross-site requests are not allowed", http.StatusForbidden)
			return
		}
		if r.Method == http.MethodPost || r.Method == http.MethodPut {
			if ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); ct != "application/json" {
				http.Error(w, "requests must be JSON", http.StatusUnsupportedMediaType)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func isLocalHost(hostport string) bool {
	host, _, err := net.SplitHostPort(hostport)
	if err != nil {
		host = hostport
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

type failure struct {
	Kind string
	Msg  string
}

var errCoolingDown = errors.New("cooling down after a rate limit")

func classify(err error) failure {
	var apiErr *client.APIError
	switch {
	case errors.Is(err, errCoolingDown):
		return failure{"rate_limited", "Kroger is limiting requests right now. Wait a minute, then try again."}
	case errors.Is(err, errNoActiveCart):
		return failure{"no_cart", "Your Kroger account has no cart yet. Add any item on kroger.com once, then reload this page."}
	case errors.As(err, &apiErr):
		switch {
		case apiErr.StatusCode == http.StatusUnauthorized:
			return failure{"auth", "Your Kroger session has expired."}
		case apiErr.StatusCode == http.StatusForbidden:
			return failure{"auth", "Kroger refused the saved session."}
		case apiErr.StatusCode == http.StatusTooManyRequests:
			return failure{"rate_limited", "Kroger is limiting requests right now. Wait a minute, then try again."}
		case apiErr.StatusCode == http.StatusBadRequest && strings.Contains(apiErr.Body, "LAF_OBJECTS_REQUIRED"):
			return failure{"no_store", "Kroger needs to know which store you shop at. Set it with `kroger-pp-cli auth set-store` (see the README)."}
		case apiErr.StatusCode >= 500:
			return failure{"kroger_down", fmt.Sprintf("Kroger's servers had a problem (HTTP %d). Try again in a moment.", apiErr.StatusCode)}
		default:
			return failure{"other", fmt.Sprintf("Kroger rejected the request (HTTP %d).", apiErr.StatusCode)}
		}
	}
	// Akamai drops the HTTP/2 stream, with no status code, when the session has gone stale.
	if msg := err.Error(); strings.Contains(msg, "INTERNAL_ERROR") || strings.Contains(msg, "stream error") {
		return failure{"auth", "Kroger's bot protection turned the request away, which is what an old session looks like."}
	}
	return failure{"network", "Couldn't reach Kroger: " + err.Error()}
}

func writeFailure(w http.ResponseWriter, status int, f failure) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error":       f.Msg,
		"kind":        f.Kind,
		"needsCookie": f.Kind == "auth",
	})
}

func (s *Server) fail(w http.ResponseWriter, err error) {
	s.noteRateLimit(err)
	writeFailure(w, http.StatusBadGateway, classify(err))
}

// noteRateLimit pauses Kroger calls for a minute after a 429; retrying sooner prolongs it.
func (s *Server) noteRateLimit(err error) {
	var apiErr *client.APIError
	if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusTooManyRequests {
		s.mu.Lock()
		s.coolUntil = time.Now().Add(time.Minute)
		s.mu.Unlock()
	}
}

func badRequest(w http.ResponseWriter, err error) {
	writeFailure(w, http.StatusBadRequest, failure{Kind: "bad_request", Msg: err.Error()})
}

func (s *Server) api() (*api, error) {
	s.mu.Lock()
	cooling := time.Now().Before(s.coolUntil)
	s.mu.Unlock()
	if cooling {
		return nil, errCoolingDown
	}
	c, err := s.NewClient()
	if err != nil {
		return nil, err
	}
	c.NoCache = true
	c.NoRetryOn429 = true
	s.mu.Lock()
	if s.limiter == nil {
		s.limiter = c.Limiter()
	} else {
		c.ShareLimiter(s.limiter)
	}
	s.mu.Unlock()
	return &api{c: c}, nil
}

func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("parsing request body: %w", err)
	}
	return nil
}
