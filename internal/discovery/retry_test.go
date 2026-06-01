package discovery

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// newTestRetryClient builds a retry client with no real sleeping, for tests.
func newTestRetryClient() *http.Client {
	c := newRetryClient(nil)
	c.Transport.(*retryTransport).sleep = func(ctx context.Context, d time.Duration) error {
		return ctx.Err()
	}
	return c
}

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
	if d, ok := retryAfter(mk("5")); !ok || d != 5*time.Second {
		t.Errorf("delta-seconds: got %v ok=%v", d, ok)
	}
	if _, ok := retryAfter(&http.Response{Header: http.Header{}}); ok {
		t.Error("missing header should not parse")
	}
	if _, ok := retryAfter(mk("garbage")); ok {
		t.Error("garbage should not parse")
	}
}
