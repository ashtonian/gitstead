package cmd

import (
	"context"
	"os"

	"github.com/ashtonian/gitstead/internal/config"
	"github.com/ashtonian/gitstead/internal/jump"
	"github.com/ashtonian/gitstead/internal/state"
	"github.com/posener/complete"
	"github.com/willabides/kongplete"
)

// CompletionPredictors returns kongplete options that complete dynamic values --
// repo keys, tags, named sets, workspace and fleet names, jump targets and archive
// IDs -- for any arg/flag tagged
// with the matching `predictor:"..."`. They run at completion time, resolving the
// state/config path from the env or default, best-effort (no candidates on error).
func CompletionPredictors() []kongplete.Option {
	return []kongplete.Option{
		kongplete.WithPredictor("repo", complete.PredictFunc(func(complete.Args) []string { return completionRepoKeys() })),
		kongplete.WithPredictor("tag", complete.PredictFunc(func(complete.Args) []string { return completionTags() })),
		kongplete.WithPredictor("set", complete.PredictFunc(func(complete.Args) []string { return completionSetNames() })),
		kongplete.WithPredictor("workspace", complete.PredictFunc(func(complete.Args) []string { return completionWorkspaces() })),
		kongplete.WithPredictor("fleet", complete.PredictFunc(func(complete.Args) []string { return completionFleets() })),
		kongplete.WithPredictor("target", complete.PredictFunc(func(complete.Args) []string { return completionTargets() })),
		kongplete.WithPredictor("archive", complete.PredictFunc(func(complete.Args) []string { return completionArchives() })),
	}
}

func completionState() *state.State {
	path := os.Getenv("GITSTEAD_STATE")
	if path == "" {
		path = config.DefaultStatePath()
	}
	st, err := state.Load(path)
	if err != nil {
		return nil
	}
	return st
}

func completionConfig() *config.Config {
	path := os.Getenv("GITSTEAD_CONFIG")
	var opts []config.Option
	if path == "" {
		path, opts = config.DefaultConfigPath(), append(opts, config.WithOptionalFile())
	}
	// Completion runs outside any command, so nothing above it can cancel.
	cfg, err := config.Load(context.Background(), path, opts...)
	if err != nil {
		return nil
	}
	return cfg
}

func completionRepoKeys() []string {
	st := completionState()
	if st == nil {
		return nil
	}
	out := make([]string, 0, len(st.Repos))
	for k := range st.Repos {
		out = append(out, k)
	}
	return out
}

func completionTags() []string {
	st := completionState()
	if st == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, r := range st.Repos {
		for _, t := range r.Tags {
			if !seen[t] {
				seen[t] = true
				out = append(out, t)
			}
		}
	}
	return out
}

func completionSetNames() []string {
	cfg := completionConfig()
	if cfg == nil {
		return nil
	}
	out := make([]string, 0, len(cfg.Sets))
	for _, s := range cfg.Sets {
		out = append(out, s.Name)
	}
	return out
}

func completionWorkspaces() []string {
	seen := map[string]bool{}
	var out []string
	add := func(n string) {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	if st := completionState(); st != nil {
		for n := range st.Workspaces {
			add(n)
		}
	}
	if cfg := completionConfig(); cfg != nil {
		for _, w := range cfg.Workspaces {
			add(w.Name)
		}
	}
	return out
}

func completionFleets() []string {
	cfg := completionConfig()
	if cfg == nil {
		return nil
	}
	out := make([]string, 0, len(cfg.Fleets))
	for _, f := range cfg.Fleets {
		out = append(out, f.Name)
	}
	return out
}

func completionArchives() []string {
	st := completionState()
	if st == nil {
		return nil
	}
	out := make([]string, 0, len(st.Archives))
	for _, a := range st.Archives {
		out = append(out, a.ID)
	}
	return out
}

func completionTargets() []string {
	st := completionState()
	if st == nil {
		return nil
	}
	var out []string
	for _, t := range jump.Targets(st) {
		out = append(out, t.Label)
	}
	return out
}
