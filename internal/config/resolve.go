package config

import (
	"os"
	"path/filepath"
	"strings"
)

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
		if !matchesOverride(repoKey, o.Repos) {
			continue
		}
		if o.BaseDir != "" {
			resolved.BaseDir = ExpandHome(o.BaseDir)
		}
		if o.PathTemplate != "" {
			resolved.PathTemplate = o.PathTemplate
		}
		if o.CloneProtocol != "" {
			resolved.CloneProtocol = o.CloneProtocol
		}
		if o.Sync != nil {
			if o.Sync.AutoDiscover != nil {
				resolved.Sync.AutoDiscover = *o.Sync.AutoDiscover
			}
			if o.Sync.MinFetchInterval != nil {
				resolved.Sync.MinFetchInterval = *o.Sync.MinFetchInterval
			}
			if o.Sync.TimeoutPerRepo != nil {
				resolved.Sync.TimeoutPerRepo = *o.Sync.TimeoutPerRepo
			}
			if o.Sync.Retries != nil {
				resolved.Sync.Retries = *o.Sync.Retries
			}
			if o.Sync.RetryBackoff != nil {
				resolved.Sync.RetryBackoff = *o.Sync.RetryBackoff
			}
		}
		if o.Backup != nil {
			if o.Backup.Enabled != nil {
				resolved.Backup.Enabled = *o.Backup.Enabled
			}
			if o.Backup.Strategy != nil {
				resolved.Backup.Strategy = *o.Backup.Strategy
			}
			if o.Backup.IncludeUntracked != nil {
				resolved.Backup.IncludeUntracked = *o.Backup.IncludeUntracked
			}
			if o.Backup.MaxPerRepo != nil {
				resolved.Backup.MaxPerRepo = *o.Backup.MaxPerRepo
			}
			if o.Backup.PatchDir != nil {
				resolved.Backup.PatchDir = *o.Backup.PatchDir
			}
			if o.Backup.PushBranch != nil {
				resolved.Backup.PushBranch = *o.Backup.PushBranch
			}
			if o.Backup.PushRemote != nil {
				resolved.Backup.PushRemote = *o.Backup.PushRemote
			}
		}
		if o.RemoteSync != nil {
			if o.RemoteSync.Enabled != nil {
				resolved.RemoteSync.Enabled = *o.RemoteSync.Enabled
			}
			if o.RemoteSync.RemoteName != nil {
				resolved.RemoteSync.RemoteName = *o.RemoteSync.RemoteName
			}
			if o.RemoteSync.ReposGlob != nil {
				resolved.RemoteSync.ReposGlob = *o.RemoteSync.ReposGlob
			}
		}
	}

	return &resolved
}

// CloneProtocolFor resolves the clone transport for a repo, in order: a per-repo
// override, the provider that discovered it (via the discovery rule), then the
// top-level clone_protocol default. Only consulted at first clone of a newly
// discovered repo; afterwards the stored remote URL is used.
func (c *Config) CloneProtocolFor(repoKey, discoveredBy string) string {
	for _, o := range c.Overrides {
		if matchesOverride(repoKey, o.Repos) && o.CloneProtocol != "" {
			return o.CloneProtocol
		}
	}
	for _, r := range c.Discovery {
		if r.Name == discoveredBy {
			for _, p := range c.Providers {
				if p.Name == r.Provider && p.CloneProtocol != "" {
					return p.CloneProtocol
				}
			}
		}
	}
	return c.CloneProtocol
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
