package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type fakeDoer struct {
	status int
	body   []byte
	err    error
	calls  int32
}

func (d *fakeDoer) Get(context.Context, string) (int, http.Header, []byte, error) {
	atomic.AddInt32(&d.calls, 1)
	if d.err != nil {
		return 0, nil, nil, d.err
	}
	h := http.Header{}
	h.Set("Content-Type", "text/plain")
	return d.status, h, d.body, nil
}

// countingFactory returns a factory that builds a new fakeDoer per call, plus a
// pointer to the call count.
func countingFactory() (Factory, *int32) {
	var calls int32
	f := func(string) (Doer, error) {
		atomic.AddInt32(&calls, 1)
		return &fakeDoer{status: 200, body: []byte("hello")}, nil
	}
	return f, &calls
}

func do(h http.Handler, method, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	return rec
}

func TestHealthz(t *testing.T) {
	f, _ := countingFactory()
	rec := do(New(f, Options{}).Handler(), http.MethodGet, "/healthz")
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "ok" {
		t.Errorf("healthz = %d %q", rec.Code, rec.Body.String())
	}
}

func TestFetchOK(t *testing.T) {
	f, _ := countingFactory()
	rec := do(New(f, Options{}).Handler(), http.MethodGet, "/fetch?url=https://x.example")
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rec.Code)
	}
	if rec.Body.String() != "hello" {
		t.Errorf("body = %q, want hello", rec.Body.String())
	}
	if rec.Header().Get("X-Upstream-Status") != "200" {
		t.Errorf("X-Upstream-Status = %q, want 200", rec.Header().Get("X-Upstream-Status"))
	}
	if rec.Header().Get("Content-Type") != "text/plain" {
		t.Errorf("Content-Type = %q", rec.Header().Get("Content-Type"))
	}
}

func TestFetchMissingURL(t *testing.T) {
	f, _ := countingFactory()
	if rec := do(New(f, Options{}).Handler(), http.MethodGet, "/fetch"); rec.Code != http.StatusBadRequest {
		t.Errorf("code = %d, want 400", rec.Code)
	}
}

func TestFetchUpstreamError(t *testing.T) {
	f := func(string) (Doer, error) { return &fakeDoer{err: errors.New("boom")}, nil }
	if rec := do(New(f, Options{}).Handler(), http.MethodGet, "/fetch?url=https://x"); rec.Code != http.StatusBadGateway {
		t.Errorf("code = %d, want 502", rec.Code)
	}
}

func TestSessionReuse(t *testing.T) {
	f, calls := countingFactory()
	h := New(f, Options{}).Handler()
	for i := 0; i < 2; i++ {
		if rec := do(h, http.MethodGet, "/fetch?url=https://x&session=a"); rec.Code != http.StatusOK {
			t.Fatalf("request %d code = %d", i, rec.Code)
		}
	}
	if *calls != 1 {
		t.Errorf("factory calls = %d, want 1 (session reused)", *calls)
	}
}

func TestSessionIsolation(t *testing.T) {
	f, calls := countingFactory()
	h := New(f, Options{}).Handler()
	do(h, http.MethodGet, "/fetch?url=https://x&session=a")
	do(h, http.MethodGet, "/fetch?url=https://x&session=b")
	if *calls != 2 {
		t.Errorf("factory calls = %d, want 2 (distinct sessions)", *calls)
	}
}

func TestCloseSession(t *testing.T) {
	f, _ := countingFactory()
	s := New(f, Options{})
	h := s.Handler()

	do(h, http.MethodGet, "/fetch?url=https://x&session=a")
	if rec := do(h, http.MethodDelete, "/sessions/a"); rec.Code != http.StatusNoContent {
		t.Fatalf("close = %d, want 204", rec.Code)
	}
	s.mu.Lock()
	_, ok := s.sessions["a"]
	s.mu.Unlock()
	if ok {
		t.Error("session a should be gone after close")
	}
	if rec := do(h, http.MethodDelete, "/sessions/zzz"); rec.Code != http.StatusNotFound {
		t.Errorf("closing unknown session = %d, want 404", rec.Code)
	}
}

func TestEvictIdle(t *testing.T) {
	f, _ := countingFactory()
	s := New(f, Options{IdleTTL: time.Minute})
	t0 := time.Unix(1_000_000, 0)
	s.now = func() time.Time { return t0 }

	if _, err := s.session("a", "chrome"); err != nil {
		t.Fatal(err)
	}
	// Not yet idle.
	if n := s.evictIdle(time.Minute); n != 0 {
		t.Fatalf("evicted %d too early, want 0", n)
	}
	// Advance past the TTL.
	s.now = func() time.Time { return t0.Add(2 * time.Minute) }
	if n := s.evictIdle(time.Minute); n != 1 {
		t.Fatalf("evicted %d, want 1", n)
	}
	s.mu.Lock()
	_, ok := s.sessions["a"]
	s.mu.Unlock()
	if ok {
		t.Error("session a should have been evicted")
	}
}

func TestMetricsEndpoint(t *testing.T) {
	f, _ := countingFactory()
	h := New(f, Options{}).Handler()
	do(h, http.MethodGet, "/fetch?url=https://x") // generate a metric

	rec := do(h, http.MethodGet, "/metrics")
	if rec.Code != http.StatusOK {
		t.Fatalf("/metrics = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "cloudscraper_server_requests_total") {
		t.Error("/metrics missing cloudscraper_server_requests_total")
	}
}

func TestMethodNotAllowed(t *testing.T) {
	f, _ := countingFactory()
	if rec := do(New(f, Options{}).Handler(), http.MethodPost, "/healthz"); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /healthz = %d, want 405", rec.Code)
	}
}
