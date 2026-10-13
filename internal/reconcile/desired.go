package reconcile

import (
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/ashtonian/git-mgr/internal/config"
	"github.com/ashtonian/git-mgr/internal/discovery"
	"github.com/ashtonian/git-mgr/internal/gitops"
	"github.com/ashtonian/git-mgr/internal/pathtemplate"
	"github.com/ashtonian/git-mgr/internal/repourl"
	"github.com/ashtonian/git-mgr/internal/shellutil"
	"github.com/ashtonian/git-mgr/internal/state"
)

// Managed git config keys, in the lower-case form `git config --list` reports.
const (
	keySSHCommand = "core.sshcommand"
	keyUserName   = "user.name"
	keyUserEmail  = "user.email"
)

// AccountConfig is the repo-local git config an account asks for: its commit
// identity and signing settings. The SSH key's core.sshCommand is added per
// repo, since it depends on the remote.
func AccountConfig(a *config.Account) map[string]string {
	cfg := map[string]string{}
	if a == nil {
		return cfg
	}
	if a.Git.Name != "" {
		cfg[keyUserName] = a.Git.Name
	}
	if a.Git.Email != "" {
		cfg[keyUserEmail] = a.Git.Email
	}
	switch s := a.Signing; s.Mode {
	case "required":
		format := s.SigningFormat()
		cfg["commit.gpgsign"], cfg["tag.gpgsign"], cfg["gpg.format"] = "true", "true", format
		if s.Key != "" {
			key := s.Key
			if format == "ssh" && !strings.HasPrefix(key, "key::") {
				key = config.ExpandHome(key)
			}
			cfg["user.signingkey"] = key
		}
		if s.Program != "" {
			program := map[string]string{"ssh": "gpg.ssh.program", "openpgp": "gpg.program", "x509": "gpg.x509.program"}[format]
			cfg[program] = config.ExpandHome(s.Program)
		}
		if s.AllowedSigners != "" && format == "ssh" {
			cfg["gpg.ssh.allowedsignersfile"] = config.ExpandHome(s.AllowedSigners)
		}
	case "off":
		cfg["commit.gpgsign"], cfg["tag.gpgsign"] = "false", "false"
	}
	return cfg
}

// Repo is what config and discovery want for one tracked repo.
type Repo struct {
	Key, Host, Owner, Name, FullName string

	ProviderID string
	Path       string
	URL        string
	SSHKey     string   // the key git-mgr uses for this repo; "" for ssh's own choice
	Account    string   // the account covering it; "" for none
	Tags       []string // sorted
	UserTags   []string
	Rule       string             // discovering rule; "_manual" for repos: entries
	Config     map[string]string  // managed repo-local git config, by lower-case key
	Clone      config.CloneConfig // how a new clone is made

	// Pinned repos were added by hand: their path and URL are the recorded
	// ones, never derived from config.
	Pinned bool
	// Found is this run's discovery result; nil when discovery didn't see it.
	Found *discovery.Repo
	// accountConfig is the managed config the account adds; Config is it
	// plus the SSH key's.
	accountConfig map[string]string
}

// SSHCommand is the core.sshCommand git-mgr writes for a repo with a
// configured key, so your own git commands in it use that key. Host-key
// checking is left to your ssh config.
func SSHCommand(key string) string {
	return "ssh -i " + shellutil.Quote(key) + " -o IdentitiesOnly=yes"
}

// managedConfig is the repo-local git config git-mgr keeps for a copy: the
// SSH key for SSH remotes, what the covering account asks for, and Git LFS
// kept from downloading under clone.lfs: skip -- for existing clones' later
// checkouts too.
func managedConfig(url, sshKey string, accountConfig map[string]string, clone config.CloneConfig) map[string]string {
	cfg := maps.Clone(accountConfig)
	if cfg == nil {
		cfg = map[string]string{}
	}
	if sshKey != "" && !repourl.IsHTTP(url) {
		cfg[keySSHCommand] = SSHCommand(sshKey)
	}
	if clone.SkipLFS() {
		cfg[gitops.LFSFetchExclude] = "*"
	}
	if len(cfg) == 0 {
		return nil
	}
	return cfg
}

// access fills in the SSH key, account, identity and managed config config
// gives r, which must already have its key, rule and URL.
func (r *Repo) access(cfg *config.Config) {
	r.SSHKey = cfg.ResolveSSHKey(r.Key, r.Rule)
	a := cfg.AccountFor(r.Key, r.Rule)
	r.Account = ""
	if a != nil {
		r.Account = a.Name
	}
	r.accountConfig = AccountConfig(a)
	r.Clone = cfg.CloneFor(r.Key, r.Rule)
	r.Config = managedConfig(r.URL, r.SSHKey, r.accountConfig, r.Clone)
}

// Repos returns what config wants for every repo this run can judge: those
// discovery found, and those added by hand. scope tells which rules ran: a
// hand-added repo loses a rule's tags only when that rule ran and missed it.
func Repos(cfg *config.Config, st *state.State, discovered map[string]discovery.Repo, scope discovery.StaleScope) (map[string]Repo, []Blocked) {
	out := map[string]Repo{}
	var blocked []Blocked

	for key, found := range discovered {
		found := found
		applied := st.Repos[key]
		r := Repo{
			Key: key, Host: found.Host, Owner: found.Owner, Name: found.Name, FullName: found.FullName,
			ProviderID: found.ProviderID, Rule: found.DiscoveredBy, Found: &found,
		}
		if applied != nil {
			r.UserTags = applied.UserTags
		}
		if applied != nil && applied.ManuallyAdded {
			r.Pinned, r.Path, r.URL = true, applied.LocalPath, applied.CloneURL
		} else {
			path, err := TemplatePath(cfg, key, found.Host, found.Owner, found.Name, found.FullName)
			if err != nil {
				blocked = append(blocked, Blocked{Target: key, Reason: err.Error()})
				continue
			}
			r.Path = path
			r.URL = cloneURL(found, cfg.CloneProtocolFor(key, found.DiscoveredBy))
		}
		r.Tags = tagUnion(found.Tags, r.UserTags)
		r.access(cfg)
		out[key] = r
	}

	for key, applied := range st.Repos {
		if !applied.ManuallyAdded {
			continue
		}
		if _, ok := discovered[key]; ok {
			continue
		}
		r := Repo{
			Key: key, Host: applied.Host, Owner: applied.Owner, Name: applied.Name, FullName: applied.FullName,
			ProviderID: applied.ProviderID, Path: applied.LocalPath, URL: applied.CloneURL,
			UserTags: applied.UserTags, Pinned: true,
			Rule: applied.DiscoveredBy, Tags: tagUnion(applied.Tags),
		}
		if scope.Covers(applied) {
			// Its rule ran and didn't find it: only the hand-given tags remain.
			r.Rule, r.Tags = "", tagUnion(applied.UserTags)
		}
		r.access(cfg)
		out[key] = r
	}

	// Two repos must never be asked to live at one path.
	byPath := map[string][]string{}
	for key, r := range out {
		byPath[r.Path] = append(byPath[r.Path], key)
	}
	for path, keys := range byPath {
		if len(keys) < 2 {
			continue
		}
		sort.Strings(keys)
		for _, key := range keys {
			if applied := st.Repos[key]; applied != nil && applied.LocalPath == path {
				continue // already there; the others are blocked
			}
			blocked = append(blocked, Blocked{Target: key, Reason: fmt.Sprintf("path %s is also wanted by %v; fix path_template or an override", path, keys)})
			delete(out, key)
		}
	}
	sort.Slice(blocked, func(i, j int) bool { return blocked[i].Target < blocked[j].Target })
	return out, blocked
}

// HandAdded returns what is wanted for a repo being added by hand: cloned
// from id.URL, or adopted, at path.
func HandAdded(cfg *config.Config, id *repourl.Identity, path string, tags []string) Repo {
	key := id.Key()
	r := Repo{
		Key: key, Host: id.Host, Owner: id.Owner, Name: id.Name, FullName: id.FullName,
		Path: path, URL: id.URL, UserTags: tags, Tags: tagUnion(tags), Pinned: true,
	}
	r.access(cfg)
	return r
}

// TemplatePath is where config puts a repo: base_dir joined with the
// path_template, both after the repo's overrides.
func TemplatePath(cfg *config.Config, key, host, owner, name, fullName string) (string, error) {
	repoCfg := cfg.ResolveForRepo(key)
	rel, err := pathtemplate.Resolve(repoCfg.PathTemplate, pathtemplate.RepoData{
		Host: host, Owner: owner, Name: name, FullName: fullName,
	})
	if err != nil {
		return "", fmt.Errorf("path template for %s: %w", key, err)
	}
	return filepath.Join(config.ExpandHome(repoCfg.BaseDir), rel), nil
}

// cloneURL picks the URL for the resolved protocol: https uses the provider's
// HTTP(S) URL, ssh the SSH URL; a repo with only one of them uses that one.
func cloneURL(r discovery.Repo, protocol string) string {
	if protocol == "https" && r.HTTPURL != "" {
		return r.HTTPURL
	}
	if r.SSHURL != "" {
		return r.SSHURL
	}
	return r.HTTPURL
}

func tagUnion(groups ...[]string) []string {
	var out []string
	for _, g := range groups {
		for _, t := range g {
			if t != "" && !slices.Contains(out, t) {
				out = append(out, t)
			}
		}
	}
	sort.Strings(out)
	return out
}

// CopyAccess is what config gives a clone of the repo with key, found by
// rule, whose origin is url: the SSH key git-mgr fetches it with ("" for
// ssh's own choice) and the managed git config written into it.
func CopyAccess(cfg *config.Config, key, rule, url string) (sshKey string, managed map[string]string) {
	r := Repo{Key: key, Rule: rule, URL: url}
	r.access(cfg)
	if repourl.IsHTTP(url) {
		return "", r.Config
	}
	return r.SSHKey, r.Config
}
