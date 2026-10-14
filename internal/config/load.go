package config

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	pkgconfig "github.com/ashtonian/pkg/config"
)

// ErrNotFound reports that a config file the caller required does not exist.
var ErrNotFound = errors.New("config file not found")

// --- Options pattern ---

// Option configures how config is loaded.
type Option func(*loadOptions)

type loadOptions struct {
	configPath     string
	optionalFile   bool
	envPrefix      string
	skipValidation bool
	extraDefaults  map[string]any
}

// WithConfigFile sets the config file path.
func WithConfigFile(path string) Option {
	return func(o *loadOptions) { o.configPath = path }
}

// WithOptionalFile treats a missing config file as "use defaults and env".
// Without it, a missing file is ErrNotFound: a mistyped --config must not
// silently run with zero discovery rules, which would mark every repo stale.
func WithOptionalFile() Option {
	return func(o *loadOptions) { o.optionalFile = true }
}

// WithEnvPrefix sets the env var prefix (default: "GITTEN_").
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
	cfg, _ := Load("", WithSkipValidation())
	return cfg
}

// configValidator adapts gitten's domain Validate(*Config) to the
// pkgconfig.Validator seam (Struct(any) error) so cross-field validation
// -- duplicate provider names, selector-vs-type, query exclusivity, workspace
// rules, path-template parsing -- runs inside GetConfig's pipeline. These rules
// are not expressible as struct tags, so go-playground/validator alone cannot
// cover them. Validation is normalization-independent (it inspects raw config
// values, not expanded paths), so running it before normalize() is equivalent
// to gitten's previous validate-after-normalize order.
type configValidator struct{}

func (configValidator) Struct(v any) error {
	cfg, ok := v.(*Config)
	if !ok {
		return nil
	}
	if err := Validate(cfg); err != nil {
		return &invalidError{err}
	}
	return nil
}

// invalidError carries gitten's validation error through the loader's
// wrapping, so Load can report it on its own.
type invalidError struct{ err error }

func (e *invalidError) Error() string { return e.err.Error() }
func (e *invalidError) Unwrap() error { return e.err }

// --- Loading ---

// Load reads configuration from path, layered over defaults and env. An empty
// path loads defaults and env only. A path that does not exist is ErrNotFound
// unless WithOptionalFile is given; any other problem reading it is an error.
//
// Loading is delegated to github.com/ashtonian/pkg/config, which
// supplies struct-tag + programmatic defaults, struct-aware env mapping (so
// GITTEN_BASE_DIR maps to the koanf key "base_dir", not "base.dir"), and the
// koanf decode pipeline. gitten layers its own path normalization and
// cross-field validation (via configValidator) on top.
func Load(path string, opts ...Option) (*Config, error) {
	o := &loadOptions{
		configPath: path,
		envPrefix:  "GITTEN_",
	}
	for _, opt := range opts {
		opt(o)
	}

	o.configPath = ExpandHome(o.configPath)

	kopts := []pkgconfig.Option{
		// pkgconfig trims a trailing "_" itself; pass the bare prefix.
		pkgconfig.WithEnvPrefix(strings.TrimSuffix(o.envPrefix, "_")),
	}
	if o.configPath != "" {
		info, err := os.Stat(o.configPath)
		switch {
		case err == nil && info.IsDir():
			return nil, fmt.Errorf("config path %s is a directory", o.configPath)
		case err == nil:
			// Must: pkgconfig skips an optional file it cannot read or parse,
			// which would turn a YAML typo into a defaults-only run.
			kopts = append(kopts, pkgconfig.WithMustConfigFile(o.configPath))
		case errors.Is(err, fs.ErrNotExist) && o.optionalFile:
			// Zero-config run: defaults and env only.
		case errors.Is(err, fs.ErrNotExist):
			return nil, fmt.Errorf("%w: %s", ErrNotFound, o.configPath)
		default:
			return nil, fmt.Errorf("reading config %s: %w", o.configPath, err)
		}
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
	if invalid := (*invalidError)(nil); errors.As(err, &invalid) {
		where := "config"
		if o.configPath != "" {
			where = "config " + o.configPath
		}
		sep := " " // a list of problems starts on a line of its own
		if strings.HasPrefix(invalid.err.Error(), "\n") {
			sep = ""
		}
		return nil, fmt.Errorf("%s is invalid:%s%w", where, sep, invalid.err)
	}
	if err != nil {
		return nil, err
	}
	if o.configPath != "" && !o.skipValidation {
		if err := checkKeys(o.configPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
	}

	normalize(cfg)
	if err := loadManifest(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

// normalize applies gitten's post-load fixups: ~ expansion, BaseDir cleaning,
// trailing-slash trimming on provider URLs, and the PatchDir default.
func normalize(cfg *Config) {
	if t, ok := Layouts[cfg.Layout]; ok {
		cfg.PathTemplate = t
	}
	expandPaths(cfg)
	cfg.BaseDir = filepath.Clean(cfg.BaseDir)
	for i := range cfg.Providers {
		cfg.Providers[i].URL = strings.TrimRight(cfg.Providers[i].URL, "/")
	}
}

func expandPaths(cfg *Config) {
	cfg.BaseDir = ExpandHome(cfg.BaseDir)
	cfg.Manifest = ExpandHome(cfg.Manifest)
	if cfg.Mirror.Dir == "" {
		cfg.Mirror.Dir = filepath.Join(DataDir(), "mirrors")
	} else {
		cfg.Mirror.Dir = filepath.Clean(ExpandHome(cfg.Mirror.Dir))
	}
	cfg.WorkspaceDefaults.Root = ExpandHome(cfg.WorkspaceDefaults.Root)
	if cfg.WorkspaceDefaults.CacheRoot == "" {
		cfg.WorkspaceDefaults.CacheRoot = defaultCacheRoot()
	} else {
		cfg.WorkspaceDefaults.CacheRoot = ExpandHome(cfg.WorkspaceDefaults.CacheRoot)
	}
	for i := range cfg.Workspaces {
		cfg.Workspaces[i].Root = ExpandHome(cfg.Workspaces[i].Root)
		cfg.Workspaces[i].Path = ExpandHome(cfg.Workspaces[i].Path)
	}
	if cfg.Backup.PatchDir == "" {
		cfg.Backup.PatchDir = filepath.Join(DataDir(), "patches")
	} else {
		cfg.Backup.PatchDir = ExpandHome(cfg.Backup.PatchDir)
	}
	if cfg.Backup.ArchiveDir == "" {
		cfg.Backup.ArchiveDir = filepath.Join(DataDir(), "archive")
	} else {
		cfg.Backup.ArchiveDir = ExpandHome(cfg.Backup.ArchiveDir)
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
