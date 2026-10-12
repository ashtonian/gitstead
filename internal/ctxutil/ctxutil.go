// Package ctxutil holds context helpers shared across packages.
package ctxutil

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// WithTimeout is context.WithTimeout where a zero or negative d, a timeout
// configured off, means no deadline.
func WithTimeout(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if d <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, d)
}

// TimedOut rewords err when ctx's deadline caused it, naming the setting
// that bounds the operation; otherwise a killed git reads only "signal:
// killed".
func TimedOut(ctx context.Context, err error, what string, d time.Duration, setting string) error {
	if err == nil || !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return err
	}
	return fmt.Errorf("%s timed out after %s (%s): %w", what, d, setting, err)
}
