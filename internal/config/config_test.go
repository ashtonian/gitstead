package config_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ashtonian/git-mgr/internal/config"
	"gopkg.in/yaml.v3"
)

func TestDefaults(t *testing.T) {
	cfg := config.Defaults()
	home, _ := os.UserHomeDir()
	wantBase := filepath.Join(home, "repos")
	if cfg.BaseDir != wantBase {
		t.Errorf("base_dir: got %q, want %q", cfg.BaseDir, wantBase)
	}
	if cfg.PathTemplate != "{{.Host}}/{{.Owner}}/{{.Name}}" {
		t.Errorf("path_template: got %q", cfg.PathTemplate)
	}
	if cfg.Concurrency != 8 {
		t.Errorf("concurrency: got %d, want 8", cfg.Concurrency)
	}
	if !cfg.Backup.Enabled {
		t.Error("backup should be enabled by default")
	}
	if cfg.Backup.Strategy != "both" {
		t.Errorf("backup strategy: got %q, want both", cfg.Backup.Strategy)
	}
}

func TestLoad_NonExistent(t *testing.T) {
	cfg, err := config.Load("/tmp/nonexistent-git-mgr-config.yaml")
	if err != nil {
		t.Fatalf("Load nonexistent should return defaults: %v", err)
	}
	if cfg.BaseDir == "" {
		t.Error("expected defaults to be populated")
	}
}

func TestLoad_FromFile(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	content := `
base_dir: /custom/repos
path_template: "{{.Name}}"
concurrency: 4
providers:
  - name: github.com
    type: github
    token_command: echo test-token
`
	os.WriteFile(cfgPath, []byte(content), 0600)

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.BaseDir != "/custom/repos" {
		t.Errorf("base_dir: got %q, want /custom/repos", cfg.BaseDir)
	}
	if cfg.PathTemplate != "{{.Name}}" {
		t.Errorf("path_template: got %q", cfg.PathTemplate)
	}
	if cfg.Concurrency != 4 {
		t.Errorf("concurrency: got %d, want 4", cfg.Concurrency)
	}
	if len(cfg.Providers) != 1 {
		t.Fatalf("providers: got %d, want 1", len(cfg.Providers))
	}
	if cfg.Providers[0].Name != "github.com" {
		t.Errorf("provider name: got %q", cfg.Providers[0].Name)
	}
}

func TestResolveForRepo(t *testing.T) {
	cfg := config.Defaults()
	cfg.Overrides = []config.Override{
		{
			Repos:        []string{"github.com/company/*"},
			PathTemplate: "{{.Name}}",
		},
	}

	resolved := cfg.ResolveForRepo("github.com/company/api")
	if resolved.PathTemplate != "{{.Name}}" {
		t.Errorf("override not applied: got %q", resolved.PathTemplate)
	}

	// Non-matching repo should keep default
	resolved = cfg.ResolveForRepo("github.com/personal/dotfiles")
	if resolved.PathTemplate != "{{.Host}}/{{.Owner}}/{{.Name}}" {
		t.Errorf("default not preserved: got %q", resolved.PathTemplate)
	}
}

func TestResolveSSHKey(t *testing.T) {
	cfg := config.Defaults()
	cfg.Providers = []config.Provider{
		{Name: "ghes", Type: "github", SSHKey: "~/.ssh/id_work"},
	}
	cfg.Discovery = []config.DiscoveryRule{
		{Name: "work", Provider: "ghes", SSHKey: ""},
	}
	cfg.Overrides = []config.Override{
		{Repos: []string{"ghes/specific/repo"}, SSHKey: "~/.ssh/id_override"},
	}

	// Override wins
	key := cfg.ResolveSSHKey("ghes/specific/repo", "work")
	if !contains(key, "id_override") {
		t.Errorf("expected override key, got %q", key)
	}

	// Provider default for non-overridden repo
	key = cfg.ResolveSSHKey("ghes/other/repo", "work")
	if !contains(key, "id_work") {
		t.Errorf("expected provider key, got %q", key)
	}
}

func TestYAMLMarshal_SnakeCase(t *testing.T) {
	cfg := config.Defaults()
	out, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	s := string(out)
	if !contains(s, "base_dir:") {
		t.Errorf("expected base_dir in YAML output, got:\n%s", s)
	}
	if !contains(s, "path_template:") {
		t.Errorf("expected path_template in YAML output, got:\n%s", s)
	}
	if !contains(s, "auto_discover:") {
		t.Errorf("expected auto_discover in YAML output, got:\n%s", s)
	}
}

// A backup override that sets only one field must not silently disable
// backups (regression test for the override-merge bug).
func TestResolveForRepo_BackupOverrideKeepsDefaults(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	content := `
base_dir: /tmp/repos
providers:
  - name: github.com
    type: github
    token_command: echo t
overrides:
  - repos: ["github.com/company/*"]
    backup:
      strategy: patch
    sync:
      timeout_per_repo: 30s
`
	if err := os.WriteFile(cfgPath, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	r := cfg.ResolveForRepo("github.com/company/api")
	if !r.Backup.Enabled {
		t.Error("backup must stay enabled when override only sets strategy")
	}
	if r.Backup.Strategy != "patch" {
		t.Errorf("strategy: got %q, want patch", r.Backup.Strategy)
	}
	if !r.Backup.IncludeUntracked {
		t.Error("include_untracked must stay true (default) when not overridden")
	}
	if r.Backup.MaxPerRepo != 10 {
		t.Errorf("max_per_repo: got %d, want default 10", r.Backup.MaxPerRepo)
	}
	if r.Sync.TimeoutPerRepo != 30*time.Second {
		t.Errorf("timeout_per_repo: got %v, want 30s", r.Sync.TimeoutPerRepo)
	}
	// A non-matching repo keeps the global defaults.
	other := cfg.ResolveForRepo("github.com/other/repo")
	if other.Backup.Strategy != "both" {
		t.Errorf("non-matching strategy: got %q, want both", other.Backup.Strategy)
	}
}

// An explicit enabled:false in an override must still disable backups.
func TestResolveForRepo_BackupCanDisable(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	content := `
base_dir: /tmp/repos
providers:
  - name: github.com
    type: github
    token_command: echo t
overrides:
  - repos: ["github.com/company/*"]
    backup:
      enabled: false
`
	if err := os.WriteFile(cfgPath, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	r := cfg.ResolveForRepo("github.com/company/api")
	if r.Backup.Enabled {
		t.Error("backup should be disabled when override sets enabled: false")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsStr(s, substr))
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
