package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestPortablePath(t *testing.T) {
	home, _ := os.UserHomeDir()
	base := "/b"
	for _, tt := range []struct{ local, templated, want string }{
		{"/b/github.com/a/b", "/b/github.com/a/b", ""},
		{"/b/work/b", "/b/github.com/a/b", "work/b"},
		{filepath.Join(home, "src", "b"), "/b/github.com/a/b", "~/src/b"},
		{"/elsewhere/x", "/b/github.com/a/b", "/elsewhere/x"},
	} {
		if got := PortablePath(tt.local, tt.templated, base); got != tt.want {
			t.Errorf("PortablePath(%s) = %q, want %q", tt.local, got, tt.want)
		}
	}
}

func TestManualPath(t *testing.T) {
	home, _ := os.UserHomeDir()
	cfg := &Config{BaseDir: "/b"}
	for path, want := range map[string]string{
		"x/y":     "/b/x/y",
		"/abs/p":  "/abs/p",
		"~/src/b": filepath.Join(home, "src", "b"),
	} {
		if got := cfg.ManualPath("github.com/a/b", path); got != want {
			t.Errorf("ManualPath(%q) = %q, want %q", path, got, want)
		}
	}
}

// Writers on one machine take turns; entries are kept by repo, whatever
// form their URL takes.
func TestUpdateManifest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shared", "repos.yaml")
	if m, err := ReadManifest(path); err != nil || len(m.Repos) != 0 {
		t.Fatalf("a missing manifest is empty: %+v, %v", m, err)
	}
	var wg sync.WaitGroup
	for _, url := range []string{"git@github.com:a/one.git", "git@github.com:a/two.git", "git@github.com:a/three.git"} {
		wg.Go(func() {
			if err := UpdateManifest(path, func(m *Manifest) error { m.Upsert(ManualRepo{URL: url}); return nil }); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	err := UpdateManifest(path, func(m *Manifest) error {
		m.Upsert(ManualRepo{URL: "https://github.com/a/one.git", Tags: []string{"x"}})
		m.Remove("github.com/a/two")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	m, err := ReadManifest(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Repos) != 2 || !m.Has("github.com/a/one") || !m.Has("github.com/a/three") || m.Has("github.com/a/two") {
		t.Errorf("manifest = %+v", m.Repos)
	}
	data, _ := os.ReadFile(path)
	if !strings.HasPrefix(string(data), "# Repos tracked by hand") {
		t.Errorf("the file should say what it is:\n%s", data)
	}
	if entries, _ := os.ReadDir(filepath.Dir(path)); len(entries) != 1 {
		t.Errorf("only the manifest belongs beside it, in a shared folder: %v", entries)
	}
	if err := os.WriteFile(path, []byte("repos:\n  - url: https://tok@github.com/a/b.git\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadManifest(path); err == nil {
		t.Error("an entry with a credential must be refused")
	}
}

// Config reads the manifest it names as more repos: entries.
func TestLoad_Manifest(t *testing.T) {
	dir := t.TempDir()
	manifest := filepath.Join(dir, "repos.yaml")
	if err := os.WriteFile(manifest, []byte("repos:\n  - url: git@github.com:me/notes.git\n    path: ~/notes\n    ssh_key: ~/.ssh/notes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.yaml")
	yaml := "manifest: " + manifest + "\nrepos:\n  - url: git@github.com:me/dots.git\n"
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Repos) != 2 || cfg.Repos[1].URL != "git@github.com:me/notes.git" || cfg.Repos[1].Path != "~/notes" {
		t.Errorf("repos = %+v", cfg.Repos)
	}
	// Its keys are expanded as the config's own entries' are.
	if want := ExpandHome("~/.ssh/notes"); cfg.Repos[1].SSHKey != want {
		t.Errorf("ssh_key = %q, want %q", cfg.Repos[1].SSHKey, want)
	}
}

// A shared manifest's entry is checked as a repos: entry is: an account it
// names must be one config has.
func TestLoad_ManifestUnknownAccount(t *testing.T) {
	dir := t.TempDir()
	manifest := filepath.Join(dir, "repos.yaml")
	if err := os.WriteFile(manifest, []byte("repos:\n  - url: git@github.com:acme/app.git\n    account: wrok\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("manifest: "+manifest+"\naccounts:\n  - name: work\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(t.Context(), path); err == nil || !strings.Contains(err.Error(), `unknown account "wrok"`) {
		t.Fatalf("Load = %v, want an unknown account error", err)
	}
}

// A repos: entry names its repo by exactly one of url and repo, and the
// error names the entry.
func TestValidate_RepoEntries(t *testing.T) {
	tests := []struct {
		name  string
		entry ManualRepo
		err   string
	}{
		{"by url", ManualRepo{URL: "git@github.com:acme/api.git"}, ""},
		{"by key", ManualRepo{Repo: "github.com/acme/api"}, ""},
		{"neither", ManualRepo{Tags: []string{"work"}}, "repos entry 2 has neither url nor repo"},
		{"both", ManualRepo{URL: "git@github.com:acme/api.git", Repo: "github.com/acme/api"}, "repos entry git@github.com:acme/api.git also sets repo github.com/acme/api"},
		{"a URL as the key", ManualRepo{Repo: "https://github.com/acme/api.git"}, "repos entry https://github.com/acme/api.git: repo key"},
		{"a key without an owner", ManualRepo{Repo: "github.com/api"}, "repos entry github.com/api: repo key"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Defaults()
			cfg.Repos = []ManualRepo{{URL: "git@github.com:me/first.git"}, tt.entry}
			err := Check(cfg)
			if tt.err == "" && err != nil || tt.err != "" && (err == nil || !strings.Contains(err.Error(), tt.err)) {
				t.Errorf("Check = %v, want %q", err, tt.err)
			}
		})
	}
}

// An entry by key is the repo's entry wherever config looks one up by key:
// its account and key bind the repo, and writes find it.
func TestRepoEntryByKey(t *testing.T) {
	cfg := Defaults()
	cfg.Accounts = []Account{{Name: "work", Hosts: []string{"github.com"}}}
	cfg.Repos = []ManualRepo{{Repo: "github.com/acme/api", Account: "work", SSHKey: "/keys/work"}}
	if r, ok := cfg.RepoEntry("github.com/acme/api"); !ok || r.Repo != "github.com/acme/api" {
		t.Errorf("RepoEntry = %+v, %v", r, ok)
	}
	if a := cfg.AccountFor("github.com/acme/api", ""); a == nil || a.Name != "work" {
		t.Errorf("AccountFor = %+v", a)
	}
	if key := cfg.ResolveSSHKey("github.com/acme/api", ""); key != "/keys/work" {
		t.Errorf("ResolveSSHKey = %q", key)
	}
	if !(Manifest{Repos: cfg.Repos}).Has("github.com/acme/api") {
		t.Error("the manifest's Has misses an entry by key")
	}
}

// An update to a repo's entry keeps naming the repo as the entry does: a
// repo key stays a key, leaving the transport to clone_protocol, and a url
// entry keeps a url. A new entry is written as given.
func TestUpsertKeepsHowAnEntryNamesItsRepo(t *testing.T) {
	byKey := ManualRepo{Repo: "github.com/acme/api", Tags: []string{"work"}}
	byURL := ManualRepo{URL: "git@github.com:acme/web.git"}
	m := Manifest{Repos: []ManualRepo{byKey, byURL}}
	m.Upsert(ManualRepo{URL: "https://github.com/acme/api.git", Account: "work"})
	m.Upsert(ManualRepo{Repo: "github.com/acme/web", SSHKey: "/keys/work"})
	m.Upsert(ManualRepo{URL: "https://github.com/acme/new.git"})
	want := []ManualRepo{
		{Repo: "github.com/acme/api", Account: "work"},
		{URL: "git@github.com:acme/web.git", SSHKey: "/keys/work"},
		{URL: "https://github.com/acme/new.git"},
	}
	if !reflect.DeepEqual(m.Repos, want) {
		t.Errorf("manifest after upserts = %+v, want %+v", m.Repos, want)
	}

	path := writeConfigDir(t, map[string]string{
		"config.yaml":        "accounts: [{name: work, hosts: [github.com]}]\nrepos:\n  - repo: github.com/acme/api # by key\n",
		"config.d/work.yaml": "repos:\n  - repo: github.com/acme/ops\n    account: work\n",
	})
	doc, err := OpenDocument(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := doc.UpsertRepo(ManualRepo{URL: "git@github.com:acme/api.git", Account: "work"}); err != nil {
		t.Fatal(err)
	}
	// The fragment's entry already says this, once named as it names it.
	if err := doc.UpsertRepo(ManualRepo{URL: "git@github.com:acme/ops.git", Account: "work"}); err != nil {
		t.Errorf("an update the fragment's entry already has: %v", err)
	}
	if err := doc.Save(t.Context()); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	var written struct{ Repos []ManualRepo }
	if err := yaml.Unmarshal(data, &written); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(written.Repos, []ManualRepo{{Repo: "github.com/acme/api", Account: "work"}}) || !strings.Contains(string(data), "# by key") {
		t.Errorf("config after UpsertRepo:\n%s", data)
	}
}
