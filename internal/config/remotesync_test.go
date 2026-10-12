package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ashtonian/git-mgr/internal/config"
	"github.com/ashtonian/git-mgr/internal/repourl"
)

func ptr[T any](v T) *T { return &v }

// The account's policy applies to its repos, and an override's to the repos
// it matches, over the account's.
func TestRemoteSyncFor(t *testing.T) {
	cfg := accountsConfig()
	cfg.RemoteSync.Enabled = true
	cfg.Accounts[1].RemoteSync = &config.RemoteSyncOverride{Remote: ptr("backup"), WIP: ptr("off")}
	cfg.Overrides = []config.Override{{Repos: []string{"github.com/acme/secret"}, RemoteSync: &config.RemoteSyncOverride{Enabled: ptr(false)}}}

	if r := cfg.RemoteSyncFor("github.com/someone/dotfiles", ""); !r.Enabled || r.Remote != "origin" || r.WIP != "ref" || r.Unpushed != "push" {
		t.Errorf("personal repo: %+v", r)
	}
	if r := cfg.RemoteSyncFor("github.com/acme/api", ""); !r.Enabled || r.Remote != "backup" || r.WIP != "off" {
		t.Errorf("work repo: %+v; want the work account's remote and wip", r)
	}
	if r := cfg.RemoteSyncFor("github.com/acme/secret", ""); r.Enabled || r.Remote != "backup" {
		t.Errorf("overridden work repo: %+v; want the override's enabled over the account's remote", r)
	}
}

func TestOffsiteNamespace(t *testing.T) {
	tests := []struct {
		tmpl, want, err string
	}{
		{"refs/git-mgr/{{.Machine}}/{{.Copy}}", "refs/git-mgr/laptop/abc", ""},
		{"refs/backups/{{.Copy}}/", "refs/backups/abc", ""},
		{"refs/heads/wip/{{.Copy}}", "", "must stay hidden"},
		{"refs/tags/{{.Copy}}", "", "must stay hidden"},
		{"git-mgr/{{.Copy}}", "", "not a valid ref prefix"},
		{"refs/git-mgr/{{.Host}}/{{.Copy}}", "", "can't evaluate field Host"},
		{"refs/git mgr/{{.Copy}}", "", "not a valid ref prefix"},
	}
	for _, tt := range tests {
		got, err := config.OffsiteNamespace(tt.tmpl, "laptop", "abc")
		if tt.err == "" && (err != nil || got != tt.want) || tt.err != "" && (err == nil || !strings.Contains(err.Error(), tt.err)) {
			t.Errorf("OffsiteNamespace(%q) = %q, %v; want %q, %q", tt.tmpl, got, err, tt.want, tt.err)
		}
	}
}

func TestOffsiteRemote(t *testing.T) {
	id := repourl.Identity{Host: "github.com", Owner: "acme", Name: "api", FullName: "acme/api"}
	if got, _ := config.OffsiteRemote("origin", id); got != "origin" {
		t.Errorf("a remote name: %q", got)
	}
	if got, _ := config.OffsiteRemote("git@backup.example:{{.Host}}/{{.FullName}}.git", id); got != "git@backup.example:github.com/acme/api.git" {
		t.Errorf("a URL template: %q", got)
	}
}

func TestValidate_RemoteSync(t *testing.T) {
	tests := []struct {
		name string
		mod  func(*config.Config)
		err  string
	}{
		{"defaults", func(*config.Config) {}, ""},
		{"a namespace every copy shares", func(c *config.Config) { c.RemoteSync.Namespace = "refs/git-mgr/{{.Machine}}" }, "must include {{.Copy}}"},
		{"an account's visible namespace", func(c *config.Config) {
			c.Accounts = []config.Account{{Name: "work", RemoteSync: &config.RemoteSyncOverride{Namespace: ptr("refs/heads/{{.Copy}}")}}}
		}, `account "work"`},
		{"an override's broken remote template", func(c *config.Config) {
			c.Overrides = []config.Override{{Repos: []string{"x/*"}, RemoteSync: &config.RemoteSyncOverride{Remote: ptr("git@h:{{.Nope}}")}}}
		}, "remote_sync.remote"},
		{"an unknown wip mode", func(c *config.Config) { c.RemoteSync.WIP = "stash" }, "WIP"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.Defaults()
			tt.mod(cfg)
			err := config.Validate(cfg)
			if tt.err == "" && err != nil || tt.err != "" && (err == nil || !strings.Contains(err.Error(), tt.err)) {
				t.Errorf("Validate() = %v, want %q", err, tt.err)
			}
		})
	}
}

// A key that isn't a setting fails the load instead of silently falling back
// to the default.
func TestLoad_RejectsUnknownKeys(t *testing.T) {
	tests := []struct {
		yaml, err string
	}{
		{"sync:\n  timout_per_repo: 1m\n", `"sync.timout_per_repo" is not a setting (did you mean "timeout_per_repo"?)`},
		{"remote_sync:\n  remote_name: backup\n", `"remote_sync.remote_name" is not a setting`},
		{"accounts:\n  - name: work\n    ssh-key: ~/.ssh/id\n", `"accounts[0].ssh-key" is not a setting (did you mean "ssh_key"?)`},
		{"agents:\n  claude: claude --resume\n", ""}, // a map takes any key
		{"accounts:\n  - name: work\n    remote_sync:\n      wip: off\n", ""},
	}
	for _, tt := range tests {
		path := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(path, []byte(tt.yaml), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := config.Load(path)
		if tt.err == "" && err != nil || tt.err != "" && (err == nil || !strings.Contains(err.Error(), tt.err)) {
			t.Errorf("Load(%q) = %v, want %q", tt.yaml, err, tt.err)
		}
	}
}

// The annotated example documents only real settings.
func TestLoad_Example(t *testing.T) {
	if _, err := config.Load("../../config.example.yaml"); err != nil {
		t.Fatal(err)
	}
}
