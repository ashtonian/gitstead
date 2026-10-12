package cmd

import (
	"slices"
	"testing"

	"github.com/ashtonian/git-mgr/internal/config"
	"github.com/ashtonian/git-mgr/internal/repourl"
	"github.com/ashtonian/git-mgr/internal/state"
)

func TestForge(t *testing.T) {
	cfg := &config.Config{
		Providers: []config.Provider{
			{Name: "ghe", Type: "github", URL: "https://ghe.acme.com/api/v3", Token: "ghe-token"},
			{Name: "lab", Type: "gitlab", URL: "https://gitlab.acme.com", Token: "lab-token"},
		},
		Discovery: []config.DiscoveryRule{{Name: "work", Provider: "ghe"}},
	}
	st := &state.State{Repos: map[string]*state.RepoState{"ghe.acme.com/acme/app": {DiscoveredBy: "work"}}}

	kind, env := forge(cfg, st, "ghe.acme.com/acme/app", repourl.Identity{Host: "ghe.acme.com"})
	if kind != "github" || !slices.Contains(env, "GH_TOKEN=ghe-token") || !slices.Contains(env, "GH_HOST=ghe.acme.com") {
		t.Errorf("a repo discovered through GHE: %s %v", kind, env)
	}
	kind, env = forge(cfg, st, "gitlab.acme.com/acme/lib", repourl.Identity{Host: "gitlab.acme.com"})
	if kind != "gitlab" || !slices.Contains(env, "GITLAB_TOKEN=lab-token") {
		t.Errorf("a repo on a provider's host: %s %v", kind, env)
	}
	if kind, env := forge(cfg, st, "github.com/me/x", repourl.Identity{Host: "github.com"}); kind != "github" || len(env) != 0 {
		t.Errorf("github.com with no provider uses gh's own login: %s %v", kind, env)
	}
	if kind, _ := forge(cfg, st, "git.example.com/me/x", repourl.Identity{Host: "git.example.com"}); kind != "" {
		t.Errorf("an unknown host = %s", kind)
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
