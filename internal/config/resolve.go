package config

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ashtonian/git-mgr/internal/repourl"
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
			if o.Sync.Enabled != nil {
				resolved.Sync.Enabled = *o.Sync.Enabled
			}
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
			if o.Sync.Update != nil {
				resolved.Sync.Update = *o.Sync.Update
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
			if o.Backup.KeepAll != nil {
				resolved.Backup.KeepAll = *o.Backup.KeepAll
			}
			if o.Backup.KeepDaily != nil {
				resolved.Backup.KeepDaily = *o.Backup.KeepDaily
			}
			if o.Backup.MaxPerCopy != nil {
				resolved.Backup.MaxPerCopy = *o.Backup.MaxPerCopy
			}
			if o.Backup.MaxUntrackedFileMB != nil {
				resolved.Backup.MaxUntrackedFileMB = *o.Backup.MaxUntrackedFileMB
			}
			if o.Backup.MaxUntrackedTotalMB != nil {
				resolved.Backup.MaxUntrackedTotalMB = *o.Backup.MaxUntrackedTotalMB
			}
			if o.Backup.PatchDir != nil {
				resolved.Backup.PatchDir = *o.Backup.PatchDir
			}
		}
		o.RemoteSync.apply(&resolved.RemoteSync)
	}

	return &resolved
}

// RemoteSyncFor resolves remote_sync for a repo: the top-level settings, then
// its account's, then any override's.
func (c *Config) RemoteSyncFor(repoKey, ruleName string) RemoteSyncConfig {
	r := c.RemoteSync
	if a := c.AccountFor(repoKey, ruleName); a != nil {
		a.RemoteSync.apply(&r)
	}
	for _, o := range c.Overrides {
		if matchesOverride(repoKey, o.Repos) {
			o.RemoteSync.apply(&r)
		}
	}
	return r
}

// CloneFor resolves how a repo is cloned: the top-level clone settings,
// then its account's, then any override's.
func (c *Config) CloneFor(repoKey, ruleName string) CloneConfig {
	r := c.Clone
	if a := c.AccountFor(repoKey, ruleName); a != nil {
		a.Clone.apply(&r)
	}
	for _, o := range c.Overrides {
		if matchesOverride(repoKey, o.Repos) {
			o.Clone.apply(&r)
		}
	}
	return r
}

// RemoteSyncInWorkspace resolves remote_sync for a copy of a repo in a
// workspace: the repo's, then the workspace's.
func (c *Config) RemoteSyncInWorkspace(repoKey, ruleName, workspace string) RemoteSyncConfig {
	r := c.RemoteSyncFor(repoKey, ruleName)
	if w := c.FindWorkspace(workspace); w != nil {
		w.RemoteSync.apply(&r)
	}
	return r
}

// CloneProtocolFor resolves the clone transport for a repo, in order: a per-repo
// override (the last that sets it, as later overrides win), the provider that
// discovered it (via the discovery rule), then the top-level clone_protocol
// default.
func (c *Config) CloneProtocolFor(repoKey, discoveredBy string) string {
	for i := len(c.Overrides) - 1; i >= 0; i-- {
		if o := c.Overrides[i]; matchesOverride(repoKey, o.Repos) && o.CloneProtocol != "" {
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

// binding is one place config can bind a repo to a key or an account.
type binding struct {
	sshKey, account string
}

// bindings lists the places that may bind repoKey, most specific first:
// matching overrides (later entries first, as later entries win), the repo's
// `repos:` entry, its discovering rule, then that rule's provider.
func (c *Config) bindings(repoKey, ruleName string) []binding {
	var out []binding
	for i := len(c.Overrides) - 1; i >= 0; i-- {
		if o := c.Overrides[i]; matchesOverride(repoKey, o.Repos) {
			out = append(out, binding{o.SSHKey, o.Account})
		}
	}
	for _, mr := range c.Repos {
		if id, err := repourl.Parse(mr.URL); err == nil && id.Key() == repoKey {
			out = append(out, binding{mr.SSHKey, mr.Account})
		}
	}
	for _, r := range c.Discovery {
		if r.Name != ruleName {
			continue
		}
		out = append(out, binding{r.SSHKey, r.Account})
		for _, p := range c.Providers {
			if p.Name == r.Provider {
				out = append(out, binding{p.SSHKey, p.Account})
			}
		}
	}
	return out
}

// AccountFor returns the account that covers a repo: the one the most
// specific binding names, else the one whose hosts and owners match the
// repo's key. nil means no account covers it.
func (c *Config) AccountFor(repoKey, ruleName string) *Account {
	for _, b := range c.bindings(repoKey, ruleName) {
		if b.account != "" {
			return c.FindAccount(b.account)
		}
	}
	return c.accountByOwner(repoKey)
}

// ResolveSSHKey returns the SSH key for a repo: from the most specific binding
// that sets an ssh_key or names an account, else from the account matching
// its host and owner. "" means none is configured.
func (c *Config) ResolveSSHKey(repoKey, ruleName string) string {
	for _, b := range c.bindings(repoKey, ruleName) {
		switch {
		case b.sshKey != "":
			return ExpandHome(b.sshKey)
		case b.account != "":
			if a := c.FindAccount(b.account); a != nil {
				return ExpandHome(a.SSHKey)
			}
			return ""
		}
	}
	if a := c.accountByOwner(repoKey); a != nil {
		return ExpandHome(a.SSHKey)
	}
	return ""
}

// Host is the host a provider's repos are on: its URL's, without an "api."
// prefix, else github.com or gitlab.com for those types; "" when unknown.
func (p Provider) Host() string {
	if p.URL == "" {
		switch NormalizeProviderType(p.Type) {
		case "github":
			return "github.com"
		case "gitlab":
			return "gitlab.com"
		}
		return ""
	}
	u := strings.TrimPrefix(strings.TrimPrefix(p.URL, "https://"), "http://")
	host, _, _ := strings.Cut(u, "/")
	return strings.TrimPrefix(host, "api.")
}

// ProviderFor returns the provider whose token acts for repoKey on its host:
// the one its discovering rule names, else the one bound to its account on
// that host, else the only one on that host when it isn't bound to another
// account. nil when no provider serves the host. An error when several might
// and nothing says which, so a request is never made as the wrong account.
func (c *Config) ProviderFor(repoKey, ruleName string) (*Provider, error) {
	for _, r := range c.Discovery {
		if r.Name == ruleName {
			for i := range c.Providers {
				if c.Providers[i].Name == r.Provider {
					return &c.Providers[i], nil
				}
			}
		}
	}
	host, _ := splitKey(repoKey)
	var onHost []*Provider
	for i := range c.Providers {
		if c.Providers[i].Host() == host {
			onHost = append(onHost, &c.Providers[i])
		}
	}
	if len(onHost) == 0 {
		return nil, nil
	}
	account := c.AccountFor(repoKey, ruleName)
	if account != nil {
		var bound []*Provider
		for _, p := range onHost {
			if p.Account == account.Name {
				bound = append(bound, p)
			}
		}
		switch len(bound) {
		case 1:
			return bound[0], nil
		case 0:
		default:
			return nil, fmt.Errorf("%s: providers %s and %s both act for account %s on %s; keep one", repoKey, bound[0].Name, bound[1].Name, account.Name, host)
		}
	}
	if len(onHost) == 1 && (onHost[0].Account == "" || account == nil || onHost[0].Account == account.Name) {
		return onHost[0], nil
	}
	who := "it"
	if account != nil {
		who = "account " + account.Name
	}
	return nil, fmt.Errorf("%s: no provider on %s acts for %s; bind one to its account (providers[].account) or the repo to an account (repos[].account, overrides[].account)", repoKey, host, who)
}

// AccountsOn counts the accounts that cover repos on host.
func (c *Config) AccountsOn(host string) int {
	n := 0
	for _, a := range c.Accounts {
		if slices.Contains(a.Hosts, host) {
			n++
		}
	}
	return n
}

// FindAccount returns the named account, or nil.
func (c *Config) FindAccount(name string) *Account {
	for i := range c.Accounts {
		if c.Accounts[i].Name == name {
			return &c.Accounts[i]
		}
	}
	return nil
}

// accountByOwner matches a repo key against accounts' hosts and owners. An
// account listing the repo's owner (or a parent group of it) wins over one
// covering its whole host.
func (c *Config) accountByOwner(repoKey string) *Account {
	host, owner := splitKey(repoKey)
	var hostWide *Account
	for i := range c.Accounts {
		a := &c.Accounts[i]
		if !slices.Contains(a.Hosts, host) {
			continue
		}
		if len(a.Owners) == 0 {
			hostWide = a
			continue
		}
		for _, o := range a.Owners {
			if ownerCovers(o, owner) {
				return a
			}
		}
	}
	return hostWide
}

// splitKey splits host/owner/name into its host and owner; GitLab owners can
// span several segments.
func splitKey(repoKey string) (host, owner string) {
	host, rest, _ := strings.Cut(repoKey, "/")
	if i := strings.LastIndex(rest, "/"); i >= 0 {
		owner = rest[:i]
	}
	return host, owner
}

// ownerCovers reports whether an account owner covers a repo owner: the same
// owner, or a group the repo's owner is a subgroup of.
func ownerCovers(accountOwner, repoOwner string) bool {
	return strings.EqualFold(accountOwner, repoOwner) ||
		strings.HasPrefix(strings.ToLower(repoOwner), strings.ToLower(accountOwner)+"/")
}

func matchesOverride(repoKey string, patterns []string) bool {
	return slices.ContainsFunc(patterns, func(pat string) bool { return repourl.MatchKey(pat, repoKey) })
}
