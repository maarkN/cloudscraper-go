package retry

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// stubRT returns programmed responses/errors, one per call.
type stubRT struct {
	calls     int32
	responses []*http.Response
	errs      []error
}

func (s *stubRT) RoundTrip(*http.Request) (*http.Response, error) {
	i := int(atomic.AddInt32(&s.calls, 1)) - 1
	var resp *http.Response
	var err error
	if i < len(s.responses) {
		resp = s.responses[i]
	}
	if i < len(s.errs) {
		err = s.errs[i]
	}
	return resp, err
}

func respStatus(status int) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader("body")),
	}
}

// testRT wires the wrapper with no real waiting and deterministic jitter.
func testRT(next http.RoundTripper, maxRetries int) *RoundTripper {
	rt := New(next, maxRetries)
	rt.BaseDelay = 0
	rt.rnd = func() float64 { return 0.5 }
	rt.sleep = func(ctx context.Context, _ time.Duration) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			return nil
		}
	}
	return rt
}

func mustReq(t *testing.T) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "https://example.com", nil)
	if err != nil {
		t.Fatal(err)
	}
	return req
}

func TestRetriesNetworkErrorThenSucceeds(t *testing.T) {
	netErr := errors.New("connection reset")
	stub := &stubRT{
		responses: []*http.Response{nil, nil, respStatus(200)},
		errs:      []error{netErr, netErr, nil},
	}
	resp, err := testRT(stub, 3).RoundTrip(mustReq(t))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if stub.calls != 3 {
		t.Errorf("calls = %d, want 3", stub.calls)
	}
}

func TestRetriesRetryableStatusThenSucceeds(t *testing.T) {
	stub := &stubRT{responses: []*http.Response{respStatus(503), respStatus(200)}}
	resp, err := testRT(stub, 2).RoundTrip(mustReq(t))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StatusCode != 200 || stub.calls != 2 {
		t.Errorf("status=%d calls=%d, want 200/2", resp.StatusCode, stub.calls)
	}
}

func TestDoesNotRetryNonRetryableStatus(t *testing.T) {
	stub := &stubRT{responses: []*http.Response{respStatus(404)}}
	resp, err := testRT(stub, 3).RoundTrip(mustReq(t))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 404 || stub.calls != 1 {
		t.Errorf("status=%d calls=%d, want 404/1", resp.StatusCode, stub.calls)
	}
}

func TestExhaustsRetriesReturnsLast(t *testing.T) {
	stub := &stubRT{responses: []*http.Response{respStatus(503), respStatus(503), respStatus(503)}}
	resp, err := testRT(stub, 2).RoundTrip(mustReq(t))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 503 || stub.calls != 3 {
		t.Errorf("status=%d calls=%d, want 503/3", resp.StatusCode, stub.calls)
	}
}

func TestContextCancellationStopsRetries(t *testing.T) {
	stub := &stubRT{responses: []*http.Response{respStatus(503), respStatus(200)}}
	rt := testRT(stub, 3)
	rt.sleep = func(context.Context, time.Duration) error { return context.Canceled }

	_, err := rt.RoundTrip(mustReq(t))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if stub.calls != 1 {
		t.Errorf("calls = %d, want 1 (no retry after cancel)", stub.calls)
	}
}

func TestNonRewindableBodyNotRetried(t *testing.T) {
	// io.NopCloser is not a known body type, so http.NewRequest leaves GetBody nil.
	req, err := http.NewRequest(http.MethodPost, "https://example.com", io.NopCloser(strings.NewReader("data")))
	if err != nil {
		t.Fatal(err)
	}
	if req.GetBody != nil {
		t.Fatal("precondition failed: expected GetBody to be nil")
	}
	stub := &stubRT{responses: []*http.Response{respStatus(503), respStatus(200)}}
	resp, rerr := testRT(stub, 3).RoundTrip(req)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if resp.StatusCode != 503 || stub.calls != 1 {
		t.Errorf("status=%d calls=%d, want 503/1 (non-rewindable body)", resp.StatusCode, stub.calls)
	}
}

func TestRetryAfterHeaderHonoured(t *testing.T) {
	r503 := respStatus(503)
	r503.Header.Set("Retry-After", "2")
	stub := &stubRT{responses: []*http.Response{r503, respStatus(200)}}

	var delays []time.Duration
	rt := testRT(stub, 2)
	rt.sleep = func(_ context.Context, d time.Duration) error {
		delays = append(delays, d)
		return nil
	}

	if _, err := rt.RoundTrip(mustReq(t)); err != nil {
		t.Fatal(err)
	}
	if len(delays) != 1 || delays[0] != 2*time.Second {
		t.Errorf("delays = %v, want a single 2s wait", delays)
	}
}

func TestBackoffIsExponentialAndCapped(t *testing.T) {
	base := 100 * time.Millisecond
	maxDelay := 1 * time.Second
	full := func() float64 { return 1.0 } // no jitter reduction

	if got := backoff(base, maxDelay, 1, full, 0); got != 100*time.Millisecond {
		t.Errorf("attempt 1 = %v, want 100ms", got)
	}
	if got := backoff(base, maxDelay, 2, full, 0); got != 200*time.Millisecond {
		t.Errorf("attempt 2 = %v, want 200ms", got)
	}
	if got := backoff(base, maxDelay, 10, full, 0); got != maxDelay {
		t.Errorf("attempt 10 = %v, want cap %v", got, maxDelay)
	}
}
