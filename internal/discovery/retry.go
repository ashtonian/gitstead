package discovery

import (
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"time"
)

// retryTransport is an http.RoundTripper that retries requests on transient
// failures: HTTP 429 (rate limit), 403 with a rate-limit signal (GitHub's
// secondary or primary limit), 5xx, and an attempt that timed out. A wait the
// server asks for -- Retry-After, or X-RateLimit-Reset once the limit is used
// up -- is honoured in full; when it would outlast the request's deadline
// (the rule's discovery_timeout) the request fails at once and says so.
// Otherwise it backs off exponentially. go-github and the Gitea SDK both
// accept a custom *http.Client, so this gives them the retry behavior the
// GitLab SDK already has built in.
type retryTransport struct {
	base       http.RoundTripper
	maxRetries int
	baseDelay  time.Duration
	maxDelay   time.Duration // caps our own backoff, not a wait the server asks for
	// attemptTimeout bounds each attempt, body included; 0 means none.
	attemptTimeout time.Duration
	// maxWait caps a server-requested wait when the request has no deadline.
	maxWait time.Duration
	// sleep and now are overridable in tests.
	sleep func(ctx context.Context, d time.Duration) error
	now   func() time.Time
}

// newRetryClient returns an HTTP client that retries transient failures and
// gives each attempt up to attemptTimeout (0 means no limit). Waits between
// attempts are bounded by the request's context, not by attemptTimeout.
func newRetryClient(attemptTimeout time.Duration) *http.Client {
	return &http.Client{Transport: &retryTransport{
		base:           http.DefaultTransport,
		maxRetries:     5,
		baseDelay:      time.Second,
		maxDelay:       30 * time.Second,
		attemptTimeout: attemptTimeout,
		maxWait:        time.Hour,
		sleep:          sleepCtx,
		now:            time.Now,
	}}
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

// RateLimitError reports a rate limit that lifts only after the request's
// deadline.
type RateLimitError struct {
	Host  string
	Until time.Time
}

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("%s rate limit lifts at %s, after this rule's deadline (discovery_timeout); retry later",
		e.Host, e.Until.Local().Format(time.Kitchen))
}

func (t *retryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx := req.Context()
	var lastResp *http.Response
	var lastErr error

	for attempt := 0; attempt <= t.maxRetries; attempt++ {
		if attempt > 0 {
			delay, err := t.wait(ctx, req, attempt, lastResp)
			if err != nil {
				return nil, err
			}
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

		resp, err := t.attempt(req)
		if err != nil {
			// The request's own deadline or cancellation ends it; an attempt
			// that timed out on its own is retried.
			if ctx.Err() != nil {
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

// attempt sends req once under attemptTimeout. The timeout ends when the
// response body is closed, so reading the body counts toward it.
func (t *retryTransport) attempt(req *http.Request) (*http.Response, error) {
	if t.attemptTimeout <= 0 {
		return t.base.RoundTrip(req)
	}
	ctx, cancel := context.WithTimeout(req.Context(), t.attemptTimeout)
	resp, err := t.base.RoundTrip(req.Clone(ctx))
	if err != nil {
		cancel()
		return nil, err
	}
	resp.Body = &cancelOnClose{ReadCloser: resp.Body, cancel: cancel}
	return resp, nil
}

type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c *cancelOnClose) Close() error {
	err := c.ReadCloser.Close()
	c.cancel()
	return err
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

// wait returns the delay before the given attempt (1-based): the wait the
// last response asked for, or exponential backoff. A requested wait that
// outlasts the request's deadline is a RateLimitError instead.
func (t *retryTransport) wait(ctx context.Context, req *http.Request, attempt int, resp *http.Response) (time.Duration, error) {
	if d, ok := t.requestedWait(resp); ok {
		deadline, hasDeadline := ctx.Deadline()
		if hasDeadline && d > time.Until(deadline) || !hasDeadline && d > t.maxWait {
			return 0, &RateLimitError{Host: req.URL.Host, Until: t.now().Add(d)}
		}
		return d, nil
	}
	delay := time.Duration(float64(t.baseDelay) * math.Pow(2, float64(attempt-1)))
	return min(delay, t.maxDelay), nil
}

// requestedWait is how long the server asked to wait: Retry-After, or until
// X-RateLimit-Reset when the limit is used up.
func (t *retryTransport) requestedWait(resp *http.Response) (time.Duration, bool) {
	if resp == nil {
		return 0, false
	}
	if d, ok := retryAfter(resp, t.now()); ok {
		return d, true
	}
	if resp.Header.Get("X-RateLimit-Remaining") != "0" {
		return 0, false
	}
	reset, err := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64)
	if err != nil {
		return 0, false
	}
	// A second past the reset, so the first request after it isn't early.
	return max(time.Unix(reset, 0).Sub(t.now())+time.Second, 0), true
}

// retryAfter parses the Retry-After header (delta-seconds or HTTP-date).
func retryAfter(resp *http.Response, now time.Time) (time.Duration, bool) {
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
		return max(when.Sub(now), 0), true
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
