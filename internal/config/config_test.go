package config_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ashtonian/gitten/internal/config"
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
	if cfg.Reconcile.Auto != "safe" || cfg.Reconcile.Drift != "report" {
		t.Errorf("reconcile: got %+v, want auto safe, drift report", cfg.Reconcile)
	}
}

// A config file the caller named must exist: a typo in --config or
// GITTEN_CONFIG must not silently run with zero discovery rules.
func TestLoad_MissingRequiredFile(t *testing.T) {
	_, err := config.Load(filepath.Join(t.TempDir(), "confg.yaml"))
	if !errors.Is(err, config.ErrNotFound) {
		t.Fatalf("want ErrNotFound for a missing required file, got %v", err)
	}
}

// The default location is optional: a zero-config run uses defaults and env.
func TestLoad_MissingOptionalFile(t *testing.T) {
	cfg, err := config.Load(filepath.Join(t.TempDir(), "config.yaml"), config.WithOptionalFile())
	if err != nil {
		t.Fatalf("missing optional file should load defaults: %v", err)
	}
	if cfg.BaseDir == "" {
		t.Error("expected defaults to be populated")
	}
}

func TestLoad_DirectoryIsAnError(t *testing.T) {
	if _, err := config.Load(t.TempDir(), config.WithOptionalFile()); err == nil {
		t.Fatal("a directory as config path must be an error")
	}
}

func TestLoad_UnreadableFileIsAnError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read any file")
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("base_dir: /x\n"), 0o000); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Load(path, config.WithOptionalFile()); err == nil {
		t.Fatal("an unreadable config must be an error, not defaults")
	}
}

func TestLoad_MalformedFileIsAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("discovery: [\n  - name: x\n    provider: [unclosed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Load(path, config.WithOptionalFile()); err == nil {
		t.Fatal("malformed YAML must be an error, not defaults")
	}
}

// The default locations follow the XDG base directories when they are set
// to absolute paths, and fall back to ~/.config, ~/.local/share and ~/.cache.
func TestDefaultDirs(t *testing.T) {
	home, _ := os.UserHomeDir()
	t.Setenv("XDG_CONFIG_HOME", "/xdg/config")
	t.Setenv("XDG_DATA_HOME", "/xdg/data")
	t.Setenv("XDG_CACHE_HOME", "relative/is/ignored")
	if got := config.DefaultConfigPath(); got != "/xdg/config/gitten/config.yaml" {
		t.Errorf("DefaultConfigPath = %s", got)
	}
	if got := config.DefaultStatePath(); got != "/xdg/data/gitten/state.yaml" {
		t.Errorf("DefaultStatePath = %s", got)
	}
	if got := config.CacheDir(); got != filepath.Join(home, ".cache", "gitten") {
		t.Errorf("CacheDir = %s", got)
	}
	cfg := config.Defaults()
	if cfg.Backup.ArchiveDir != "/xdg/data/gitten/archive" || cfg.Mirror.Dir != "/xdg/data/gitten/mirrors" {
		t.Errorf("data defaults: archive %s, mirrors %s", cfg.Backup.ArchiveDir, cfg.Mirror.Dir)
	}
	t.Setenv("XDG_CONFIG_HOME", "")
	if got := config.DefaultConfigPath(); got != filepath.Join(home, ".config", "gitten", "config.yaml") {
		t.Errorf("DefaultConfigPath without XDG = %s", got)
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

	// A repos: entry's own key applies to that repo whatever rule found it.
	cfg.Repos = []config.ManualRepo{{URL: "git@ghes:manual/repo.git", SSHKey: "~/.ssh/id_manual"}}
	if key = cfg.ResolveSSHKey("ghes/manual/repo", "_manual"); !contains(key, "id_manual") {
		t.Errorf("expected the repos: entry's key, got %q", key)
	}
	if key = cfg.ResolveSSHKey("ghes/manual/repo", "work"); !contains(key, "id_manual") {
		t.Errorf("a repos: entry's key outranks the rule's, got %q", key)
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
	if r.Backup.MaxPerCopy != 100 {
		t.Errorf("max_per_copy: got %d, want default 100", r.Backup.MaxPerCopy)
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

// TestLoad_EnvUnderscoreKey proves env overrides of koanf keys containing
// underscores now work. The previous hand-rolled loader mapped GITTEN_BASE_DIR
// to "base.dir" (≠ koanf key "base_dir") and silently ignored it; the pkg
// loader's struct-aware env mapping resolves it correctly.
func TestLoad_EnvUnderscoreKey(t *testing.T) {
	t.Setenv("GITTEN_BASE_DIR", "/custom/env/repos")
	cfg, err := config.Load(filepath.Join(t.TempDir(), "config.yaml"), config.WithOptionalFile())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.BaseDir != "/custom/env/repos" {
		t.Errorf("base_dir from env: got %q, want /custom/env/repos", cfg.BaseDir)
	}
}

// An override's empty sparse list checks out everything, over a sparse
// default; an override that doesn't mention sparse leaves it alone.
func TestLoad_CloneShape(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	yaml := `clone:
  sparse: [services/api]
  submodules: true
overrides:
  - repos: ["github.com/acme/small"]
    clone:
      sparse: []
      lfs: skip
  - repos: ["github.com/acme/other"]
    clone:
      depth: 1
`
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	small := cfg.CloneFor("github.com/acme/small", "")
	if len(small.Sparse) != 0 || !small.Submodules || !small.SkipLFS() {
		t.Errorf("acme/small = %+v", small)
	}
	other := cfg.CloneFor("github.com/acme/other", "")
	if len(other.Sparse) != 1 || other.Depth != 1 || other.SkipLFS() || other.LFS != "auto" {
		t.Errorf("acme/other = %+v", other)
	}
}

// An override's settings are checked as the settings themselves are, and a
// malformed glob is reported rather than matching nothing.
func TestLoad_OverridesAreValidated(t *testing.T) {
	for name, override := range map[string]string{
		"negative retries":    "sync: {retries: -1}",
		"negative timeout":    "sync: {timeout_per_repo: -1s}",
		"too many retries":    "sync: {retries: 50}",
		"negative keep_daily": "backup: {keep_daily: -1h}",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			yaml := "overrides:\n  - repos: [\"github.com/acme/*\"]\n    " + override + "\n"
			if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := config.Load(path); err == nil {
				t.Errorf("Load accepted %s", override)
			}
		})
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("overrides:\n  - repos: [\"github.com/[acme/*\"]\n    clone_protocol: https\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Load(path); err == nil || !strings.Contains(err.Error(), "overrides[0].repos") {
		t.Errorf("Load = %v, want the malformed glob named", err)
	}
}

// One glob grammar: * crosses slashes, so a host-wide or name-only pattern
// covers what it says, and later overrides win for every setting.
func TestResolve_OverrideGlobsAndPrecedence(t *testing.T) {
	retries := func(n int) *int { return &n }
	cfg := &config.Config{
		CloneProtocol: "ssh",
		Overrides: []config.Override{
			{Repos: []string{"ghes.company.com/*"}, CloneProtocol: "https", Sync: &config.SyncOverride{Retries: retries(1)}},
			{Repos: []string{"*/api"}, CloneProtocol: "ssh", Sync: &config.SyncOverride{Retries: retries(5)}},
		},
	}
	if got := cfg.CloneProtocolFor("ghes.company.com/team/web", ""); got != "https" {
		t.Errorf("host-wide override: protocol %s", got)
	}
	if got := cfg.CloneProtocolFor("ghes.company.com/team/api", ""); got != "ssh" {
		t.Errorf("the later override's protocol should win, got %s", got)
	}
	if got := cfg.ResolveForRepo("ghes.company.com/team/api").Sync.Retries; got != 5 {
		t.Errorf("the later override's retries should win, got %d", got)
	}
}

// An invalid config is reported once, naming the file, each problem on a
// line of its own.
func TestLoad_InvalidIsReportedOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("concurrency: -1\nlock_timeout: -1s\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := config.Load(path)
	if err == nil {
		t.Fatal("Load accepted an invalid config")
	}
	msg := err.Error()
	if !strings.HasPrefix(msg, "config "+path+" is invalid:\n  ") || strings.Count(msg, "invalid") != 1 || strings.Contains(msg, "E7002") {
		t.Errorf("error = %q", msg)
	}
}
