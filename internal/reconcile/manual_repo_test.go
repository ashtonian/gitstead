package reconcile_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/ashtonian/gitstead/internal/config"
	"github.com/ashtonian/gitstead/internal/discovery"
	"github.com/ashtonian/gitstead/internal/reconcile"
	"github.com/ashtonian/gitstead/internal/state"
)

// desiredRepos is what config asks for of its repos: entries alone.
func desiredRepos(t *testing.T, cfg *config.Config) map[string]reconcile.Repo {
	t.Helper()
	results, discovered, err := discovery.RunDiscovery(context.Background(), cfg, "", "")
	if err != nil {
		t.Fatal(err)
	}
	st, err := state.Load(filepath.Join(t.TempDir(), "state.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	repos, blocked := reconcile.Repos(cfg, st, discovered, discovery.StaleScopeOf(results, "", ""))
	if len(blocked) > 0 {
		t.Fatalf("blocked: %+v", blocked)
	}
	return repos
}

// A repos: entry by key is the repo an entry by its URL is -- key, path and
// clone URL -- over whichever transport clone_protocol names, so changing an
// entry from one form to the other changes nothing.
func TestRepos_EntryByKeyIsEntryByURL(t *testing.T) {
	const key = "github.com/acme/CamelCase"
	for protocol, url := range map[string]string{
		"ssh":   "git@github.com:acme/CamelCase.git",
		"https": "https://github.com/acme/CamelCase.git",
	} {
		t.Run(protocol, func(t *testing.T) {
			cfg := config.Defaults()
			cfg.BaseDir, cfg.CloneProtocol = t.TempDir(), protocol
			cfg.Repos = []config.ManualRepo{{URL: url}}
			byURL := desiredRepos(t, cfg)[key]
			cfg.Repos = []config.ManualRepo{{Repo: key}}
			byKey := desiredRepos(t, cfg)[key]
			if byKey.Key != key || byKey.Key != byURL.Key || byKey.Path != byURL.Path || byKey.URL != url || byURL.URL != url {
				t.Errorf("by key %s at %s from %s; by url %s at %s from %s", byKey.Key, byKey.Path, byKey.URL, byURL.Key, byURL.Path, byURL.URL)
			}
		})
	}
}

// An entry by key takes its transport as a discovered repo does: an
// override's clone_protocol, else the top level's. An entry by URL keeps its
// URL's.
func TestRepos_EntryByKeyTakesCloneProtocol(t *testing.T) {
	cfg := config.Defaults()
	cfg.BaseDir, cfg.CloneProtocol = t.TempDir(), "ssh"
	cfg.Overrides = []config.Override{{Repos: []string{"github.com/acme/*"}, CloneProtocol: "https"}}
	cfg.Repos = []config.ManualRepo{
		{Repo: "github.com/acme/api"},
		{Repo: "github.com/me/notes"},
		{URL: "git@github.com:acme/web.git"},
	}
	repos := desiredRepos(t, cfg)
	for key, want := range map[string]string{
		"github.com/acme/api": "https://github.com/acme/api.git",
		"github.com/me/notes": "git@github.com:me/notes.git",
		"github.com/acme/web": "git@github.com:acme/web.git",
	} {
		if got := repos[key].URL; got != want {
			t.Errorf("%s clones from %s, want %s", key, got, want)
		}
	}
}
