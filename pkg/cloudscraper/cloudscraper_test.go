package cloudscraper_test

import (
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/maarkN/cloudscraper-go/pkg/cloudscraper"
)

// TestGetOverTLS exercises the full stack (uTLS handshake -> HTTP/1.1 ->
// gzip decompression) against a local TLS server, so it stays deterministic and
// offline — safe under `go test -race -short`.
func TestGetOverTLS(t *testing.T) {
	want := "hello from a gzip body — " + strings.Repeat("x", 2000)

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("User-Agent"); !strings.Contains(got, "Chrome") {
			t.Errorf("server saw User-Agent %q, want it to contain Chrome", got)
		}
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Content-Type", "text/plain")
		gz := gzip.NewWriter(w)
		defer func() { _ = gz.Close() }()
		_, _ = gz.Write([]byte(want))
	}))
	defer srv.Close()

	client, err := cloudscraper.New(cloudscraper.WithInsecureSkipVerify())
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	resp, err := client.Get(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if resp.String() != want {
		t.Errorf("body mismatch: got %d bytes, want %d", len(resp.Body), len(want))
	}
	if enc := resp.Header.Get("Content-Encoding"); enc != "" {
		t.Errorf("Content-Encoding should be stripped after decompression, got %q", enc)
	}
}

func TestUnknownProfileFails(t *testing.T) {
	if _, err := cloudscraper.New(cloudscraper.WithProfile("netscape")); err == nil {
		t.Fatal("expected error for unknown profile")
	}
}

func TestGetRejectsHTTP(t *testing.T) {
	client, err := cloudscraper.New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := client.Get(context.Background(), "http://example.com"); err == nil {
		t.Fatal("expected error for plain-http URL, got nil")
	}
}

func TestInvalidProxyURLFails(t *testing.T) {
	if _, err := cloudscraper.New(cloudscraper.WithProxy("://nope")); err == nil {
		t.Fatal("expected error for invalid proxy url")
	}
}

// TestCookieSessionReuse proves the Client keeps a session hot: a cookie set on
// the first request is sent back on the next one (deterministic, offline).
func TestCookieSessionReuse(t *testing.T) {
	var secondReqCookie string
	mux := http.NewServeMux()
	mux.HandleFunc("/set", func(w http.ResponseWriter, _ *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "sid", Value: "abc123", Path: "/"})
		_, _ = io.WriteString(w, "set")
	})
	mux.HandleFunc("/check", func(w http.ResponseWriter, r *http.Request) {
		secondReqCookie = r.Header.Get("Cookie")
		_, _ = io.WriteString(w, "check")
	})
	srv := httptest.NewTLSServer(mux)
	defer srv.Close()

	client, err := cloudscraper.New(cloudscraper.WithInsecureSkipVerify())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := client.Get(context.Background(), srv.URL+"/set"); err != nil {
		t.Fatalf("first Get: %v", err)
	}
	if _, err := client.Get(context.Background(), srv.URL+"/check"); err != nil {
		t.Fatalf("second Get: %v", err)
	}
	if !strings.Contains(secondReqCookie, "sid=abc123") {
		t.Errorf("second request Cookie = %q, want it to carry sid=abc123", secondReqCookie)
	}

	cookies, err := client.Cookies(srv.URL)
	if err != nil {
		t.Fatalf("Cookies: %v", err)
	}
	if len(cookies) == 0 {
		t.Error("jar holds no cookies for the server")
	}
}

// TestCustomHeaderIsSent proves WithHeader (and the CLI's -H flag) reaches the
// server: an Authorization header set on the client shows up on the request.
func TestCustomHeaderIsSent(t *testing.T) {
	var gotAuth, gotKey string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotKey = r.Header.Get("X-Api-Key")
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()

	client, err := cloudscraper.New(
		cloudscraper.WithInsecureSkipVerify(),
		cloudscraper.WithHeader("Authorization", "Bearer secret-token"),
		cloudscraper.WithHeader("X-Api-Key", "k-123"),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := client.Get(context.Background(), srv.URL); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if gotAuth != "Bearer secret-token" {
		t.Errorf("Authorization = %q, want Bearer secret-token", gotAuth)
	}
	if gotKey != "k-123" {
		t.Errorf("X-Api-Key = %q, want k-123", gotKey)
	}
}
