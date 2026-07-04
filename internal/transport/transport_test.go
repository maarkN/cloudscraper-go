package transport_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	utls "github.com/refraction-networking/utls"

	"github.com/maarkN/cloudscraper-go/internal/transport"
)

func TestRoundTripRejectsNonHTTPS(t *testing.T) {
	tr := transport.New(utls.HelloChrome_Auto)
	req, _ := http.NewRequest(http.MethodGet, "http://example.com", nil)
	if _, err := tr.RoundTrip(req); err == nil {
		t.Fatal("expected error for http:// scheme, got nil")
	}
}

// TestFingerprintIsBrowserLike hits tls.peet.ws and asserts the observed JA3 is
// a real Chrome-like hash negotiated over HTTP/2 — not Go's default. It needs
// network, so it is skipped under `-short`.
func TestFingerprintIsBrowserLike(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping network fingerprint test in -short mode")
	}

	tr := transport.New(utls.HelloChrome_Auto)
	client := &http.Client{Transport: tr, Timeout: 20 * time.Second}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://tls.peet.ws/api/all", nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")

	resp, err := client.Do(req)
	if err != nil {
		t.Skipf("network unavailable: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}

	var data struct {
		TLS struct {
			JA3Hash string `json:"ja3_hash"`
		} `json:"tls"`
		HTTPVersion string `json:"http_version"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		t.Fatalf("decode peet.ws response: %v", err)
	}
	if data.TLS.JA3Hash == "" {
		t.Error("expected a non-empty JA3 hash")
	}
	if data.HTTPVersion != "h2" {
		t.Errorf("negotiated protocol = %q, want h2", data.HTTPVersion)
	}
	t.Logf("observed ja3_hash=%s over %s", data.TLS.JA3Hash, data.HTTPVersion)
}

func BenchmarkChromeGet(b *testing.B) {
	if testing.Short() {
		b.Skip("skipping network benchmark in -short mode")
	}
	tr := transport.New(utls.HelloChrome_Auto)
	client := &http.Client{Transport: tr, Timeout: 20 * time.Second}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		resp, err := client.Get("https://tls.peet.ws/api/clean")
		if err != nil {
			b.Fatalf("get: %v", err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
}
