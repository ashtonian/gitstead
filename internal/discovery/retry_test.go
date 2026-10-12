package discovery

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

// newTestRetryClient builds a retry client with no real sleeping, for tests.
func newTestRetryClient() *http.Client {
	c, _ := sleepRecordingClient(0)
	return c
}

// sleepRecordingClient builds a retry client whose sleeps are recorded, not
// slept, with a fixed clock.
func sleepRecordingClient(attemptTimeout time.Duration) (*http.Client, *[]time.Duration) {
	var slept []time.Duration
	c := newRetryClient(attemptTimeout)
	rt := c.Transport.(*retryTransport)
	rt.sleep = func(ctx context.Context, d time.Duration) error {
		slept = append(slept, d)
		return ctx.Err()
	}
	rt.now = func() time.Time { return testNow }
	return c, &slept
}

var testNow = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

func TestRetryOn429ThenSuccess(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	resp, err := newTestRetryClient().Get(srv.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("calls = %d, want 2 (one 429 + one success)", got)
	}
}

func TestRetryOn5xxThenSuccess(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) <= 2 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	resp, err := newTestRetryClient().Get(srv.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Errorf("calls = %d, want 3", got)
	}
}

func TestRetryGivesUpAfterMax(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	resp, err := newTestRetryClient().Get(srv.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 (final attempt returned)", resp.StatusCode)
	}
	// 1 initial + 5 retries = 6 total attempts.
	if got := atomic.LoadInt32(&calls); got != 6 {
		t.Errorf("calls = %d, want 6", got)
	}
}

func TestNoRetryOn403WithoutRateLimit(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusForbidden) // plain 403, no rate-limit headers
	}))
	defer srv.Close()

	resp, err := newTestRetryClient().Get(srv.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("calls = %d, want 1 (plain 403 not retried)", got)
	}
}

func TestRetryOn403SecondaryRateLimit(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusForbidden) // GitHub secondary rate limit
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	resp, err := newTestRetryClient().Get(srv.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("calls = %d, want 2", got)
	}
}

func TestRetryAfterParsing(t *testing.T) {
	mk := func(v string) *http.Response {
		return &http.Response{Header: http.Header{"Retry-After": []string{v}}}
	}
	if d, ok := retryAfter(mk("5"), testNow); !ok || d != 5*time.Second {
		t.Errorf("delta-seconds: got %v ok=%v", d, ok)
	}
	if _, ok := retryAfter(&http.Response{Header: http.Header{}}, testNow); ok {
		t.Error("missing header should not parse")
	}
	if _, ok := retryAfter(mk("garbage"), testNow); ok {
		t.Error("garbage should not parse")
	}
}

// The wait a server asks for is honoured in full, not capped: a shorter one
// only burns retries and, on GitHub, extends the penalty.
func TestRetry_HonoursTheRequestedWait(t *testing.T) {
	tests := []struct {
		name   string
		header http.Header
		status int
		want   time.Duration
	}{
		{"Retry-After", http.Header{"Retry-After": {"120"}}, http.StatusTooManyRequests, 2 * time.Minute},
		{"the primary limit's reset", http.Header{
			"X-Ratelimit-Remaining": {"0"}, "X-Ratelimit-Reset": {strconv.FormatInt(testNow.Add(5*time.Minute).Unix(), 10)},
		}, http.StatusForbidden, 5*time.Minute + time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if atomic.AddInt32(&calls, 1) == 1 {
					for k, v := range tt.header {
						w.Header()[k] = v
					}
					w.WriteHeader(tt.status)
					return
				}
				w.WriteHeader(http.StatusOK)
			}))
			defer srv.Close()

			c, slept := sleepRecordingClient(0)
			resp, err := c.Get(srv.URL)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK || len(*slept) != 1 || (*slept)[0] != tt.want {
				t.Errorf("status %d after sleeping %v; want 200 after %v", resp.StatusCode, *slept, tt.want)
			}
		})
	}
}

// A wait that would outlast the rule's deadline fails the request at once,
// saying when the limit lifts, instead of sleeping into the deadline.
func TestRetry_RateLimitPastTheDeadline(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Retry-After", "600")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	c, slept := sleepRecordingClient(0)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	_, err := c.Do(req)
	var rl *RateLimitError
	if !errors.As(err, &rl) || !rl.Until.Equal(testNow.Add(10*time.Minute)) {
		t.Fatalf("err = %v; want a RateLimitError lifting at %v", err, testNow.Add(10*time.Minute))
	}
	if len(*slept) != 0 || atomic.LoadInt32(&calls) != 1 {
		t.Errorf("slept %v over %d calls; want no sleep and one call", *slept, calls)
	}
}

// An attempt that hangs past api_timeout is abandoned and retried; the
// request goes on.
func TestRetry_AttemptTimeout(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			<-r.Context().Done()
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c, _ := sleepRecordingClient(100 * time.Millisecond)
	resp, err := c.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || atomic.LoadInt32(&calls) != 2 {
		t.Errorf("status %d after %d calls; want 200 after a timed-out attempt and a retry", resp.StatusCode, calls)
	}
}
