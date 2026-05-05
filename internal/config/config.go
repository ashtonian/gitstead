package config

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"text/template"
	"time"

	"github.com/go-viper/mapstructure/v2"
	"github.com/go-playground/validator/v10"
	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/env"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
)

// Config is the top-level configuration.
type Config struct {
	BaseDir      string           `koanf:"base_dir"      yaml:"base_dir"      default:"~/repos"                              validate:"required"`
	PathTemplate string           `koanf:"path_template" yaml:"path_template" default:"{{.Host}}/{{.Owner}}/{{.Name}}"       validate:"required"`
	Concurrency  int              `koanf:"concurrency"   yaml:"concurrency"   default:"8"                                    validate:"min=1,max=128"`
	Providers    []Provider       `koanf:"providers"     yaml:"providers"      validate:"dive"`
	Discovery    []DiscoveryRule  `koanf:"discovery"     yaml:"discovery"      validate:"dive"`
	Repos        []ManualRepo     `koanf:"repos"         yaml:"repos,omitempty" validate:"dive"`
	Exclude      []ExcludeRule    `koanf:"exclude"       yaml:"exclude"        validate:"dive"`
	Overrides    []Override       `koanf:"overrides"     yaml:"overrides"      validate:"dive"`
	Sync         SyncConfig       `koanf:"sync"          yaml:"sync"`
	Cleanup      CleanupConfig    `koanf:"cleanup"       yaml:"cleanup"`
	Backup       BackupCfg        `koanf:"backup"        yaml:"backup"`
	RemoteSync   RemoteSyncConfig `koanf:"remote_sync"   yaml:"remote_sync"`
	Notify       NotifyConfig     `koanf:"notify"        yaml:"notify"`
	Log          LogConfig        `koanf:"log"           yaml:"log"`
}

// ManualRepo allows specifying individual repos to manage without discovery rules.
type ManualRepo struct {
	URL    string   `koanf:"url"     yaml:"url"              validate:"required"`
	Tags   []string `koanf:"tags"    yaml:"tags,omitempty"`
	SSHKey string   `koanf:"ssh_key" yaml:"ssh_key,omitempty"`
}

// Provider represents a GitHub or GitLab instance.
type Provider struct {
	Name         string `koanf:"name"          yaml:"name"                    validate:"required"`
	Type         string `koanf:"type"          yaml:"type"                    validate:"required,oneof=github gitlab"`
	URL          string `koanf:"url"           yaml:"url,omitempty"`
	Token        string `koanf:"token"         yaml:"token,omitempty"         sensitive:"true"`
	TokenEnv     string `koanf:"token_env"     yaml:"token_env,omitempty"`
	TokenCommand string `koanf:"token_command" yaml:"token_command,omitempty"`
	SSHKey       string `koanf:"ssh_key"       yaml:"ssh_key,omitempty"`
}

// DiscoveryRule defines how to find repos from a provider.
type DiscoveryRule struct {
	Name            string   `koanf:"name"             yaml:"name"                    validate:"required"`
	Provider        string   `koanf:"provider"         yaml:"provider"                validate:"required"`
	User            string   `koanf:"user"             yaml:"user,omitempty"`
	Org             string   `koanf:"org"              yaml:"org,omitempty"`
	Group           string   `koanf:"group"            yaml:"group,omitempty"`
	IncludeForks    bool     `koanf:"include_forks"    yaml:"include_forks"`
	IncludeArchived bool     `koanf:"include_archived" yaml:"include_archived"`
	Visibility      string   `koanf:"visibility"       yaml:"visibility,omitempty"    validate:"omitempty,oneof=all public private internal"`
	Languages       []string `koanf:"languages"        yaml:"languages,omitempty"`
	Topics          []string `koanf:"topics"           yaml:"topics,omitempty"`
	NamePattern     string   `koanf:"name_pattern"     yaml:"name_pattern,omitempty"  validate:"omitempty,valid_regex"`
	Tags            []string `koanf:"tags"             yaml:"tags,omitempty"`
	SSHKey          string   `koanf:"ssh_key"          yaml:"ssh_key,omitempty"`
}

// ExcludeRule defines repos to skip.
type ExcludeRule struct {
	NamePattern string   `koanf:"name_pattern" yaml:"name_pattern,omitempty" validate:"omitempty,valid_regex"`
	Repos       []string `koanf:"repos"        yaml:"repos,omitempty"`
}

// Override allows per-repo config overrides.
type Override struct {
	Repos        []string          `koanf:"repos"         yaml:"repos"                   validate:"required,min=1"`
	SSHKey       string            `koanf:"ssh_key"       yaml:"ssh_key,omitempty"`
	PathTemplate string            `koanf:"path_template" yaml:"path_template,omitempty"`
	Sync         *SyncConfig       `koanf:"sync"          yaml:"sync,omitempty"`
	Backup       *BackupCfg        `koanf:"backup"        yaml:"backup,omitempty"`
	RemoteSync   *RemoteSyncConfig `koanf:"remote_sync"   yaml:"remote_sync,omitempty"`
}

// SyncConfig controls sync behavior.
type SyncConfig struct {
	AutoDiscover     bool          `koanf:"auto_discover"      yaml:"auto_discover"      default:"true"`
	MinFetchInterval time.Duration `koanf:"min_fetch_interval" yaml:"min_fetch_interval" default:"10m"    validate:"min=0"`
	TimeoutPerRepo   time.Duration `koanf:"timeout_per_repo"   yaml:"timeout_per_repo"   default:"5m"     validate:"min=0"`
	Retries          int           `koanf:"retries"            yaml:"retries"            default:"2"      validate:"min=0,max=10"`
	RetryBackoff     time.Duration `koanf:"retry_backoff"      yaml:"retry_backoff"      default:"2s"     validate:"min=0"`
}

// CleanupConfig controls stale repo handling.
type CleanupConfig struct {
	Policy        string        `koanf:"policy"         yaml:"policy"         default:"mark_stale" validate:"required,oneof=mark_stale remove remove_and_delete ignore"`
	GracePeriod   time.Duration `koanf:"grace_period"   yaml:"grace_period"   default:"720h"       validate:"min=0"`
	ProtectManual bool          `koanf:"protect_manual" yaml:"protect_manual" default:"true"`
}

// BackupCfg controls backup behavior (renamed to avoid collision with gitops.BackupConfig).
type BackupCfg struct {
	Enabled          bool   `koanf:"enabled"           yaml:"enabled"           default:"true"`
	Strategy         string `koanf:"strategy"          yaml:"strategy"          default:"both"  validate:"required,oneof=stash-ref patch both"`
	IncludeUntracked bool   `koanf:"include_untracked" yaml:"include_untracked" default:"true"`
	MaxPerRepo       int    `koanf:"max_per_repo"      yaml:"max_per_repo"      default:"10"    validate:"min=0"`
	PatchDir         string `koanf:"patch_dir"         yaml:"patch_dir"`
}

// RemoteSyncConfig controls pushing backup refs.
type RemoteSyncConfig struct {
	Enabled    bool   `koanf:"enabled"     yaml:"enabled"`
	RemoteName string `koanf:"remote_name" yaml:"remote_name" default:"origin"`
	ReposGlob  string `koanf:"repos_glob"  yaml:"repos_glob"`
}

// NotifyConfig controls OS notifications.
type NotifyConfig struct {
	Enabled      bool `koanf:"enabled"       yaml:"enabled"`
	OnError      bool `koanf:"on_error"      yaml:"on_error"      default:"true"`
	OnCompletion bool `koanf:"on_completion" yaml:"on_completion"`
	OnStale      bool `koanf:"on_stale"      yaml:"on_stale"      default:"true"`
}

// LogConfig controls logging.
type LogConfig struct {
	Level  string `koanf:"level"  yaml:"level"  default:"info"  validate:"oneof=debug info warn error"`
	Format string `koanf:"format" yaml:"format" default:"json"  validate:"oneof=json text"`
	File   string `koanf:"file"   yaml:"file"`
}

// --- Options pattern ---

// Option configures how config is loaded.
type Option func(*loadOptions)

type loadOptions struct {
	configPath    string
	envPrefix     string
	skipValidation bool
	extraDefaults map[string]any
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

// --- Loading ---

// Load reads configuration with options. If no options are given, uses defaults.
func Load(path string, opts ...Option) (*Config, error) {
	o := &loadOptions{
		configPath: path,
		envPrefix:  "GIT_MGR_",
	}
	for _, opt := range opts {
		opt(o)
	}

	o.configPath = ExpandHome(o.configPath)

	k := koanf.New(".")

	// 1. Struct tag defaults (lowest precedence)
	tagDefaults := DefaultsFromTags[Config]()
	for key, val := range tagDefaults {
		k.Set(key, val)
	}

	// 2. Programmatic defaults
	for key, val := range o.extraDefaults {
		k.Set(key, val)
	}

	// 3. Config file
	if _, err := os.Stat(o.configPath); err == nil {
		if err := k.Load(file.Provider(o.configPath), yaml.Parser()); err != nil {
			return nil, fmt.Errorf("loading config %s: %w", o.configPath, err)
		}
	}

	// 4. Environment variables (highest precedence)
	if err := k.Load(env.Provider(o.envPrefix, ".", func(s string) string {
		return strings.ReplaceAll(strings.ToLower(strings.TrimPrefix(s, o.envPrefix)), "_", ".")
	}), nil); err != nil {
		return nil, fmt.Errorf("loading env vars: %w", err)
	}

	// Unmarshal with decode hooks for durations and slices
	cfg := &Config{}
	if err := k.UnmarshalWithConf("", cfg, koanf.UnmarshalConf{
		DecoderConfig: &mapstructure.DecoderConfig{
			DecodeHook: mapstructure.ComposeDecodeHookFunc(
				mapstructure.StringToTimeDurationHookFunc(),
				mapstructure.StringToSliceHookFunc(","),
			),
			WeaklyTypedInput: true,
			Result:           cfg,
			TagName:          "koanf",
		},
	}); err != nil {
		return nil, fmt.Errorf("unmarshaling config: %w", err)
	}

	// Expand ~ in paths
	expandPaths(cfg)

	// Normalize
	cfg.BaseDir = filepath.Clean(cfg.BaseDir)
	for i := range cfg.Providers {
		cfg.Providers[i].URL = strings.TrimRight(cfg.Providers[i].URL, "/")
	}

	// Validate
	if !o.skipValidation {
		if err := Validate(cfg); err != nil {
			return nil, err
		}
	}

	return cfg, nil
}

func expandPaths(cfg *Config) {
	cfg.BaseDir = ExpandHome(cfg.BaseDir)
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

// --- Validation ---

// Validate checks the config using go-playground/validator struct tags
// plus custom cross-field validation.
func Validate(cfg *Config) error {
	v := validator.New()

	// Register custom validator for regex patterns
	v.RegisterValidation("valid_regex", func(fl validator.FieldLevel) bool {
		_, err := regexp.Compile(fl.Field().String())
		return err == nil
	})

	// Struct tag validation (required, oneof, min, max, etc.)
	if err := v.Struct(cfg); err != nil {
		if validationErrors, ok := err.(validator.ValidationErrors); ok {
			return formatValidationErrors(validationErrors)
		}
		return fmt.Errorf("validation: %w", err)
	}

	// Cross-field validation
	if _, err := template.New("test").Parse(cfg.PathTemplate); err != nil {
		return fmt.Errorf("invalid path_template %q: %w", cfg.PathTemplate, err)
	}

	// Check duplicate provider names
	provNames := make(map[string]bool)
	for _, p := range cfg.Providers {
		if provNames[p.Name] {
			return fmt.Errorf("duplicate provider name: %q", p.Name)
		}
		provNames[p.Name] = true
	}

	// Check discovery rules reference valid providers
	for _, r := range cfg.Discovery {
		if !provNames[r.Provider] {
			return fmt.Errorf("discovery rule %q references unknown provider %q (available: %v)",
				r.Name, r.Provider, providerNames(cfg.Providers))
		}
	}

	return nil
}

func formatValidationErrors(errs validator.ValidationErrors) error {
	var msgs []string
	for _, e := range errs {
		field := strings.TrimPrefix(e.Namespace(), "Config.")
		switch e.Tag() {
		case "required":
			msgs = append(msgs, fmt.Sprintf("%s is required", field))
		case "oneof":
			msgs = append(msgs, fmt.Sprintf("%s must be one of [%s] (got %q)", field, e.Param(), e.Value()))
		case "min":
			msgs = append(msgs, fmt.Sprintf("%s must be >= %s (got %v)", field, e.Param(), e.Value()))
		case "max":
			msgs = append(msgs, fmt.Sprintf("%s must be <= %s (got %v)", field, e.Param(), e.Value()))
		case "valid_regex":
			msgs = append(msgs, fmt.Sprintf("%s contains invalid regex %q", field, e.Value()))
		default:
			msgs = append(msgs, fmt.Sprintf("%s: failed %s validation", field, e.Tag()))
		}
	}
	return fmt.Errorf("config validation failed:\n  %s", strings.Join(msgs, "\n  "))
}

func providerNames(providers []Provider) []string {
	names := make([]string, len(providers))
	for i, p := range providers {
		names[i] = p.Name
	}
	return names
}

// --- Defaults from struct tags ---

// DefaultsFromTags walks T's struct tags and extracts `default:"value"` into a flat map.
func DefaultsFromTags[T any]() map[string]any {
	defaults := make(map[string]any)
	var zero T
	walkDefaults(reflect.TypeOf(zero), "", defaults)
	return defaults
}

func walkDefaults(t reflect.Type, prefix string, defaults map[string]any) {
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return
	}
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if !field.IsExported() {
			continue
		}

		koanfTag := field.Tag.Get("koanf")
		if koanfTag == "" || koanfTag == "-" {
			continue
		}

		key := koanfTag
		if prefix != "" {
			key = prefix + "." + koanfTag
		}

		// Recurse into nested structs (not pointer-to-struct, those are optional)
		if field.Type.Kind() == reflect.Struct && field.Type != reflect.TypeOf(time.Duration(0)) {
			walkDefaults(field.Type, key, defaults)
			continue
		}

		defaultVal := field.Tag.Get("default")
		if defaultVal != "" {
			defaults[key] = defaultVal
		}
	}
}

// --- Path helpers ---

// ExpandHome replaces a leading ~ with the user's home directory.
func ExpandHome(path string) string {
	if path == "" {
		return path
	}
	if strings.HasPrefix(path, "~/") || path == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		return filepath.Join(home, path[1:])
	}
	return path
}

// --- Per-repo resolution ---

// ResolveForRepo returns a Config with per-repo overrides applied.
func (c *Config) ResolveForRepo(repoKey string) *Config {
	resolved := *c

	for _, o := range c.Overrides {
		if matchesOverride(repoKey, o.Repos) {
			if o.PathTemplate != "" {
				resolved.PathTemplate = o.PathTemplate
			}
			if o.Sync != nil {
				if o.Sync.TimeoutPerRepo > 0 {
					resolved.Sync.TimeoutPerRepo = o.Sync.TimeoutPerRepo
				}
				if o.Sync.MinFetchInterval > 0 {
					resolved.Sync.MinFetchInterval = o.Sync.MinFetchInterval
				}
			}
			if o.Backup != nil {
				if o.Backup.Strategy != "" {
					resolved.Backup.Strategy = o.Backup.Strategy
				}
				if !o.Backup.Enabled {
					resolved.Backup.Enabled = false
				}
				if !o.Backup.IncludeUntracked {
					resolved.Backup.IncludeUntracked = false
				}
			}
			if o.RemoteSync != nil {
				resolved.RemoteSync = *o.RemoteSync
			}
		}
	}

	return &resolved
}

// ResolveSSHKey returns the SSH key path for a given repo, checking overrides -> rule -> provider.
func (c *Config) ResolveSSHKey(repoKey, ruleName string) string {
	var overrideKey string
	for _, o := range c.Overrides {
		if matchesOverride(repoKey, o.Repos) && o.SSHKey != "" {
			overrideKey = o.SSHKey
		}
	}
	if overrideKey != "" {
		return ExpandHome(overrideKey)
	}

	for _, r := range c.Discovery {
		if r.Name == ruleName && r.SSHKey != "" {
			return ExpandHome(r.SSHKey)
		}
	}

	for _, r := range c.Discovery {
		if r.Name == ruleName {
			for _, p := range c.Providers {
				if p.Name == r.Provider && p.SSHKey != "" {
					return ExpandHome(p.SSHKey)
				}
			}
		}
	}

	return ""
}

func matchesOverride(repoKey string, patterns []string) bool {
	for _, pat := range patterns {
		matched, err := filepath.Match(pat, repoKey)
		if err == nil && matched {
			return true
		}
		if pat == repoKey {
			return true
		}
	}
	return false
}
