package config_test

import (
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

func TestSaveWorkgroup_AppendPreservesComments(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	writeFile(t, path, `# my git-mgr config
base_dir: ~/repos   # where repos live
providers:
  - name: github.com
    type: github
`)

	wg := config.Workgroup{
		Name: "scratch", Kind: "worktree", Source: "/code/app", BaseRef: "main",
		Members: []config.WorkgroupMember{{Name: "agent-1", Branch: "scratch/agent-1"}},
	}
	if err := config.SaveWorkgroup(path, wg, false); err != nil {
		t.Fatalf("SaveWorkgroup: %v", err)
	}

	out, _ := os.ReadFile(path)
	s := string(out)
	if !strings.Contains(s, "# my git-mgr config") || !strings.Contains(s, "# where repos live") {
		t.Errorf("comments not preserved:\n%s", s)
	}
	if !strings.Contains(s, "name: scratch") || !strings.Contains(s, "source: /code/app") {
		t.Errorf("workgroup not appended:\n%s", s)
	}
	// Re-load to confirm it's valid and the workgroup is present.
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(cfg.Workgroups) != 1 || cfg.Workgroups[0].Name != "scratch" {
		t.Errorf("reloaded workgroups = %+v", cfg.Workgroups)
	}
}

func TestSaveWorkgroup_ConflictRequiresForce(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	writeFile(t, path, "base_dir: ~/repos\n")

	wg := config.Workgroup{Name: "dup", Kind: "worktree", Source: "/a", Members: []config.WorkgroupMember{{Name: "m", Branch: "dup/m"}}}
	if err := config.SaveWorkgroup(path, wg, false); err != nil {
		t.Fatalf("first save: %v", err)
	}
	// Second save, same name, no force -> ErrWorkgroupExists.
	err := config.SaveWorkgroup(path, wg, false)
	if err == nil {
		t.Fatal("expected conflict error on duplicate save")
	}
	if !strings.Contains(err.Error(), "already declared") {
		t.Errorf("error = %v, want 'already declared'", err)
	}
	// With force, it replaces (no error, still one entry).
	wg.Source = "/b"
	if err := config.SaveWorkgroup(path, wg, true); err != nil {
		t.Fatalf("force save: %v", err)
	}
	cfg, _ := config.Load(path)
	if len(cfg.Workgroups) != 1 || cfg.Workgroups[0].Source != "/b" {
		t.Errorf("force-replace failed: %+v", cfg.Workgroups)
	}
}

func TestSaveWorkgroup_CreatesFileWhenAbsent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "config.yaml") // dir doesn't exist yet

	wg := config.Workgroup{Name: "new", Kind: "worktree", Source: "/x", Members: []config.WorkgroupMember{{Name: "m", Branch: "new/m"}}}
	if err := config.SaveWorkgroup(path, wg, false); err != nil {
		t.Fatalf("save to absent file: %v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(cfg.Workgroups) != 1 {
		t.Errorf("expected 1 workgroup in freshly-created config, got %d", len(cfg.Workgroups))
	}
}
