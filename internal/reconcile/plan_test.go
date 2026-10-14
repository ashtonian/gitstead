package reconcile

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/ashtonian/gitstead/internal/config"
	"github.com/ashtonian/gitstead/internal/discovery"
	"github.com/ashtonian/gitstead/internal/state"
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
	return &state.State{Repos: map[string]*state.RepoState{key: a}}
}

func untracked() *state.State {
	return &state.State{Repos: map[string]*state.RepoState{}}
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

func TestRepos_AccountGivesKeyAndIdentity(t *testing.T) {
	cfg := config.Defaults()
	cfg.Accounts = []config.Account{{Name: "work", Hosts: []string{"github.com"}, Owners: []string{"acme"},
		SSHKey: "/k/work", Git: config.GitIdentity{Name: "Me", Email: "me@acme"}}}
	desired, _ := Repos(cfg, tracked(func(a *state.RepoState) { a.ManuallyAdded = true }), nil, discovery.StaleScope{})
	d := desired[key]
	want := map[string]string{keySSHCommand: SSHCommand("/k/work"), keyUserName: "Me", keyUserEmail: "me@acme"}
	if d.Account != "work" || fmt.Sprint(d.Config) != fmt.Sprint(want) {
		t.Errorf("account %q config %v, want work %v", d.Account, d.Config, want)
	}

	// An https remote gets the identity but no sshCommand.
	desired, _ = Repos(cfg, tracked(func(a *state.RepoState) { a.ManuallyAdded, a.CloneURL = true, https }), nil, discovery.StaleScope{})
	if _, ok := desired[key].Config[keySSHCommand]; ok || desired[key].Config[keyUserEmail] != "me@acme" {
		t.Errorf("https repo config = %v", desired[key].Config)
	}
}

func TestAccountConfig_Signing(t *testing.T) {
	tests := []struct {
		name    string
		signing config.Signing
		want    map[string]string
	}{
		{"inherit writes nothing", config.Signing{Mode: "inherit", Key: "/k.pub"}, map[string]string{}},
		{"off turns signing off", config.Signing{Mode: "off"}, map[string]string{"commit.gpgsign": "false", "tag.gpgsign": "false"}},
		{"ssh is inferred from a .pub key", config.Signing{Mode: "required", Key: "/k/id.pub", Program: "/bin/op-ssh-sign", AllowedSigners: "/k/allowed"},
			map[string]string{"commit.gpgsign": "true", "tag.gpgsign": "true", "gpg.format": "ssh", "user.signingkey": "/k/id.pub",
				"gpg.ssh.program": "/bin/op-ssh-sign", "gpg.ssh.allowedsignersfile": "/k/allowed"}},
		{"a key ID means openpgp", config.Signing{Mode: "required", Key: "ABCD1234", Program: "/bin/gpg2"},
			map[string]string{"commit.gpgsign": "true", "tag.gpgsign": "true", "gpg.format": "openpgp", "user.signingkey": "ABCD1234", "gpg.program": "/bin/gpg2"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := AccountConfig(&config.Account{Signing: tt.signing})
			if fmt.Sprint(got) != fmt.Sprint(tt.want) {
				t.Errorf("got %v\nwant %v", got, tt.want)
			}
		})
	}
}
