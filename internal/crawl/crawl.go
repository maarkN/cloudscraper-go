// Package crawl fetches many URLs concurrently with a bounded worker pool,
// per-host rate limiting, cancellation and backpressure. It depends only on a
// small Fetcher interface, so it is decoupled from the HTTP client and easy to
// test without network.
package crawl

import (
	"context"
	"net/url"
	"runtime"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
	"golang.org/x/time/rate"
)

// Fetcher retrieves a single URL. Small interface at the boundary.
type Fetcher interface {
	Fetch(ctx context.Context, rawURL string) (status int, body []byte, err error)
}

// Result is the outcome of fetching one URL.
type Result struct {
	URL        string
	StatusCode int
	Body       []byte
	Err        error
	Duration   time.Duration
}

// Options configures a Crawler.
type Options struct {
	// Concurrency bounds in-flight fetches. Defaults to NumCPU*2 when < 1.
	Concurrency int
	// PerHostRPS limits requests per second to each host; <= 0 disables it.
	PerHostRPS float64
	// PerHostBurst is the token-bucket burst per host (default 1).
	PerHostBurst int
	// Metrics, when non-nil, records per-request counters and durations.
	Metrics *Metrics
}

// Crawler runs a bounded, rate-limited concurrent crawl.
type Crawler struct {
	fetcher  Fetcher
	opts     Options
	limiters sync.Map // host -> *rate.Limiter
}

// New builds a Crawler around fetcher.
func New(fetcher Fetcher, opts Options) *Crawler {
	if opts.Concurrency < 1 {
		opts.Concurrency = runtime.NumCPU() * 2
	}
	if opts.PerHostBurst < 1 {
		opts.PerHostBurst = 1
	}
	return &Crawler{fetcher: fetcher, opts: opts}
}

// Crawl fetches every URL concurrently and returns the results in the same order
// as the input. Per-URL failures are captured in Result.Err (they do not abort
// siblings); a cancelled context aborts the crawl and returns ctx.Err().
func (c *Crawler) Crawl(ctx context.Context, urls []string) ([]Result, error) {
	results := make([]Result, len(urls))
	sem := make(chan struct{}, c.opts.Concurrency)
	g, ctx := errgroup.WithContext(ctx)

	for i, raw := range urls {
		// Acquire a worker slot; block here for backpressure, but bail on cancel.
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			return results, ctx.Err()
		}
		g.Go(func() error {
			defer func() { <-sem }()
			results[i] = c.fetchOne(ctx, raw)
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return results, err
	}
	return results, nil
}

func (c *Crawler) fetchOne(ctx context.Context, raw string) Result {
	res := Result{URL: raw}
	if err := c.waitForRateLimit(ctx, raw); err != nil {
		res.Err = err
		c.record(raw, res)
		return res
	}
	start := time.Now()
	status, body, err := c.fetcher.Fetch(ctx, raw)
	res.Duration = time.Since(start)
	res.StatusCode, res.Body, res.Err = status, body, err
	c.record(raw, res)
	return res
}

func (c *Crawler) waitForRateLimit(ctx context.Context, raw string) error {
	if c.opts.PerHostRPS <= 0 {
		return nil
	}
	return c.limiterFor(hostOf(raw)).Wait(ctx)
}

func (c *Crawler) limiterFor(host string) *rate.Limiter {
	if v, ok := c.limiters.Load(host); ok {
		return v.(*rate.Limiter)
	}
	lim := rate.NewLimiter(rate.Limit(c.opts.PerHostRPS), c.opts.PerHostBurst)
	actual, _ := c.limiters.LoadOrStore(host, lim)
	return actual.(*rate.Limiter)
}

func (c *Crawler) record(raw string, res Result) {
	if c.opts.Metrics != nil {
		c.opts.Metrics.observe(hostOf(raw), res)
	}
}

func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw
	}
	return u.Host
}
