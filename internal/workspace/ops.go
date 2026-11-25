package workspace

import (
	"context"
	"path/filepath"

	"golang.org/x/sync/errgroup"

	"github.com/ashtonian/gitstead/internal/gitops"
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
	runs := make([][]int, len(targets))
	for i := range targets {
		runs[i] = []int{i}
	}
	return forEachRun(ctx, targets, runs, concurrency, fn)
}

// ForEachByRepo is ForEach, except that targets sharing a git repository --
// the worktrees of one repo -- take their turn one after another. git can't
// update one repository's refs from two commands at once: a fetch fails to
// lock a ref another holds, and a repo in which another command holds a ref
// lock looks busy.
func ForEachByRepo(ctx context.Context, targets []Located, concurrency int, fn func(context.Context, Located) (string, bool, error)) []OpResult {
	return forEachRun(ctx, targets, runsByRepo(ctx, targets), concurrency, fn)
}

// runsByRepo groups targets, by index and in order, by the git repository
// they are checkouts of: its common git dir, which a repo's linked worktrees
// share and a clone has of its own.
func runsByRepo(ctx context.Context, targets []Located) [][]int {
	var runs [][]int
	runOf := map[string]int{}
	for i, t := range targets {
		repo := gitops.ResolveGitDirs(ctx, t.Path).CommonDir
		if real, err := filepath.EvalSymlinks(repo); err == nil {
			repo = real // git names a worktree's common dir by its real path
		}
		n, ok := runOf[repo]
		if !ok {
			n = len(runs)
			runOf[repo] = n
			runs = append(runs, nil)
		}
		runs[n] = append(runs[n], i)
	}
	return runs
}

// forEachRun runs fn on the targets of each run in turn, at most concurrency
// runs at a time, and returns the results in target order.
func forEachRun(ctx context.Context, targets []Located, runs [][]int, concurrency int, fn func(context.Context, Located) (string, bool, error)) []OpResult {
	results := make([]OpResult, len(targets))
	var g errgroup.Group
	g.SetLimit(max(concurrency, 1))
	for _, run := range runs {
		g.Go(func() error {
			for _, i := range run {
				out, skipped, err := fn(ctx, targets[i])
				results[i] = OpResult{Located: targets[i], Output: out, Skipped: skipped, Err: err}
			}
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
