package cmd

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/ashtonian/git-mgr/internal/gitops"
	"golang.org/x/sync/errgroup"
)

// Run executes the given command in each matching tracked repo's directory,
// concurrently (bounded by concurrency), and prints per-repo output. It is the
// managed-repo counterpart to `wg exec`. Output is buffered per repo and printed
// in a stable order so concurrent runs stay readable.
func (c *RunCmd) Run(ctx *Context) error {
	if len(c.Command) == 0 {
		return fmt.Errorf("no command given; usage: git-mgr run [--set/--tag/--repos] -- <cmd>...")
	}
	cfg, err := ctx.LoadConfig()
	if err != nil {
		return err
	}
	st, err := ctx.LoadState()
	if err != nil {
		return err
	}
	keys, err := selectRepos(cfg, st, selection{c.Repos, c.Tag, c.Set})
	if err != nil {
		return err
	}

	var targets []string
	for _, k := range keys {
		if r := st.Repos[k]; r.Cloned && gitops.HasGitDir(r.LocalPath) {
			targets = append(targets, k)
		}
	}
	if len(targets) == 0 {
		return fmt.Errorf("no matching cloned repos")
	}

	type result struct {
		key string
		out []byte
		err error
	}
	results := make([]result, len(targets))
	g, gctx := errgroup.WithContext(ctx.GoCtx())
	sem := make(chan struct{}, cfg.Concurrency)
	for i, key := range targets {
		i, key := i, key
		g.Go(func() error {
			sem <- struct{}{}
			defer func() { <-sem }()
			cmd := exec.CommandContext(gctx, c.Command[0], c.Command[1:]...)
			cmd.Dir = st.Repos[key].LocalPath
			out, runErr := cmd.CombinedOutput()
			results[i] = result{key, out, runErr}
			if runErr != nil && c.FailFast {
				return runErr // cancels gctx, stopping the rest
			}
			return nil
		})
	}
	_ = g.Wait()

	failed := 0
	for _, r := range results {
		if r.err != nil {
			failed++
		}
		if ctx.Quiet && r.err == nil {
			continue
		}
		status := "ok"
		if r.err != nil {
			status = "FAIL: " + r.err.Error()
		}
		fmt.Printf("== %s (%s) ==\n", r.key, status)
		os.Stdout.Write(r.out)
		if n := len(r.out); n > 0 && r.out[n-1] != '\n' {
			fmt.Println()
		}
	}
	fmt.Printf("\nran in %d repos, %d failed\n", len(targets), failed)
	if failed > 0 {
		return fmt.Errorf("%d repos failed", failed)
	}
	return nil
}
