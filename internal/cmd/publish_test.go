package cmd

import (
	"slices"
	"strings"
	"testing"

	"github.com/ashtonian/gitten/internal/config"
	"github.com/ashtonian/gitten/internal/repourl"
	"github.com/ashtonian/gitten/internal/state"
)

func TestForge(t *testing.T) {
	t.Setenv("GH_TOKEN", "ambient-personal-token")
	t.Setenv("GITLAB_TOKEN", "ambient-gitlab-token")
	t.Setenv("WORK_TOKEN", "work-token")
	cfg := &config.Config{
		Accounts: []config.Account{
			{Name: "work", Hosts: []string{"github.com"}, Owners: []string{"acme"}},
			{Name: "me", Hosts: []string{"github.com"}, Owners: []string{"me"}},
		},
		Providers: []config.Provider{
			{Name: "ghe", Type: "github", URL: "https://ghe.acme.com/api/v3", Token: "ghe-token"},
			{Name: "lab", Type: "gitlab", URL: "https://gitlab.acme.com", Token: "lab-token"},
			{Name: "gh-work", Type: "github", Account: "work", TokenEnv: "WORK_TOKEN"},
			{Name: "gh-me", Type: "github", Account: "me", TokenEnv: "ME_TOKEN_UNSET"},
		},
		Discovery: []config.DiscoveryRule{{Name: "enterprise", Provider: "ghe"}},
	}
	st := &state.State{Repos: map[string]*state.RepoState{"ghe.acme.com/acme/app": {DiscoveredBy: "enterprise"}}}
	tokens := func(env []string) []string {
		var out []string
		for _, kv := range env {
			if name, _, _ := strings.Cut(kv, "="); slices.Contains(forgeTokenVars, name) {
				out = append(out, kv)
			}
		}
		return out
	}

	tests := []struct {
		name, key, host string
		kind            string
		tokens          []string // the token variables gh or glab gets
		hostVar         string
		err             string
	}{
		{name: "GitHub Enterprise Server takes GH_ENTERPRISE_TOKEN", key: "ghe.acme.com/acme/app", host: "ghe.acme.com",
			kind: "github", tokens: []string{"GH_ENTERPRISE_TOKEN=ghe-token"}, hostVar: "GH_HOST=ghe.acme.com"},
		{name: "the only provider on a host", key: "gitlab.acme.com/acme/lib", host: "gitlab.acme.com",
			kind: "gitlab", tokens: []string{"GITLAB_TOKEN=lab-token"}, hostVar: "GITLAB_HOST=gitlab.acme.com"},
		{name: "the provider of the repo's account, not the shell's token", key: "github.com/acme/api", host: "github.com",
			kind: "github", tokens: []string{"GH_TOKEN=work-token"}},
		{name: "a token that doesn't resolve fails", key: "github.com/me/dotfiles", host: "github.com",
			err: "ME_TOKEN_UNSET is not set"},
		{name: "no account says which provider", key: "github.com/someone/else", host: "github.com",
			err: "no provider on github.com acts for it"},
		{name: "an unknown host", key: "git.example.com/me/x", host: "git.example.com"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kind, env, err := forge(cfg, st, tt.key, repourl.Identity{Host: tt.host})
			if tt.err != "" {
				if err == nil || !strings.Contains(err.Error(), tt.err) {
					t.Fatalf("forge = %v, want an error containing %q", err, tt.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if kind != tt.kind {
				t.Errorf("kind = %q, want %q", kind, tt.kind)
			}
			if tt.kind != "" && !slices.Equal(tokens(env), tt.tokens) {
				t.Errorf("tokens = %v, want %v", tokens(env), tt.tokens)
			}
			if tt.hostVar != "" && !slices.Contains(env, tt.hostVar) {
				t.Errorf("env lacks %s", tt.hostVar)
			}
		})
	}
}

// Without a provider for the host, gh's own login is used -- unless several
// accounts cover it, when that login may be another account's.
func TestForge_NoProvider(t *testing.T) {
	st := &state.State{Repos: map[string]*state.RepoState{}}
	one := &config.Config{Accounts: []config.Account{{Name: "me", Hosts: []string{"github.com"}}}}
	if kind, _, err := forge(one, st, "github.com/me/x", repourl.Identity{Host: "github.com"}); err != nil || kind != "github" {
		t.Errorf("one account: %q, %v", kind, err)
	}
	two := &config.Config{Accounts: []config.Account{{Name: "me", Hosts: []string{"github.com"}}, {Name: "work", Hosts: []string{"github.com"}}}}
	if _, _, err := forge(two, st, "github.com/me/x", repourl.Identity{Host: "github.com"}); err == nil {
		t.Error("two accounts on the host and no provider must not fall back to gh's login")
	}
}

func TestLastURL(t *testing.T) {
	out := "a pull request for branch \"x\" into branch \"main\" already exists:\nhttps://github.com/me/app/pull/7"
	if got := lastURL(out); got != "https://github.com/me/app/pull/7" {
		t.Errorf("lastURL = %q", got)
	}
	if got := lastURL("nothing here"); got != "" {
		t.Errorf("lastURL = %q", got)
	}
}
