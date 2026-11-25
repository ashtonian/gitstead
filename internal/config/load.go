package config

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	pkgconfig "github.com/ashtonian/pkg/config"
	"github.com/go-playground/validator/v10"
	"github.com/knadh/koanf/providers/confmap"
	"github.com/knadh/koanf/v2"
)

// ErrNotFound reports that a config file the caller required does not exist.
var ErrNotFound = errors.New("config file not found")

// EnvPrefix starts the environment variables that set config keys:
// GITSTEAD_BASE_DIR sets base_dir, GITSTEAD_SYNC_TIMEOUT_PER_REPO
// sync.timeout_per_repo.
const EnvPrefix = "GITSTEAD"

// Option configures how config is loaded.
type Option func(*loadOptions)

type loadOptions struct {
	optionalFile bool
	flags        koanf.Provider
	// mainData stands in for the config file's contents when mainGiven: a
	// document checked before it is written.
	mainData  []byte
	mainGiven bool
}

// WithOptionalFile treats a missing config file as "use defaults and env".
// Without it, a missing file is ErrNotFound: a mistyped --config must not
// silently run with zero discovery rules, which would mark every repo stale.
func WithOptionalFile() Option {
	return func(o *loadOptions) { o.optionalFile = true }
}

// WithFlags sets config keys from command-line flags: p's keys, above the
// file and the environment, and validated with them.
func WithFlags(p koanf.Provider) Option {
	return func(o *loadOptions) { o.flags = p }
}

// withMainData loads data as the config file's contents, with the file's
// fragments as they are on disk.
func withMainData(data []byte) Option {
	return func(o *loadOptions) { o.mainData, o.mainGiven = data, true }
}

// Defaults returns the config the struct-tag defaults make, normalized as
// Load normalizes: no file, no environment.
func Defaults() *Config {
	// Nothing is read, so there is nothing to cancel.
	cfg, err := pkgconfig.Load[Config](context.Background(),
		pkgconfig.WithValidator(tagValidator()),
		pkgconfig.WithPostLoad(normalize),
	)
	if err != nil {
		panic(fmt.Sprintf("config: the defaults are not a valid config: %v", err))
	}
	return cfg
}

// Load reads the config file at path and the fragments in the config.d
// beside it, merged as one file (mergeSources), over the struct-tag
// defaults, with GITSTEAD_* environment variables and then flags (WithFlags)
// over them, and validates the result, with this machine's machines: section
// laid over it (applyMachines). An empty path reads no file. A path that does
// not exist is ErrNotFound unless WithOptionalFile is given; a file that
// exists must read and parse, as must every fragment.
//
// github.com/ashtonian/pkg/config does the loading: it fails on a key no
// setting takes, maps each variable to its key by Config's own keys, and
// validates with the struct tags and then Config.Validate. Each file is
// first decoded on its own, so a key no setting takes is reported in the file
// it is in; validation waits for the merge, since one file may name an
// account another defines. gitstead's post-load steps -- the shared
// manifest's repos, then normalize -- run before validation, so the rules
// judge the config commands will use.
func Load(ctx context.Context, path string, opts ...Option) (*Config, error) {
	o := &loadOptions{}
	for _, opt := range opts {
		opt(o)
	}
	path = ExpandHome(path)

	var srcs []source
	if path != "" {
		var err error
		if srcs, err = readSources(ctx, path, o); err != nil {
			return nil, err
		}
	}
	merged, err := mergeSources(srcs)
	if err != nil {
		return nil, err
	}
	files := make([]string, len(srcs))
	for i, src := range srcs {
		files[i] = src.path
	}

	kopts := []pkgconfig.Option{
		pkgconfig.WithEnvPrefix(EnvPrefix),
		pkgconfig.WithValidator(tagValidator()),
		pkgconfig.WithBaseProvider(confmap.Provider(merged, ""), nil),
		pkgconfig.WithPostLoad(func(_ context.Context, cfg *Config) error {
			cfg.files = files
			return nil
		}),
		pkgconfig.WithPostLoad(loadManifest),
		pkgconfig.WithPostLoad(normalize),
		pkgconfig.WithPostLoad(applyMachines),
	}
	if o.flags != nil {
		kopts = append(kopts, pkgconfig.WithOverrideProvider(o.flags, nil))
	}
	cfg, err := pkgconfig.Load[Config](ctx, kopts...)
	if err != nil {
		return nil, loadError(describeFiles(path, files), err)
	}
	return cfg, nil
}

// Files are the config files this config was read from, in the order they
// merged: the config file, when it exists, then its fragments. None is an
// empty list, never nil, so JSON shows [].
func (c *Config) Files() []string {
	return append([]string{}, c.files...)
}

// describeFiles names the config files read for path in an error: every one
// of them, or the path that would have been read.
func describeFiles(path string, files []string) string {
	switch {
	case len(files) > 0:
		return "config " + strings.Join(files, ", ")
	case path != "":
		return "config " + path
	}
	return "config"
}

// invalidError is a rule the config breaks, carried through the loader's
// wrapping so Load reports it as the config being invalid.
type invalidError struct{ err error }

func (e *invalidError) Error() string { return e.err.Error() }
func (e *invalidError) Unwrap() error { return e.err }

// stepError is a post-load step's failure that names its own subject (the
// manifest), carried through the loader's wrapping so Load reports it as
// written.
type stepError struct{ err error }

func (e *stepError) Error() string { return e.err.Error() }
func (e *stepError) Unwrap() error { return e.err }

// loadError is err, a failure loading the config read from where, in
// gitstead's words.
func loadError(where string, err error) error {
	var (
		unknown *pkgconfig.UnknownKeysError
		fields  validator.ValidationErrors
		invalid *invalidError
		step    *stepError
	)
	switch {
	case errors.As(err, &unknown):
		return invalidConfig(where, describeUnknown(unknown))
	case errors.As(err, &fields):
		return invalidConfig(where, describeFields(fields))
	case errors.As(err, &invalid):
		return invalidConfig(where, invalid.err)
	case errors.As(err, &step):
		return step.err
	}
	return fmt.Errorf("%s: %w", where, err)
}

// describeUnknown lists the keys that are not settings, each on a line of
// its own with the setting it most likely misspells.
func describeUnknown(e *pkgconfig.UnknownKeysError) error {
	msgs := make([]string, len(e.Keys))
	for i, k := range e.Keys {
		msgs[i] = fmt.Sprintf("%q is not a setting", k.Key)
		if k.Suggestion != "" {
			msgs[i] += fmt.Sprintf(" (did you mean %q?)", k.Suggestion)
		}
	}
	return fmt.Errorf("\n  %s", strings.Join(msgs, "\n  "))
}

// invalidConfig reports the problems with the config at where, a list of
// problems starting on a line of its own.
func invalidConfig(where string, problems error) error {
	sep := " "
	if strings.HasPrefix(problems.Error(), "\n") {
		sep = ""
	}
	return fmt.Errorf("%s is invalid:%s%w", where, sep, problems)
}

// normalize makes the config what commands use: layout's path_template in
// place of the default, paths with ~ expanded, and the data directories
// filled in where the config leaves them out.
func normalize(_ context.Context, cfg *Config) error {
	if t, ok := Layouts[cfg.Layout]; ok {
		if cfg.PathTemplate != DefaultPathTemplate && cfg.PathTemplate != t {
			return &invalidError{fmt.Errorf("set layout or path_template, not both (layout %q, path_template %q)", cfg.Layout, cfg.PathTemplate)}
		}
		cfg.PathTemplate = t
	}
	expandPaths(cfg)
	// An empty base_dir stays empty, for validation to reject, rather than
	// becoming ".".
	if cfg.BaseDir != "" {
		cfg.BaseDir = filepath.Clean(cfg.BaseDir)
	}
	for i := range cfg.Providers {
		cfg.Providers[i].URL = strings.TrimRight(cfg.Providers[i].URL, "/")
	}
	return nil
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
