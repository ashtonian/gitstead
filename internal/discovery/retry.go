package discovery

import (
	"context"
	"errors"
	"io"
	"math"
	"net/http"
	"strconv"
	"time"
)

// retryTransport is an http.RoundTripper that retries requests on transient
// failures: HTTP 429 (rate limit), 403 with a rate-limit signal (GitHub's
// secondary/abuse limit), and 5xx. It honors the Retry-After header when present
// and otherwise uses capped exponential backoff. go-github and the Gitea SDK
// both accept a custom *http.Client, so this gives them the retry behavior the
// GitLab SDK already has built in.
type retryTransport struct {
	base       http.RoundTripper
	maxRetries int
	baseDelay  time.Duration
	maxDelay   time.Duration
	// sleep is overridable in tests; defaults to a context-aware sleep.
	sleep func(ctx context.Context, d time.Duration) error
}

// newRetryClient wraps an existing *http.Client (or http.DefaultTransport) with
// retry behavior, preserving the original transport (e.g. oauth2's).
func newRetryClient(base *http.Client) *http.Client {
	var rt http.RoundTripper = http.DefaultTransport
	timeout := time.Duration(0)
	if base != nil {
		if base.Transport != nil {
			rt = base.Transport
		}
		timeout = base.Timeout
	}
	return &http.Client{
		Transport: &retryTransport{
			base:       rt,
			maxRetries: 5,
			baseDelay:  time.Second,
			maxDelay:   30 * time.Second,
			sleep:      sleepCtx,
		},
		Timeout: timeout,
	}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (t *retryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx := req.Context()
	var lastResp *http.Response
	var lastErr error

	for attempt := 0; attempt <= t.maxRetries; attempt++ {
		if attempt > 0 {
			delay := t.backoff(attempt, lastResp)
			if err := t.sleep(ctx, delay); err != nil {
				return nil, err
			}
		}

		// Rewind the body for replayed requests (discovery uses GETs with no
		// body, but be correct for any retried request that has one).
		if req.Body != nil && req.GetBody != nil {
			body, err := req.GetBody()
			if err != nil {
				return nil, err
			}
			req.Body = body
		}

		resp, err := t.base.RoundTrip(req)
		if err != nil {
			// Don't retry on context cancellation/deadline.
			if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil, err
			}
			lastErr, lastResp = err, nil
			continue
		}

		if !shouldRetry(resp) || attempt == t.maxRetries {
			return resp, nil
		}
		// Drain & close the body so the connection can be reused before retry.
		drainAndClose(resp)
		lastResp, lastErr = resp, nil
	}

	if lastErr != nil {
		return nil, lastErr
	}
	return lastResp, nil
}

// shouldRetry reports whether a response is a transient failure worth retrying.
func shouldRetry(resp *http.Response) bool {
	switch resp.StatusCode {
	case http.StatusTooManyRequests: // 429
		return true
	case http.StatusForbidden: // 403: GitHub secondary/abuse rate limit
		if resp.Header.Get("Retry-After") != "" {
			return true
		}
		// Primary rate limit: remaining == 0 with a reset in the future.
		return resp.Header.Get("X-RateLimit-Remaining") == "0"
	}
	return resp.StatusCode >= 500 && resp.StatusCode != http.StatusNotImplemented
}

// backoff returns the delay before the given attempt (1-based), honoring
// Retry-After from the last response when present.
func (t *retryTransport) backoff(attempt int, resp *http.Response) time.Duration {
	if resp != nil {
		if d, ok := retryAfter(resp); ok {
			if d > t.maxDelay {
				return t.maxDelay
			}
			return d
		}
	}
	delay := time.Duration(float64(t.baseDelay) * math.Pow(2, float64(attempt-1)))
	if delay > t.maxDelay {
		return t.maxDelay
	}
	return delay
}

// retryAfter parses the Retry-After header (delta-seconds or HTTP-date).
func retryAfter(resp *http.Response) (time.Duration, bool) {
	v := resp.Header.Get("Retry-After")
	if v == "" {
		return 0, false
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs < 0 {
			return 0, false
		}
		return time.Duration(secs) * time.Second, true
	}
	if when, err := http.ParseTime(v); err == nil {
		d := time.Until(when)
		if d < 0 {
			d = 0
		}
		return d, true
	}
	return 0, false
}

func drainAndClose(resp *http.Response) {
	if resp == nil || resp.Body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	_ = resp.Body.Close()
}
