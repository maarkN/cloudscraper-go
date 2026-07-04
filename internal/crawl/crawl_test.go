package crawl_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/maarkN/cloudscraper-go/internal/crawl"
)

// fakeFetcher records concurrency and can simulate delay, per-URL failures and
// blocking (to drive cancellation deterministically).
type fakeFetcher struct {
	mu          sync.Mutex
	inFlight    int
	maxInFlight int
	calls       int

	delay    time.Duration
	failURLs map[string]bool

	started chan struct{} // signalled (once) when the first Fetch begins
	block   chan struct{} // when non-nil, Fetch waits on it (ignores ctx)
	once    sync.Once
}

func (f *fakeFetcher) Fetch(ctx context.Context, raw string) (int, []byte, error) {
	f.mu.Lock()
	f.calls++
	f.inFlight++
	if f.inFlight > f.maxInFlight {
		f.maxInFlight = f.inFlight
	}
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.inFlight--
		f.mu.Unlock()
	}()

	if f.started != nil {
		f.once.Do(func() { close(f.started) })
	}
	if f.block != nil {
		<-f.block // stay blocked, holding the worker slot
	}
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return 0, nil, ctx.Err()
		}
	}
	if f.failURLs[raw] {
		return 0, nil, errors.New("boom")
	}
	return 200, []byte("ok:" + raw), nil
}

func urls(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("https://host%d.example/page", i)
	}
	return out
}

func TestConcurrencyIsBounded(t *testing.T) {
	f := &fakeFetcher{delay: 20 * time.Millisecond}
	c := crawl.New(f, crawl.Options{Concurrency: 3})

	results, err := c.Crawl(context.Background(), urls(12))
	if err != nil {
		t.Fatalf("Crawl: %v", err)
	}
	if len(results) != 12 {
		t.Fatalf("got %d results, want 12", len(results))
	}
	if f.maxInFlight > 3 {
		t.Errorf("max in-flight = %d, want <= 3", f.maxInFlight)
	}
	if f.maxInFlight < 2 {
		t.Errorf("max in-flight = %d, expected real parallelism (>= 2)", f.maxInFlight)
	}
}

func TestResultsPreserveOrderAndData(t *testing.T) {
	in := urls(5)
	c := crawl.New(&fakeFetcher{}, crawl.Options{Concurrency: 4})

	results, err := c.Crawl(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	for i, r := range results {
		if r.URL != in[i] {
			t.Errorf("results[%d].URL = %q, want %q", i, r.URL, in[i])
		}
		if r.StatusCode != 200 || string(r.Body) != "ok:"+in[i] {
			t.Errorf("results[%d] = %d/%q, want 200/ok:%s", i, r.StatusCode, r.Body, in[i])
		}
	}
}

func TestPerURLErrorDoesNotAbort(t *testing.T) {
	in := urls(4)
	f := &fakeFetcher{failURLs: map[string]bool{in[2]: true}}
	c := crawl.New(f, crawl.Options{Concurrency: 4})

	results, err := c.Crawl(context.Background(), in)
	if err != nil {
		t.Fatalf("Crawl should not fail on a single URL error: %v", err)
	}
	if results[2].Err == nil {
		t.Error("expected results[2].Err to be set")
	}
	for _, i := range []int{0, 1, 3} {
		if results[i].Err != nil || results[i].StatusCode != 200 {
			t.Errorf("results[%d] = %d/%v, want 200/nil", i, results[i].StatusCode, results[i].Err)
		}
	}
}

func TestContextCancellationAborts(t *testing.T) {
	f := &fakeFetcher{started: make(chan struct{}), block: make(chan struct{})}
	c := crawl.New(f, crawl.Options{Concurrency: 1})

	ctx, cancel := context.WithCancel(context.Background())
	type outcome struct {
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		_, err := c.Crawl(ctx, urls(3))
		done <- outcome{err}
	}()

	<-f.started // first fetch is running and holds the only worker slot
	cancel()    // the acquire for url #2 is blocked on the full semaphore

	select {
	case o := <-done:
		if !errors.Is(o.err, context.Canceled) {
			t.Fatalf("Crawl err = %v, want context.Canceled", o.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Crawl did not return after cancellation")
	}
	close(f.block) // release the blocked worker goroutine
}

func TestPerHostRateLimit(t *testing.T) {
	// 50 rps, burst 1: first request is immediate, the next two each wait ~20ms.
	f := &fakeFetcher{}
	c := crawl.New(f, crawl.Options{Concurrency: 8, PerHostRPS: 50, PerHostBurst: 1})

	same := []string{
		"https://one.example/a",
		"https://one.example/b",
		"https://one.example/c",
	}
	start := time.Now()
	if _, err := c.Crawl(context.Background(), same); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed < 30*time.Millisecond {
		t.Errorf("elapsed %v, expected >= ~30ms from per-host rate limiting", elapsed)
	}
}

func TestMetricsRecorded(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := crawl.NewMetrics(reg)

	in := urls(2)
	f := &fakeFetcher{failURLs: map[string]bool{in[1]: true}}
	c := crawl.New(f, crawl.Options{Concurrency: 2, Metrics: m})

	if _, err := c.Crawl(context.Background(), in); err != nil {
		t.Fatal(err)
	}

	// One "ok" series + one "error" series expected on the requests counter.
	got, err := testutil.GatherAndCount(reg, "cloudscraper_crawl_requests_total")
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	if got != 2 {
		t.Errorf("request series = %d, want 2 (ok + error)", got)
	}
}
