package reconcile

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/ashtonian/git-mgr/internal/config"
	"github.com/ashtonian/git-mgr/internal/discovery"
	"github.com/ashtonian/git-mgr/internal/state"
	"github.com/ashtonian/git-mgr/internal/workgroup"
)

const (
	key    = "github.com/acme/api"
	sshURL = "git@github.com:acme/api.git"
	https  = "https://github.com/acme/api.git"
	path   = "/repos/github.com/acme/api"
	moved  = "/repos/acme/api"
	sshCmd = "ssh -i /k/work -o IdentitiesOnly=yes"
)

func want(mods ...func(*Repo)) map[string]Repo {
	r := Repo{Key: key, Host: "github.com", Owner: "acme", Name: "api", FullName: "acme/api", Path: path, URL: sshURL}
	for _, m := range mods {
		m(&r)
	}
	return map[string]Repo{r.Key: r}
}

func tracked(mods ...func(*state.RepoState)) *state.State {
	a := &state.RepoState{Host: "github.com", Owner: "acme", Name: "api", FullName: "acme/api",
		LocalPath: path, CloneURL: sshURL, CopyID: "c0ffee0000000000"}
	for _, m := range mods {
		m(a)
	}
	return &state.State{Repos: map[string]*state.RepoState{key: a}, Workgroups: map[string]*state.WorkgroupState{}}
}

func untracked() *state.State {
	return &state.State{Repos: map[string]*state.RepoState{}, Workgroups: map[string]*state.WorkgroupState{}}
}

func clone(at, origin string, cfg ...string) Copy {
	c := Copy{Path: at, Exists: true, IsRepo: true, Origin: origin, CopyID: "c0ffee0000000000",
		Config: map[string]string{"remote.origin.url": origin}}
	for i := 0; i+1 < len(cfg); i += 2 {
		c.Config[cfg[i]] = cfg[i+1]
	}
	return c
}

func seenAt(copies ...Copy) Seen {
	s := Seen{}
	for _, c := range copies {
		s[c.Path] = c
	}
	return s
}

// lines renders a plan as one line per action, drift and blocked entry.
func lines(p *Plan) []string {
	var out []string
	for _, a := range p.Actions {
		l := fmt.Sprintf("%s %s", a.Kind, a.Target)
		if a.Field != "" {
			l += " " + a.Field
		}
		if a.From != "" || a.To != "" {
			l += fmt.Sprintf(" %s -> %s", a.From, a.To)
		}
		if a.Disruptive {
			l += " [disruptive]"
		}
		if a.Note != "" {
			l += " (" + a.Note + ")"
		}
		out = append(out, l)
	}
	for _, d := range p.Drift {
		out = append(out, fmt.Sprintf("drift %s %s: recorded %s, found %s", d.Target, d.What, d.Recorded, d.Found))
	}
	for _, b := range p.Blocked {
		out = append(out, "blocked "+b.Target+": "+b.Reason)
	}
	return out
}

func TestPlanRepos(t *testing.T) {
	tests := []struct {
		name      string
		desired   map[string]Repo
		st        *state.State
		seen      Seen
		overwrite bool
		want      []string
	}{
		{
			name:    "a new repo is cloned",
			desired: want(), st: untracked(), seen: seenAt(),
			want: []string{"clone " + key + " " + sshURL + " -> " + path},
		},
		{
			name:    "a clone left by an interrupted run is adopted",
			desired: want(), st: untracked(), seen: seenAt(clone(path, https)),
			want: []string{"adopt " + key + "  -> " + path + " (already cloned there)"},
		},
		{
			name:    "a path holding another repo blocks the clone",
			desired: want(), st: untracked(), seen: seenAt(clone(path, "git@github.com:other/thing.git")),
			want: []string{"blocked " + key + ": " + path + " already holds a clone of git@github.com:other/thing.git"},
		},
		{
			name:    "an unchanged repo needs nothing",
			desired: want(), st: tracked(), seen: seenAt(clone(path, sshURL)),
		},
		{
			name:    "a repo gone from disk is cloned again",
			desired: want(), st: tracked(), seen: seenAt(),
			want: []string{"clone " + key + " " + sshURL + " -> " + path + " (gone from disk; cloning it again)"},
		},
		{
			name:    "a new path_template moves the repo, disruptively",
			desired: want(func(r *Repo) { r.Path = moved }), st: tracked(), seen: seenAt(clone(path, sshURL)),
			want: []string{"move " + key + " " + path + " -> " + moved + " [disruptive]"},
		},
		{
			name:    "a move interrupted after the rename is recorded",
			desired: want(func(r *Repo) { r.Path = moved }), st: tracked(), seen: seenAt(clone(moved, sshURL)),
			want: []string{"move " + key + " " + path + " -> " + moved + " (already moved; recording it)"},
		},
		{
			name:    "an occupied destination blocks the move",
			desired: want(func(r *Repo) { r.Path = moved }), st: tracked(),
			seen: seenAt(clone(path, sshURL), Copy{Path: moved, Exists: true}),
			want: []string{"blocked " + key + ": cannot move from " + path + ": " + moved + " is already taken by something that isn't a git repository"},
		},
		{
			name:    "a hand-added repo never moves",
			desired: want(func(r *Repo) { r.Pinned = true }), st: tracked(func(a *state.RepoState) { a.ManuallyAdded = true }),
			seen: seenAt(clone(path, sshURL)),
		},
		{
			name:    "a new clone_protocol changes the origin URL",
			desired: want(func(r *Repo) { r.URL = https }), st: tracked(), seen: seenAt(clone(path, sshURL)),
			want: []string{"url " + key + " " + sshURL + " -> " + https},
		},
		{
			name:    "an origin changed by hand is drift",
			desired: want(), st: tracked(), seen: seenAt(clone(path, "git@work:acme/api.git")),
			want: []string{"drift " + key + " origin URL: recorded " + sshURL + ", found git@work:acme/api.git"},
		},
		{
			name:    "drift: overwrite restores the origin",
			desired: want(), st: tracked(), seen: seenAt(clone(path, "git@work:acme/api.git")), overwrite: true,
			want: []string{"url " + key + " git@work:acme/api.git -> " + sshURL + " (changed by hand; restoring)"},
		},
		{
			name:    "a configured ssh_key writes core.sshCommand",
			desired: want(withSSHCmd), st: tracked(), seen: seenAt(clone(path, sshURL)),
			want: []string{"config " + key + " core.sshcommand (unset) -> " + sshCmd},
		},
		{
			name:    "a value already set by hand is taken over",
			desired: want(withSSHCmd), st: tracked(), seen: seenAt(clone(path, sshURL, keySSHCommand, sshCmd)),
			want: []string{"config " + key + " core.sshcommand " + sshCmd + " -> " + sshCmd + " (already set; now managed)"},
		},
		{
			name:    "a different value set by hand is drift",
			desired: want(withSSHCmd), st: tracked(), seen: seenAt(clone(path, sshURL, keySSHCommand, "ssh -i ~/.ssh/mine")),
			want: []string{"drift " + key + " core.sshcommand: recorded (unset), found ssh -i ~/.ssh/mine"},
		},
		{
			name:    "a managed value changed by hand is drift",
			desired: want(withSSHCmd), st: tracked(managing(sshCmd)),
			seen: seenAt(clone(path, sshURL, keySSHCommand, "ssh -i ~/.ssh/mine")),
			want: []string{"drift " + key + " core.sshcommand: recorded " + sshCmd + ", found ssh -i ~/.ssh/mine"},
		},
		{
			name:    "a key no longer configured is unset",
			desired: want(), st: tracked(managing(sshCmd)), seen: seenAt(clone(path, sshURL, keySSHCommand, sshCmd)),
			want: []string{"config " + key + " core.sshcommand " + sshCmd + " ->  (no longer configured)"},
		},
		{
			name:    "a key no longer configured but changed by hand is left alone",
			desired: want(), st: tracked(managing(sshCmd)), seen: seenAt(clone(path, sshURL, keySSHCommand, "mine")),
			want: []string{"config " + key + " core.sshcommand mine ->  (changed by hand; no longer managed)"},
		},
		{
			name:    "new rule tags retag the repo",
			desired: want(func(r *Repo) { r.Tags, r.Rule = []string{"work"}, "acme" }),
			st:      tracked(func(a *state.RepoState) { a.Tags, a.DiscoveredBy = []string{"personal"}, "mine" }),
			seen:    seenAt(clone(path, sshURL)),
			want:    []string{"tags " + key + " [personal] rule mine -> [work] rule acme"},
		},
		{
			name: "a renamed repo is rekeyed, re-pointed and moved",
			desired: map[string]Repo{"github.com/acme/api-v2": {Key: "github.com/acme/api-v2", Host: "github.com",
				ProviderID: "42", Path: "/repos/github.com/acme/api-v2", URL: "git@github.com:acme/api-v2.git"}},
			st:   tracked(func(a *state.RepoState) { a.ProviderID = "42" }),
			seen: seenAt(clone(path, sshURL)),
			want: []string{
				"rename github.com/acme/api-v2 " + key + " -> github.com/acme/api-v2 (same repository (provider ID 42))",
				"url github.com/acme/api-v2 " + sshURL + " -> git@github.com:acme/api-v2.git",
				"move github.com/acme/api-v2 " + path + " -> /repos/github.com/acme/api-v2 [disruptive]",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := lines(PlanRepos(tt.desired, tt.st, tt.seen, tt.overwrite))
			if !slices.Equal(got, tt.want) {
				t.Errorf("plan:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(tt.want, "\n  "))
			}
		})
	}
}

func withSSHCmd(r *Repo) { r.SSHKey, r.Config = "/k/work", map[string]string{keySSHCommand: sshCmd} }

func managing(v string) func(*state.RepoState) {
	return func(a *state.RepoState) { a.ManagedConfig = map[string]string{keySSHCommand: v} }
}

func TestPlanWorkgroups(t *testing.T) {
	const src = "/repos/app"
	member := func(name string) workgroup.Member {
		return workgroup.Member{Name: name, Path: "/wg/fleet/" + name, Branch: name}
	}
	declared := func(members ...workgroup.Member) map[string]Workgroup {
		spec := &workgroup.Spec{Name: "fleet", Kind: "worktree", Root: "/wg/fleet", Source: src, Members: members}
		return map[string]Workgroup{"fleet": {Spec: spec, Declared: true}}
	}
	recorded := func(declared bool, members ...workgroup.Member) *state.State {
		ws := &state.WorkgroupState{Name: "fleet", Kind: "worktree", Root: "/wg/fleet", Source: src, Declared: declared}
		for _, m := range members {
			ws.Members = append(ws.Members, state.WorkgroupMemberState{Name: m.Name, Path: m.Path, Branch: m.Branch})
		}
		return &state.State{Repos: map[string]*state.RepoState{}, Workgroups: map[string]*state.WorkgroupState{"fleet": ws}}
	}
	worktree := func(at string) Copy { return Copy{Path: at, Exists: true, IsRepo: true, CommonDir: src + "/.git"} }
	source := worktree(src)

	tests := []struct {
		name    string
		desired map[string]Workgroup
		st      *state.State
		seen    Seen
		want    []string
	}{
		{
			name:    "a declared workgroup is created",
			desired: declared(member("a1"), member("a2")), st: untracked(), seen: seenAt(source),
			want: []string{"create-workgroup fleet  -> /wg/fleet (worktree, 2 member(s))"},
		},
		{
			name:    "a declared member is added",
			desired: declared(member("a1"), member("a2")), st: recorded(true, member("a1")),
			seen: seenAt(source, worktree("/wg/fleet/a1")),
			want: []string{"create-member fleet/a2  -> /wg/fleet/a2"},
		},
		{
			name:    "a member gone from disk is created again",
			desired: declared(member("a1")), st: recorded(true, member("a1")), seen: seenAt(source),
			want: []string{"create-member fleet/a1  -> /wg/fleet/a1 (gone from disk; creating it again)"},
		},
		{
			name:    "a member no longer declared is archived and removed",
			desired: declared(member("a1")), st: recorded(true, member("a1"), member("a2")),
			seen: seenAt(source, worktree("/wg/fleet/a1"), worktree("/wg/fleet/a2")),
			want: []string{"remove-member fleet/a2 /wg/fleet/a2 ->  [disruptive] (no longer declared; unique work archived first)"},
		},
		{
			name:    "a member no longer declared and gone is forgotten",
			desired: declared(member("a1")), st: recorded(true, member("a1"), member("a2")),
			seen: seenAt(source, worktree("/wg/fleet/a1")),
			want: []string{"forget-member fleet/a2 /wg/fleet/a2 ->  (no longer declared and gone from disk)"},
		},
		{
			name:    "a workgroup no longer declared is removed",
			desired: map[string]Workgroup{}, st: recorded(true, member("a1")), seen: seenAt(source, worktree("/wg/fleet/a1")),
			want: []string{"remove-workgroup fleet /wg/fleet ->  [disruptive] (no longer declared; 1 member(s) archived and removed)"},
		},
		{
			name:    "a workgroup created from flags is kept",
			desired: map[string]Workgroup{}, st: recorded(false, member("a1")), seen: seenAt(source, worktree("/wg/fleet/a1")),
		},
		{
			name:    "a member created before an interruption is recorded",
			desired: declared(member("a1"), member("a2")), st: recorded(true, member("a1")),
			seen: seenAt(source, worktree("/wg/fleet/a1"), worktree("/wg/fleet/a2")),
			want: []string{"create-member fleet/a2  -> /wg/fleet/a2 (already there; recording it)"},
		},
		{
			name: "a new root moves members, disruptively",
			desired: func() map[string]Workgroup {
				d := declared(workgroup.Member{Name: "a1", Path: "/agents/fleet/a1", Branch: "a1"})
				d["fleet"].Spec.Root = "/agents/fleet"
				return d
			}(),
			st: recorded(true, member("a1")), seen: seenAt(source, worktree("/wg/fleet/a1")),
			want: []string{
				"workgroup fleet /wg/fleet -> /agents/fleet",
				"move-member fleet/a1 /wg/fleet/a1 -> /agents/fleet/a1 [disruptive]",
			},
		},
		{
			name: "a kind change is blocked",
			desired: func() map[string]Workgroup {
				d := declared(member("a1"))
				d["fleet"].Spec.Kind = "clone"
				return d
			}(),
			st: recorded(true, member("a1")), seen: seenAt(source, worktree("/wg/fleet/a1")),
			want: []string{"blocked fleet: kind changed from worktree to clone; remove it (git-mgr wg rm fleet --delete) to recreate it"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := lines(PlanWorkgroups(tt.desired, tt.st, tt.seen, nil))
			if !slices.Equal(got, tt.want) {
				t.Errorf("plan:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(tt.want, "\n  "))
			}
		})
	}
}

// A hand-added repo loses a rule's tags only when that rule ran, succeeded
// and didn't find it.
func TestRepos_HandAddedKeepsRuleTagsUnlessItsRuleMissedIt(t *testing.T) {
	st := tracked(func(a *state.RepoState) {
		a.ManuallyAdded, a.DiscoveredBy = true, "work"
		a.Tags, a.UserTags = []string{"mine", "work"}, []string{"mine"}
	})
	cfg := config.Defaults()
	for _, tt := range []struct {
		name     string
		results  []discovery.DiscoverResult
		wantTags string
		wantRule string
	}{
		{"no discovery ran", nil, "mine work", "work"},
		{"its rule failed", []discovery.DiscoverResult{{Rule: "work", Err: errors.New("401")}}, "mine work", "work"},
		{"its rule ran and missed it", []discovery.DiscoverResult{{Rule: "work"}}, "mine", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			scope := discovery.StaleScope{}
			if tt.results != nil {
				scope = discovery.StaleScopeOf(tt.results, "", "")
			}
			desired, _ := Repos(cfg, st, map[string]discovery.Repo{}, scope)
			d := desired[key]
			if got := strings.Join(d.Tags, " "); got != tt.wantTags || d.Rule != tt.wantRule {
				t.Errorf("tags %q rule %q, want %q rule %q", got, d.Rule, tt.wantTags, tt.wantRule)
			}
			if !d.Pinned || d.Path != path || d.URL != sshURL {
				t.Errorf("a hand-added repo keeps its path and URL: %+v", d)
			}
		})
	}
}

func TestPlan_JSONHasEmptyLists(t *testing.T) {
	data, err := json.Marshal(&Plan{})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); got != `{"actions":[],"drift":[],"blocked":[]}` {
		t.Errorf("empty plan JSON = %s", got)
	}
}
