package config

import (
	"os"
	"path/filepath"
)

// The XDG base directories gitstead keeps its files in: $XDG_CONFIG_HOME,
// $XDG_DATA_HOME and $XDG_CACHE_HOME when set to absolute paths, else
// ~/.config, ~/.local/share and ~/.cache.
func xdgDir(env, fallback string) string {
	if dir := os.Getenv(env); filepath.IsAbs(dir) {
		return filepath.Join(dir, "gitstead")
	}
	return filepath.Join(ExpandHome(fallback), "gitstead")
}

// ConfigDir holds the config file.
func ConfigDir() string { return xdgDir("XDG_CONFIG_HOME", "~/.config") }

// DataDir holds state, its operation log and leases, backups, archives,
// mirrors and the scheduled sync's log, unless config puts them elsewhere.
func DataDir() string { return xdgDir("XDG_DATA_HOME", "~/.local/share") }

// CacheDir holds what can be made again: workspaces' shared caches, and
// lock files.
func CacheDir() string { return xdgDir("XDG_CACHE_HOME", "~/.cache") }

// DefaultConfigPath is where gitstead looks for its config when none is given.
func DefaultConfigPath() string { return filepath.Join(ConfigDir(), "config.yaml") }

// DefaultStatePath is where gitstead keeps its state when no path is given.
func DefaultStatePath() string { return filepath.Join(DataDir(), "state.yaml") }
