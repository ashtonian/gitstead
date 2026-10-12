package config

import (
	"strings"
	"testing"
)

func TestValidateWorkspaces(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
		err  string
	}{
		{"one repo", Config{Workspaces: []Workspace{{Name: "app", Repos: []string{"github.com/me/app"}}}}, ""},
		{"a path is shared", Config{Workspaces: []Workspace{{Name: "notes", Path: "~/notes", Agents: 2}}}, ""},
		{"no repos", Config{Workspaces: []Workspace{{Name: "app"}}}, "set one of repos, set and path"},
		{"repos and path", Config{Workspaces: []Workspace{{Name: "app", Repos: []string{"x"}, Path: "/p"}}}, "set one of repos, set and path"},
		{"unknown set", Config{Workspaces: []Workspace{{Name: "app", Set: "nope"}}}, `unknown set "nope"`},
		{"a path with clones", Config{Workspaces: []Workspace{{Name: "app", Path: "/p", Isolation: "clone"}}}, "a workspace over a path is shared"},
		{"shared over two repos", Config{Workspaces: []Workspace{{Name: "app", Repos: []string{"a", "b"}, Isolation: "shared"}}}, "exactly one repo"},
		{"a dot in the name", Config{Workspaces: []Workspace{{Name: "foo.bar", Repos: []string{"x"}}}}, "a name is letters"},
		{"a slash in the name", Config{Workspaces: []Workspace{{Name: "foo/bar", Repos: []string{"x"}}}}, "a name is letters"},
		{"duplicate", Config{Workspaces: []Workspace{{Name: "a", Repos: []string{"x"}}, {Name: "a", Repos: []string{"y"}}}}, "duplicate workspace"},
		{"agent names collide", Config{Workspaces: []Workspace{{Name: "a", Repos: []string{"x"}, Agents: 2, AgentName: "bot"}}}, `gives "bot" twice`},
		{"agent name escapes", Config{Workspaces: []Workspace{{Name: "a", Repos: []string{"x"}, AgentName: "../{{.N}}"}}}, "a name is letters"},
		{"bad branch", Config{Workspaces: []Workspace{{Name: "a", Repos: []string{"x"}, Branch: "agents/{{.Agent}}.lock"}}}, "git doesn't allow"},
		{"bad template field", Config{Workspaces: []Workspace{{Name: "a", Repos: []string{"x"}, Branch: "{{.Nope}}"}}}, "branch"},
		{"credentials in a repo", Config{Workspaces: []Workspace{{Name: "a", Repos: []string{"https://u:p@github.com/me/x.git"}}}}, "credential"},
		{"duplicate agent", Config{Agents: []AgentProfile{{Name: "c", Command: "c"}, {Name: "c", Command: "d"}}}, "duplicate agent"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.cfg.WorkspaceDefaults = WorkspaceDefaults{Root: "/ws", Isolation: "worktree", Agents: 1, AgentName: "agent-{{.N}}", Branch: "agents/{{.Workspace}}/{{.Agent}}"}
			err := validateWorkspaces(&tt.cfg)
			if tt.err == "" && err != nil || tt.err != "" && (err == nil || !strings.Contains(err.Error(), tt.err)) {
				t.Errorf("validateWorkspaces = %v, want %q", err, tt.err)
			}
		})
	}
}

func TestWithDefaults(t *testing.T) {
	cfg := &Config{WorkspaceDefaults: WorkspaceDefaults{Root: "/ws", Isolation: "worktree", Agents: 3, AgentName: "agent-{{.N}}", Branch: "agents/{{.Workspace}}/{{.Agent}}"}}

	w := cfg.WithDefaults(Workspace{Name: "app", Repos: []string{"x"}})
	names, err := w.AgentNames()
	if err != nil || w.Dir() != "/ws/app" || w.Isolation != "worktree" || len(names) != 3 || names[2] != "agent-3" {
		t.Errorf("w = %+v, names %v, err %v", w, names, err)
	}
	if b, err := w.BranchFor("agent-2", 2, "app"); err != nil || b != "agents/app/agent-2" {
		t.Errorf("branch = %q, %v", b, err)
	}
	if w := cfg.WithDefaults(Workspace{Name: "notes", Path: "/n"}); w.Isolation != "shared" {
		t.Errorf("a workspace over a path has isolation %q", w.Isolation)
	}
}

func TestValidBranch(t *testing.T) {
	for name, want := range map[string]bool{
		"agents/app/agent-1": true, "feature": true,
		"": false, "-x": false, "a..b": false, "a//b": false, "a/": false, "a.": false,
		"a b": false, "a:b": false, "a/.b": false, "a.lock": false, "a@{b": false, "@": false,
	} {
		if got := ValidBranch(name); got != want {
			t.Errorf("ValidBranch(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestValidateClone(t *testing.T) {
	bad := "blob:everything"
	for _, cfg := range []Config{
		{Clone: CloneConfig{Filter: "blob"}},
		{Accounts: []Account{{Name: "a", Clone: &CloneOverride{Filter: &bad}}}},
		{Overrides: []Override{{Repos: []string{"*"}, Clone: &CloneOverride{Filter: &bad}}}},
	} {
		if err := validateClone(&cfg); err == nil {
			t.Errorf("validateClone(%+v) accepted a bad filter", cfg)
		}
	}
	for _, f := range []string{"", "blob:none", "blob:limit=1m", "tree:0"} {
		if err := validateClone(&Config{Clone: CloneConfig{Filter: f}}); err != nil {
			t.Errorf("filter %q: %v", f, err)
		}
	}
}

func TestCloneFor_AccountThenOverride(t *testing.T) {
	none, one := "blob:none", 1
	cfg := &Config{
		Clone:     CloneConfig{Depth: 50},
		Accounts:  []Account{{Name: "work", Hosts: []string{"github.com"}, Owners: []string{"acme"}, Clone: &CloneOverride{Filter: &none}}},
		Overrides: []Override{{Repos: []string{"github.com/acme/big"}, Clone: &CloneOverride{Depth: &one}}},
	}
	if got := cfg.CloneFor("github.com/acme/big", ""); got != (CloneConfig{Filter: "blob:none", Depth: 1}) {
		t.Errorf("acme/big = %+v", got)
	}
	if got := cfg.CloneFor("github.com/me/x", ""); got != (CloneConfig{Depth: 50}) {
		t.Errorf("me/x = %+v", got)
	}
}
