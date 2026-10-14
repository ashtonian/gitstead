package cmd

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"

	"github.com/ashtonian/gitstead/internal/hook"
	"github.com/ashtonian/gitstead/internal/reconcile"
	"github.com/ashtonian/gitstead/internal/workspace"
)

// HooksCmd runs hooks again: after fixing one that failed, or changing one.
type HooksCmd struct {
	PostClone  HooksPostCloneCmd  `cmd:"" name:"post-clone" help:"Run repos' post_clone hooks again, in their clones."`
	PostCreate HooksPostCreateCmd `cmd:"" name:"post-create" help:"Run a workspace's post_create hooks again, in its copies."`
}

// HooksPostCloneCmd runs repos' post_clone hooks.
type HooksPostCloneCmd struct {
	Pattern string `arg:"" optional:"" help:"Repo key glob (or use --tag/--set)." predictor:"repo"`
	Tag     string `help:"Repos with this tag." predictor:"tag"`
	Set     string `help:"Repos in this named set (from config 'sets:')." predictor:"set"`
}

func (c *HooksPostCloneCmd) Run(ctx *Context) error {
	cfg, err := ctx.LoadConfig()
	if err != nil {
		return err
	}
	st, err := ctx.LoadState()
	if err != nil {
		return err
	}
	sel := selection{repos: c.Pattern, tag: c.Tag, set: c.Set}
	if sel.empty() {
		return errors.New("specify a repo pattern, --tag, or --set")
	}
	keys, err := selectRepos(cfg, st, sel)
	if err != nil {
		return err
	}
	runner := hookRunner(cfg, st, os.Stdout)
	ran, failed := 0, 0
	doc := []hookRunJSON{}
	for _, key := range keys {
		repo := st.Repos[key]
		commands := cfg.HooksFor(key, repo.DiscoveredBy).PostClone
		if len(commands) == 0 {
			continue
		}
		if !onDisk(repo.LocalPath) {
			fmt.Fprintf(os.Stderr, "%s: not cloned at %s; skipped\n", key, tildify(repo.LocalPath))
			continue
		}
		ran++
		fmt.Printf("== %s\n", key)
		err := runner.Run(ctx.GoCtx(), reconcile.PostCloneHook(key, repo.LocalPath, commands))
		doc = append(doc, hookResult(key, repo.LocalPath, err))
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", key, err)
			failed++
		}
	}
	if ran == 0 {
		fmt.Println("No post_clone hooks to run for these repos.")
	}
	return ctx.finish(doc, outcome(failed, ran, "post_clone hooks failed in %d of %d repos", failed, ran))
}

// hookRunJSON is what running one checkout's hooks did.
type hookRunJSON struct {
	Target string `json:"target"`
	Path   string `json:"path"`
	OK     bool   `json:"ok"`
	// Command is the one that failed, Log where its run's output is.
	Command string `json:"command,omitempty"`
	Log     string `json:"log,omitempty"`
	Error   string `json:"error,omitempty"`
}

func hookResult(target, path string, err error) hookRunJSON {
	r := hookRunJSON{Target: target, Path: path, OK: err == nil, Error: errString(err)}
	var hookErr *hook.Error
	if errors.As(err, &hookErr) {
		r.Command, r.Log = hookErr.Command, hookErr.Log
	}
	return r
}

// HooksPostCreateCmd runs a workspace's post_create hooks.
type HooksPostCreateCmd struct {
	Workspace string   `arg:"" help:"Workspace name." predictor:"workspace"`
	Agents    []string `arg:"" optional:"" name:"agent" help:"Only these agents' copies."`
}

func (c *HooksPostCreateCmd) Run(ctx *Context) error {
	cfg, err := ctx.LoadConfig()
	if err != nil {
		return err
	}
	st, err := ctx.LoadState()
	if err != nil {
		return err
	}
	ws := st.Workspaces[c.Workspace]
	if ws == nil {
		return fmt.Errorf("no workspace %q; `gitstead ws ls` lists them", c.Workspace)
	}
	spec := workspace.FromState(cfg, st, ws)
	runner := hookRunner(cfg, st, os.Stdout)
	ran, failed := 0, 0
	doc := []hookRunJSON{}
	for _, l := range spec.Copies() {
		if len(c.Agents) > 0 && !slices.Contains(c.Agents, l.Agent) {
			continue
		}
		if len(l.PostCreate) == 0 {
			continue
		}
		label := l.Label(spec)
		if !onDisk(l.Path) {
			fmt.Fprintf(os.Stderr, "%s: no copy at %s; skipped\n", label, tildify(l.Path))
			continue
		}
		ran++
		fmt.Printf("== %s\n", label)
		vars, err := agentEnv(cfg, st, spec.Name, l.Agent)
		if err == nil {
			err = runner.Run(ctx.GoCtx(), workspace.PostCreateHook(spec.Name, l.Agent, l.Copy, vars))
		}
		doc = append(doc, hookResult(label, l.Path, err))
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", label, err)
			failed++
		}
	}
	if ran == 0 {
		fmt.Println("No post_create hooks to run in these copies.")
	}
	return ctx.finish(doc, outcome(failed, ran, "post_create hooks failed in %d of %d copies", failed, ran))
}

func onDisk(path string) bool {
	_, err := os.Stat(path)
	return path != "" && !errors.Is(err, fs.ErrNotExist)
}
