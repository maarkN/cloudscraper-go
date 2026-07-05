// Package server exposes cloudscraper-go over HTTP as a daemon that keeps solved
// sessions hot: each session id maps to its own long-lived fetcher (cookies stay
// warm across requests). It depends only on a small Doer/Factory pair, so the
// handlers are unit-testable without any network.
package server

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Doer fetches a single URL. A session's hot client satisfies this.
type Doer interface {
	Get(ctx context.Context, url string) (status int, header http.Header, body []byte, err error)
}

// Factory builds a fresh Doer (hot session) for the given browser profile.
type Factory func(profile string) (Doer, error)

// Options configures a Server.
type Options struct {
	// DefaultProfile is used when a request omits ?profile=.
	DefaultProfile string
	// IdleTTL evicts sessions unused for longer than this. 0 disables eviction.
	IdleTTL time.Duration
}

type sessionEntry struct {
	doer     Doer
	lastUsed time.Time
}

// Server is the HTTP daemon. Construct with New and mount Handler().
type Server struct {
	factory Factory
	opts    Options
	metrics *Metrics

	mu       sync.Mutex
	sessions map[string]sessionEntry

	// now is injectable so eviction is deterministically testable.
	now func() time.Time
}

// New builds a Server around factory.
func New(factory Factory, opts Options) *Server {
	if opts.DefaultProfile == "" {
		opts.DefaultProfile = "chrome"
	}
	return &Server{
		factory:  factory,
		opts:     opts,
		metrics:  NewMetrics(),
		sessions: make(map[string]sessionEntry),
		now:      time.Now,
	}
}

// Handler returns the HTTP handler for the daemon.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET /fetch", s.handleFetch)
	mux.HandleFunc("DELETE /sessions/{id}", s.handleCloseSession)
	mux.Handle("GET /metrics", promhttp.HandlerFor(s.metrics.registry, promhttp.HandlerOpts{}))
	return mux
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	_, _ = fmt.Fprintln(w, "ok")
}

func (s *Server) handleFetch(w http.ResponseWriter, r *http.Request) {
	target := r.URL.Query().Get("url")
	if target == "" {
		http.Error(w, "missing ?url=", http.StatusBadRequest)
		return
	}
	sessionID := r.URL.Query().Get("session")
	if sessionID == "" {
		sessionID = "default"
	}
	profile := r.URL.Query().Get("profile")
	if profile == "" {
		profile = s.opts.DefaultProfile
	}

	doer, err := s.session(sessionID, profile)
	if err != nil {
		http.Error(w, "session: "+err.Error(), http.StatusInternalServerError)
		return
	}

	s.metrics.inFlight.Inc()
	defer s.metrics.inFlight.Dec()

	start := s.now()
	status, header, body, err := doer.Get(r.Context(), target)
	s.metrics.observe(status, err, s.now().Sub(start))
	if err != nil {
		http.Error(w, "fetch: "+err.Error(), http.StatusBadGateway)
		return
	}

	if ct := header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.Header().Set("X-Upstream-Status", fmt.Sprintf("%d", status))
	w.Header().Set("X-Session", sessionID)
	_, _ = w.Write(body)
}

func (s *Server) handleCloseSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.mu.Lock()
	_, ok := s.sessions[id]
	delete(s.sessions, id)
	s.metrics.activeSessions.Set(float64(len(s.sessions)))
	s.mu.Unlock()
	if !ok {
		http.Error(w, "no such session", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// session returns the hot Doer for id, creating it on first use.
func (s *Server) session(id, profile string) (Doer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.sessions[id]; ok {
		e.lastUsed = s.now()
		s.sessions[id] = e
		return e.doer, nil
	}
	doer, err := s.factory(profile)
	if err != nil {
		return nil, err
	}
	s.sessions[id] = sessionEntry{doer: doer, lastUsed: s.now()}
	s.metrics.activeSessions.Set(float64(len(s.sessions)))
	return doer, nil
}

// evictIdle drops sessions unused for longer than ttl and returns how many were
// removed. It is called periodically by the janitor (see StartJanitor).
func (s *Server) evictIdle(ttl time.Duration) int {
	if ttl <= 0 {
		return 0
	}
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	removed := 0
	for id, e := range s.sessions {
		if now.Sub(e.lastUsed) > ttl {
			delete(s.sessions, id)
			removed++
		}
	}
	if removed > 0 {
		s.metrics.activeSessions.Set(float64(len(s.sessions)))
	}
	return removed
}

// StartJanitor runs idle eviction every interval until the returned stop func is
// called. It is a no-op when IdleTTL <= 0.
func (s *Server) StartJanitor(interval time.Duration) (stop func()) {
	if s.opts.IdleTTL <= 0 || interval <= 0 {
		return func() {}
	}
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				s.evictIdle(s.opts.IdleTTL)
			case <-done:
				return
			}
		}
	}()
	return func() { close(done) }
}
