package config

import (
	"path/filepath"
	"time"
)

// MirrorConfig keeps a full mirror of each repo's upstream -- every branch,
// tag and other ref the host has -- as a bare repository: a copy that
// outlives the host losing the repo, or you losing the account.
type MirrorConfig struct {
	// Enabled mirrors every tracked repo; accounts[].mirror and
	// overrides[].mirror turn it on or off for some.
	Enabled bool `koanf:"enabled" yaml:"enabled"`
	// Dir holds the mirrors, at <dir>/<host>/<owner>/<name>.git: a backup
	// disk, a NAS mount. Empty for ~/.local/share/git-mgr/mirrors.
	Dir string `koanf:"dir" yaml:"dir,omitempty"`
	// Interval is how long sync leaves a mirror before updating it again.
	Interval time.Duration `koanf:"interval" yaml:"interval" default:"24h" validate:"min=0"`
	// LFS also mirrors the repos' Git LFS content.
	LFS bool `koanf:"lfs" yaml:"lfs,omitempty"`
	// KeepRewritten is how long a mirror keeps the old tip of a ref its
	// upstream rewrote, such as a force-pushed branch; 0 keeps none.
	KeepRewritten time.Duration `koanf:"keep_rewritten" yaml:"keep_rewritten" default:"2160h" validate:"min=0"`
}

// MirrorOverride turns mirroring on or off for an account's repos or some
// repos. A nil pointer means "not set".
type MirrorOverride struct {
	Enabled *bool `koanf:"enabled" yaml:"enabled,omitempty"`
	LFS     *bool `koanf:"lfs"     yaml:"lfs,omitempty"`
}

// RepoMirror is how a repo is mirrored: whether, and its LFS content too.
type RepoMirror struct {
	Enabled, LFS bool
}

// MirrorFor resolves whether a repo is mirrored: the top-level setting, then
// its account's, then any override's.
func (c *Config) MirrorFor(repoKey, ruleName string) RepoMirror {
	m := RepoMirror{Enabled: c.Mirror.Enabled, LFS: c.Mirror.LFS}
	apply := func(o *MirrorOverride) {
		if o == nil {
			return
		}
		if o.Enabled != nil {
			m.Enabled = *o.Enabled
		}
		if o.LFS != nil {
			m.LFS = *o.LFS
		}
	}
	if a := c.AccountFor(repoKey, ruleName); a != nil {
		apply(a.Mirror)
	}
	for _, o := range c.Overrides {
		if matchesOverride(repoKey, o.Repos) {
			apply(o.Mirror)
		}
	}
	return m
}

// MirrorPath is where repo key's mirror goes.
func (c *Config) MirrorPath(key string) string {
	return filepath.Join(c.Mirror.Dir, filepath.FromSlash(key)+".git")
}
