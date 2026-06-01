package config

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	pkgconfig "github.com/ashtonian/pkg/config"
)

// --- Options pattern ---

// Option configures how config is loaded.
type Option func(*loadOptions)

type loadOptions struct {
	configPath     string
	envPrefix      string
	skipValidation bool
	extraDefaults  map[string]any
}

// WithConfigFile sets the config file path.
func WithConfigFile(path string) Option {
	return func(o *loadOptions) { o.configPath = path }
}

// WithEnvPrefix sets the env var prefix (default: "GIT_MGR_").
func WithEnvPrefix(prefix string) Option {
	return func(o *loadOptions) { o.envPrefix = prefix }
}

// WithSkipValidation disables validation.
func WithSkipValidation() Option {
	return func(o *loadOptions) { o.skipValidation = true }
}

// WithDefaults provides programmatic defaults (override struct tag defaults).
func WithDefaults(defaults map[string]any) Option {
	return func(o *loadOptions) { o.extraDefaults = defaults }
}

// Defaults returns a Config populated from struct tag defaults.
func Defaults() *Config {
	cfg, _ := Load("/dev/null/nonexistent", WithSkipValidation())
	return cfg
}

// configValidator adapts git-mgr's domain Validate(*Config) to the
// pkgconfig.Validator seam (Struct(any) error) so cross-field validation
// -- duplicate provider names, selector-vs-type, query exclusivity, workgroup
// rules, path-template parsing -- runs inside GetConfig's pipeline. These rules
// are not expressible as struct tags, so go-playground/validator alone cannot
// cover them. Validation is normalization-independent (it inspects raw config
// values, not expanded paths), so running it before normalize() is equivalent
// to git-mgr's previous validate-after-normalize order.
type configValidator struct{}

func (configValidator) Struct(v any) error {
	cfg, ok := v.(*Config)
	if !ok {
		return nil
	}
	return Validate(cfg)
}

// --- Loading ---

// Load reads configuration with options. If no options are given, uses defaults.
//
// Loading is delegated to github.com/ashtonian/pkg/config, which
// supplies struct-tag + programmatic defaults, struct-aware env mapping (so
// GIT_MGR_BASE_DIR maps to the koanf key "base_dir", not "base.dir"), and the
// koanf decode pipeline. git-mgr layers its own path normalization and
// cross-field validation (via configValidator) on top.
func Load(path string, opts ...Option) (*Config, error) {
	o := &loadOptions{
		configPath: path,
		envPrefix:  "GIT_MGR_",
	}
	for _, opt := range opts {
		opt(o)
	}

	o.configPath = ExpandHome(o.configPath)

	kopts := []pkgconfig.Option{
		// pkgconfig trims a trailing "_" itself; pass the bare prefix.
		pkgconfig.WithEnvPrefix(strings.TrimSuffix(o.envPrefix, "_")),
	}
	if _, err := os.Stat(o.configPath); err == nil {
		kopts = append(kopts, pkgconfig.WithConfigFile(o.configPath))
	}
	if len(o.extraDefaults) > 0 {
		kopts = append(kopts, pkgconfig.WithDefaults(o.extraDefaults))
	}
	if o.skipValidation {
		kopts = append(kopts, pkgconfig.WithSkipValidation())
	} else {
		kopts = append(kopts, pkgconfig.WithValidator(configValidator{}))
	}

	cfg, err := pkgconfig.GetConfig[Config](context.Background(), kopts...)
	if err != nil {
		return nil, err
	}

	normalize(cfg)
	return cfg, nil
}

// normalize applies git-mgr's post-load fixups: ~ expansion, BaseDir cleaning,
// trailing-slash trimming on provider URLs, and the PatchDir default.
func normalize(cfg *Config) {
	expandPaths(cfg)
	cfg.BaseDir = filepath.Clean(cfg.BaseDir)
	for i := range cfg.Providers {
		cfg.Providers[i].URL = strings.TrimRight(cfg.Providers[i].URL, "/")
	}
}

func expandPaths(cfg *Config) {
	cfg.BaseDir = ExpandHome(cfg.BaseDir)
	cfg.WorkgroupBaseDir = ExpandHome(cfg.WorkgroupBaseDir)
	for i := range cfg.Workgroups {
		if cfg.Workgroups[i].BaseDir != "" {
			cfg.Workgroups[i].BaseDir = ExpandHome(cfg.Workgroups[i].BaseDir)
		}
	}
	if cfg.Backup.PatchDir == "" {
		cfg.Backup.PatchDir = ExpandHome("~/.local/share/git-mgr/patches")
	} else {
		cfg.Backup.PatchDir = ExpandHome(cfg.Backup.PatchDir)
	}
	if cfg.Log.File != "" {
		cfg.Log.File = ExpandHome(cfg.Log.File)
	}
	for i := range cfg.Providers {
		cfg.Providers[i].SSHKey = ExpandHome(cfg.Providers[i].SSHKey)
	}
	for i := range cfg.Discovery {
		cfg.Discovery[i].SSHKey = ExpandHome(cfg.Discovery[i].SSHKey)
	}
	for i := range cfg.Repos {
		cfg.Repos[i].SSHKey = ExpandHome(cfg.Repos[i].SSHKey)
	}
}
