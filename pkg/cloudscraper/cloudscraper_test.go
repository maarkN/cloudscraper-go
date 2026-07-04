package cloudscraper_test

import (
	"compress/gzip"
	"context"
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
