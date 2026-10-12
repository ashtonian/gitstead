package reconcile

import (
	"context"
	"strings"

	"github.com/ashtonian/git-mgr/internal/config"
	"github.com/ashtonian/git-mgr/internal/discovery"
	"github.com/ashtonian/git-mgr/internal/state"
)

// Scope selects what a plan covers.
type Scope struct {
	// Discovered is this run's discovery result, and Rules which rules ran;
	// both empty when discovery didn't run, which leaves discovered repos out.
	Discovered map[string]discovery.Repo
	Rules      discovery.StaleScope
}

// Compute plans every change config asks for of tracked repos within scope.
func Compute(ctx context.Context, cfg *config.Config, st *state.State, scope Scope) *Plan {
	desired, blocked := Repos(cfg, st, scope.Discovered, scope.Rules)
	seen := Observe(ctx, RepoPaths(desired, st), cfg.Concurrency)
	p := PlanRepos(desired, st, seen, cfg.Reconcile.Drift == "overwrite")
	p.Blocked = append(p.Blocked, blocked...)
	return p
}

// Projected returns st as it will be once the clones and adoptions of
// desired are done: each desired repo not yet tracked is added at the path
// and URL it will get, so a workspace built on a repo the same plan clones
// can be planned. Tracked repos keep their recorded path.
func Projected(st *state.State, desired map[string]Repo) *state.State {
	projected := *st
	projected.Repos = make(map[string]*state.RepoState, len(st.Repos)+len(desired))
	for k, r := range st.Repos {
		projected.Repos[k] = r
	}
	for k, d := range desired {
		if _, tracked := projected.Repos[k]; !tracked {
			projected.Repos[k] = &state.RepoState{
				Host: d.Host, Owner: d.Owner, Name: d.Name, FullName: d.FullName,
				LocalPath: d.Path, CloneURL: d.URL, DiscoveredBy: d.Rule, Tags: d.Tags,
				ManuallyAdded: d.Pinned, UserTags: d.UserTags,
			}
		}
	}
	return &projected
}

// AutoFilter returns the actions reconcile.auto lets sync apply on its own.
func AutoFilter(auto string) func(Action) bool {
	switch auto {
	case "all":
		return All
	case "none":
		return None
	default:
		return Safe
	}
}

// Identity distinguishes actions that make the same change.
func (a Action) Identity() string {
	return strings.Join([]string{string(a.Kind), a.Target, a.Field, a.From, a.To}, "\x00")
}
