package cmd

import (
	"os"

	"github.com/ashtonian/git-mgr/internal/config"
	"github.com/ashtonian/git-mgr/internal/state"
	"github.com/posener/complete"
	"github.com/willabides/kongplete"
)

// CompletionPredictors returns kongplete options that complete dynamic values --
// repo keys, tags, named sets, and workgroup names -- for any arg/flag tagged
// with the matching `predictor:"..."`. They run at completion time, resolving the
// state/config path from the env or default, best-effort (no candidates on error).
func CompletionPredictors() []kongplete.Option {
	return []kongplete.Option{
		kongplete.WithPredictor("repo", complete.PredictFunc(func(complete.Args) []string { return completionRepoKeys() })),
		kongplete.WithPredictor("tag", complete.PredictFunc(func(complete.Args) []string { return completionTags() })),
		kongplete.WithPredictor("set", complete.PredictFunc(func(complete.Args) []string { return completionSetNames() })),
		kongplete.WithPredictor("workgroup", complete.PredictFunc(func(complete.Args) []string { return completionWorkgroups() })),
	}
}

func completionState() *state.State {
	path := os.Getenv("GIT_MGR_STATE")
	if path == "" {
		path = config.ExpandHome("~/.local/share/git-mgr/state.yaml")
	}
	st, err := state.Load(path)
	if err != nil {
		return nil
	}
	return st
}

func completionConfig() *config.Config {
	path := os.Getenv("GIT_MGR_CONFIG")
	if path == "" {
		path = config.ExpandHome("~/.config/git-mgr/config.yaml")
	}
	cfg, err := config.Load(path)
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

func completionWorkgroups() []string {
	seen := map[string]bool{}
	var out []string
	if st := completionState(); st != nil {
		for n := range st.Workgroups {
			if !seen[n] {
				seen[n] = true
				out = append(out, n)
			}
		}
	}
	if cfg := completionConfig(); cfg != nil {
		for _, wg := range cfg.Workgroups {
			if !seen[wg.Name] {
				seen[wg.Name] = true
				out = append(out, wg.Name)
			}
		}
	}
	return out
}
