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
	wgState := st
	if scope.Workgroups {
		if scope.Repos {
			wgState = withNewRepos(st, desired)
		}
		var b []Blocked
		wgs, b = Workgroups(cfg, wgState)
		for _, x := range b {
			if scope.Only == nil || scope.Only(x.Target) {
				blocked = append(blocked, x)
			}
		}
		paths = append(paths, WorkgroupPaths(wgs, wgState)...)
	}

	seen := Observe(ctx, paths, cfg.Concurrency)
	p := &Plan{}
	if scope.Repos {
		p.Merge(PlanRepos(desired, st, seen, cfg.Reconcile.Drift == "overwrite"))
	}
	if scope.Workgroups {
		p.Merge(PlanWorkgroups(wgs, wgState, seen, scope.Only, cfg.Reconcile.Drift == "overwrite"))
	}
	p.Blocked = append(p.Blocked, blocked...)
	return p
}

// withNewRepos returns st as workgroups will see it once the plan's clones are
// done: each desired repo not yet tracked is added at the path and URL it will
// get, so a workgroup built on a repo the same plan clones can be planned.
// Tracked repos keep their recorded path; moves update workgroups as they run.
func withNewRepos(st *state.State, desired map[string]Repo) *state.State {
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

// ApplyPlan applies p, a plan Compute made for scope: its repo actions first,
// then, because workgroups are built from repos, a workgroup plan made afresh
// against the updated state. A disruptive workgroup action of the fresh plan
// runs only if p had it too, so nothing disruptive runs that wasn't shown.
func ApplyPlan(ctx context.Context, cfg *config.Config, env *Env, p *Plan, scope Scope, include func(Action) bool) []Result {
	repoPart := &Plan{record: p.record}
	shown := map[string]bool{}
	for _, a := range p.Actions {
		if a.Workgroup() {
			if a.Disruptive {
				shown[a.identity()] = true
			}
			continue
		}
		repoPart.Actions = append(repoPart.Actions, a)
	}
	results := repoPart.Apply(ctx, env, include)
	if !scope.Workgroups {
		return results
	}
	fresh := Compute(ctx, cfg, env.State, Scope{Workgroups: true, Only: scope.Only})
	return append(results, fresh.Apply(ctx, env, func(a Action) bool {
		return include(a) && (!a.Disruptive || shown[a.identity()])
	})...)
}

func (a Action) identity() string {
	return strings.Join([]string{string(a.Kind), a.Target, a.Field, a.From, a.To}, "\x00")
}
