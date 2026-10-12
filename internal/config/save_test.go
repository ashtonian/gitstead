package config_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ashtonian/git-mgr/internal/config"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSaveWorkspace_AppendPreservesComments(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	writeFile(t, path, `# my git-mgr config
base_dir: ~/repos   # where repos live
providers:
  - name: github.com
    type: github
`)

	w := config.Workspace{Name: "scratch", Repos: []string{"/code/app"}, Agents: 2, BaseRef: "main"}
	if err := config.SaveWorkspace(path, w, false); err != nil {
		t.Fatalf("SaveWorkspace: %v", err)
	}

	out, _ := os.ReadFile(path)
	s := string(out)
	if !strings.Contains(s, "# my git-mgr config") || !strings.Contains(s, "# where repos live") {
		t.Errorf("comments not preserved:\n%s", s)
	}
	if !strings.Contains(s, "name: scratch") || !strings.Contains(s, "- /code/app") {
		t.Errorf("workspace not appended:\n%s", s)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(cfg.Workspaces) != 1 || cfg.Workspaces[0].Name != "scratch" || cfg.Workspaces[0].Agents != 2 {
		t.Errorf("reloaded workspaces = %+v", cfg.Workspaces)
	}
}

func TestSaveWorkspace_ConflictRequiresForce(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	writeFile(t, path, "base_dir: ~/repos\n")

	w := config.Workspace{Name: "dup", Repos: []string{"/a"}}
	if err := config.SaveWorkspace(path, w, false); err != nil {
		t.Fatalf("first save: %v", err)
	}
	err := config.SaveWorkspace(path, w, false)
	if !errors.Is(err, config.ErrWorkspaceExists) {
		t.Fatalf("duplicate save = %v, want ErrWorkspaceExists", err)
	}
	w.Repos = []string{"/b"}
	if err := config.SaveWorkspace(path, w, true); err != nil {
		t.Fatalf("force save: %v", err)
	}
	cfg, _ := config.Load(path)
	if len(cfg.Workspaces) != 1 || cfg.Workspaces[0].Repos[0] != "/b" {
		t.Errorf("force-replace failed: %+v", cfg.Workspaces)
	}
}

func TestSaveWorkspace_CreatesFileWhenAbsent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "config.yaml") // dir doesn't exist yet

	if err := config.SaveWorkspace(path, config.Workspace{Name: "new", Repos: []string{"/x"}}, false); err != nil {
		t.Fatalf("save to absent file: %v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(cfg.Workspaces) != 1 {
		t.Errorf("expected 1 workspace in freshly-created config, got %d", len(cfg.Workspaces))
	}
}
