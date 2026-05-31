package workgroup

import (
	"context"
	"sync"
)

// OpResult is the outcome of a group operation against a single member.
type OpResult struct {
	Member
	Skipped bool
	Output  string
	Err     error
}

// ForEach runs fn against every member concurrently, bounded by concurrency,
// and returns results in member order. fn returns (output, skipped, error).
func ForEach(ctx context.Context, spec *Spec, concurrency int, fn func(context.Context, Member) (string, bool, error)) []OpResult {
	if concurrency < 1 {
		concurrency = 1
	}
	results := make([]OpResult, len(spec.Members))
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for i, m := range spec.Members {
		i, m := i, m
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			out, skipped, err := fn(ctx, m)
			results[i] = OpResult{Member: m, Output: out, Skipped: skipped, Err: err}
		}()
	}
	wg.Wait()
	return results
}
