// Package retry provides an http.RoundTripper that transparently retries
// transient failures — network errors and configurable status codes — with
// exponential backoff and full jitter, honouring the Retry-After header and the
// request's context deadline/cancellation.
package retry

import (
	"context"
	"errors"
	"io"
	"math"
	"math/rand"
	"net/http"
	"strconv"
	"time"
)

// RoundTripper wraps Next, retrying failed requests up to MaxRetries times.
type RoundTripper struct {
	Next          http.RoundTripper
	MaxRetries    int
	BaseDelay     time.Duration
	MaxDelay      time.Duration
	RetryStatuses map[int]bool

	// sleep and rnd are injectable so tests stay fast and deterministic.
	sleep func(ctx context.Context, d time.Duration) error
	rnd   func() float64
}

// DefaultRetryStatuses are retried by default: rate limiting and transient
// upstream errors.
func DefaultRetryStatuses() map[int]bool {
	return map[int]bool{
		http.StatusTooManyRequests:    true, // 429
		http.StatusBadGateway:         true, // 502
		http.StatusServiceUnavailable: true, // 503
		http.StatusGatewayTimeout:     true, // 504
	}
}

// New returns a RoundTripper with sensible defaults around next.
func New(next http.RoundTripper, maxRetries int) *RoundTripper {
	return &RoundTripper{
		Next:          next,
		MaxRetries:    maxRetries,
		BaseDelay:     250 * time.Millisecond,
		MaxDelay:      10 * time.Second,
		RetryStatuses: DefaultRetryStatuses(),
	}
}

// RoundTrip implements http.RoundTripper.
func (rt *RoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	sleep := rt.sleep
	if sleep == nil {
		sleep = sleepCtx
	}
	rnd := rt.rnd
	if rnd == nil {
		rnd = rand.Float64
	}

	var (
		resp *http.Response
		err  error
	)
	for attempt := 0; ; attempt++ {
		if attempt > 0 {
			if req.Body != nil { // rewind for a fresh attempt
				body, gerr := req.GetBody()
				if gerr != nil {
					return resp, err
				}
				req.Body = body
			}
			delay := backoff(rt.BaseDelay, rt.MaxDelay, attempt, rnd, retryAfter(resp))
			if serr := sleep(req.Context(), delay); serr != nil {
				return resp, serr
			}
		}

		resp, err = rt.Next.RoundTrip(req)
		if attempt >= rt.MaxRetries || !rt.retryable(req, resp, err) {
			return resp, err
		}
		drain(resp) // discard this attempt's body before retrying
	}
}

func (rt *RoundTripper) retryable(req *http.Request, resp *http.Response, err error) bool {
	// A body we cannot rewind rules out any retry.
	if req.Body != nil && req.GetBody == nil {
		return false
	}
	if err != nil {
		// Context cancellation / deadline is terminal — don't hammer.
		return !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded)
	}
	return resp != nil && rt.RetryStatuses[resp.StatusCode]
}

func drain(resp *http.Response) {
	if resp == nil || resp.Body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	_ = resp.Body.Close()
}

// backoff returns the wait before the given (1-based) attempt. A server-provided
// Retry-After hint wins when present; otherwise exponential base*2^(n-1) capped
// at MaxDelay, with full jitter.
func backoff(base, maxDelay time.Duration, attempt int, rnd func() float64, serverHint time.Duration) time.Duration {
	if serverHint > 0 {
		if serverHint > maxDelay {
			return maxDelay
		}
		return serverHint
	}
	if base <= 0 {
		return 0
	}
	capped := float64(base) * math.Pow(2, float64(attempt-1))
	if capped > float64(maxDelay) {
		capped = float64(maxDelay)
	}
	return time.Duration(rnd() * capped)
}

func retryAfter(resp *http.Response) time.Duration {
	if resp == nil {
		return 0
	}
	v := resp.Header.Get("Retry-After")
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second
	}
	if when, err := http.ParseTime(v); err == nil {
		if d := time.Until(when); d > 0 {
			return d
		}
	}
	return 0
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			return nil
		}
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
