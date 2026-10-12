package reconcile

import (
	"context"

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
	// Repos and Workgroups switch each half of the plan on.
	Repos, Workgroups bool
	// Only, when not nil, limits workgroups to the names it accepts.
	Only func(name string) bool
}

// Compute plans every change config asks for within scope.
func Compute(ctx context.Context, cfg *config.Config, st *state.State, scope Scope) *Plan {
	var desired map[string]Repo
	var wgs map[string]Workgroup
	var blocked []Blocked
	var paths []string
	if scope.Repos {
		var b []Blocked
		desired, b = Repos(cfg, st, scope.Discovered, scope.Rules)
		blocked = append(blocked, b...)
		paths = append(paths, RepoPaths(desired, st)...)
	}
	if scope.Workgroups {
		var b []Blocked
		wgs, b = Workgroups(cfg, st)
		for _, x := range b {
			if scope.Only == nil || scope.Only(x.Target) {
				blocked = append(blocked, x)
			}
		}
		paths = append(paths, WorkgroupPaths(wgs, st)...)
	}

	seen := Observe(ctx, paths, cfg.Concurrency)
	p := &Plan{}
	if scope.Repos {
		p.Merge(PlanRepos(desired, st, seen, cfg.Reconcile.Drift == "overwrite"))
	}
	if scope.Workgroups {
		p.Merge(PlanWorkgroups(wgs, st, seen, scope.Only))
	}
	p.Blocked = append(p.Blocked, blocked...)
	return p
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
