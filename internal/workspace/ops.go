package workspace

import (
	"context"

	"golang.org/x/sync/errgroup"
)

// OpResult is what an operation did to one copy.
type OpResult struct {
	Located
	Skipped bool
	Output  string
	Err     error
}

// ForEach runs fn on each target, at most concurrency at a time, and returns
// the results in target order. fn returns its output, whether it skipped the
// target, and its error.
func ForEach(ctx context.Context, targets []Located, concurrency int, fn func(context.Context, Located) (string, bool, error)) []OpResult {
	results := make([]OpResult, len(targets))
	var g errgroup.Group
	g.SetLimit(max(concurrency, 1))
	for i, t := range targets {
		g.Go(func() error {
			out, skipped, err := fn(ctx, t)
			results[i] = OpResult{Located: t, Output: out, Skipped: skipped, Err: err}
			return nil
		})
	}
	_ = g.Wait()
	return results
}

// OfAgent keeps the targets of agent; all of them when agent is "".
func OfAgent(targets []Located, agent string) []Located {
	if agent == "" {
		return targets
	}
	var out []Located
	for _, t := range targets {
		if t.Agent == agent {
			out = append(out, t)
		}
	}
	return out
}
