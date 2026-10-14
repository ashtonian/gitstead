package cmd

import (
	"errors"
	"fmt"
	"os"
	"os/exec"

	"github.com/ashtonian/gitten/internal/gitops"
	"golang.org/x/sync/errgroup"
)

// Run executes the given command in each matching tracked repo's directory,
// concurrently (bounded by concurrency), and prints per-repo output. It is the
// managed-repo counterpart to `ws exec`. Output is buffered per repo and printed
// in a stable order so concurrent runs stay readable.
func (c *RunCmd) Run(ctx *Context) error {
	if len(c.Command) == 0 {
		return errors.New("no command given; usage: gitten run [--set/--tag/--repos] -- <command> [args]")
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
	conc := cfg.Concurrency
	if conc < 1 {
		conc = 1
	}
	sem := make(chan struct{}, conc)
	for i, key := range targets {
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
	doc := make([]runJSON, 0, len(results))
	for _, r := range results {
		doc = append(doc, runJSON{Repo: r.key, Path: st.Repos[r.key].LocalPath, OK: r.err == nil, ExitCode: exitStatus(r.err), Output: string(r.out), Error: errString(r.err)})
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
		_, _ = os.Stdout.Write(r.out)
		if n := len(r.out); n > 0 && r.out[n-1] != '\n' {
			fmt.Println()
		}
	}
	fmt.Printf("\nran in %d repos, %d failed\n", len(targets), failed)
	return ctx.finish(doc, outcome(failed, len(targets), "repos failed"))
}

// runJSON is what a command run in one repo did.
type runJSON struct {
	Repo     string `json:"repo"`
	Path     string `json:"path"`
	OK       bool   `json:"ok"`
	ExitCode int    `json:"exit_code"`
	Output   string `json:"output"`
	Error    string `json:"error,omitempty"`
}

// exitStatus is the exit status a command's error says, -1 when it didn't
// run or was killed.
func exitStatus(err error) int {
	var exit *exec.ExitError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &exit):
		return exit.ExitCode()
	default:
		return -1
	}
}
