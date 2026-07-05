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

// benchServer returns a local TLS server that gzips a small HTML page — enough to
// exercise the full path (uTLS handshake, HTTP, gzip inflate) without network
// variance, so the numbers are reproducible.
func benchServer(b *testing.B) *httptest.Server {
	b.Helper()
	body := "<html><body><h1>Bench</h1>" +
		strings.Repeat("<p>lorem ipsum dolor sit amet consectetur</p>", 60) +
		"</body></html>"
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Content-Type", "text/html")
		gz := gzip.NewWriter(w)
		defer func() { _ = gz.Close() }()
		_, _ = gz.Write([]byte(body))
	}))
	b.Cleanup(srv.Close)
	return srv
}

func benchClient(b *testing.B) *cloudscraper.Client {
	b.Helper()
	c, err := cloudscraper.New(cloudscraper.WithInsecureSkipVerify())
	if err != nil {
		b.Fatalf("New: %v", err)
	}
	return c
}

// BenchmarkGet measures one full sequential fetch (fresh uTLS handshake + HTTP +
// gzip inflate) — the per-request cost with no connection pooling yet.
func BenchmarkGet(b *testing.B) {
	srv := benchServer(b)
	client := benchClient(b)
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := client.Get(ctx, srv.URL); err != nil {
			b.Fatalf("Get: %v", err)
		}
	}
}

// BenchmarkGetParallel measures throughput under concurrency (GOMAXPROCS
// goroutines sharing one Client).
func BenchmarkGetParallel(b *testing.B) {
	srv := benchServer(b)
	client := benchClient(b)
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if _, err := client.Get(ctx, srv.URL); err != nil {
				panic(err) // b.Fatal is not allowed from RunParallel goroutines
			}
		}
	})
}
