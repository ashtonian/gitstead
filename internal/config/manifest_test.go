package config

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
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
	if err := os.WriteFile(manifest, []byte("repos:\n  - url: git@github.com:me/notes.git\n    path: ~/notes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.yaml")
	yaml := "manifest: " + manifest + "\nrepos:\n  - url: git@github.com:me/dots.git\n"
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Repos) != 2 || cfg.Repos[1].URL != "git@github.com:me/notes.git" || cfg.Repos[1].Path != "~/notes" {
		t.Errorf("repos = %+v", cfg.Repos)
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
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), `unknown account "wrok"`) {
		t.Fatalf("Load = %v, want an unknown account error", err)
	}
}
