package reconcile

import (
	"fmt"
	"path/filepath"
	"slices"
	"sort"

	"github.com/ashtonian/git-mgr/internal/config"
	"github.com/ashtonian/git-mgr/internal/discovery"
	"github.com/ashtonian/git-mgr/internal/pathtemplate"
	"github.com/ashtonian/git-mgr/internal/repourl"
	"github.com/ashtonian/git-mgr/internal/shellutil"
	"github.com/ashtonian/git-mgr/internal/state"
	"github.com/ashtonian/git-mgr/internal/workgroup"
)

// keySSHCommand is the managed git config key for a repo's SSH key, in the
// lower-case form `git config --list` reports.
const keySSHCommand = "core.sshcommand"

// Repo is what config and discovery want for one tracked repo.
type Repo struct {
	Key, Host, Owner, Name, FullName string

	ProviderID string
	Path       string
	URL        string
	SSHKey     string   // the key git-mgr uses for this repo; "" for ssh's own choice
	Tags       []string // sorted
	UserTags   []string
	Rule       string            // discovering rule; "_manual" for repos: entries
	Config     map[string]string // managed repo-local git config, by lower-case key

	// Pinned repos were added by hand: their path and URL are the recorded
	// ones, never derived from config.
	Pinned bool
	// Found is this run's discovery result; nil when discovery didn't see it.
	Found *discovery.Repo
}

// SSHCommand is the core.sshCommand git-mgr writes for a repo with a
// configured key, so your own git commands in it use that key. Host-key
// checking is left to your ssh config.
func SSHCommand(key string) string {
	return "ssh -i " + shellutil.Quote(key) + " -o IdentitiesOnly=yes"
}

func managedConfig(url, sshKey string) map[string]string {
	if sshKey == "" || repourl.IsHTTP(url) {
		return nil
	}
	return map[string]string{keySSHCommand: SSHCommand(sshKey)}
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
		r.SSHKey = cfg.ResolveSSHKey(key, r.Rule)
		r.Config = managedConfig(r.URL, r.SSHKey)
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
		r.SSHKey = cfg.ResolveSSHKey(key, r.Rule)
		r.Config = managedConfig(r.URL, r.SSHKey)
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
	r.SSHKey = cfg.ResolveSSHKey(key, "")
	r.Config = managedConfig(r.URL, r.SSHKey)
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

// Workgroup is what config wants for one workgroup.
type Workgroup struct {
	Spec     *workgroup.Spec
	Declared bool
	Track    bool              // sync: true -- track the underlying repos
	SSHKeys  map[string]string // clone member name -> SSH key
}

// Workgroups returns what is wanted for every workgroup: those declared in
// config, and those created from flags or adopted, which are kept as
// recorded. A workgroup that was declared and no longer is is absent, so the
// plan removes it.
func Workgroups(cfg *config.Config, st *state.State) (map[string]Workgroup, []Blocked) {
	out := map[string]Workgroup{}
	var blocked []Blocked
	for i := range cfg.Workgroups {
		decl := &cfg.Workgroups[i]
		spec, err := workgroup.SpecFromWorkgroup(cfg, st, decl)
		if err != nil {
			blocked = append(blocked, Blocked{Target: decl.Name, Reason: err.Error()})
			continue
		}
		out[decl.Name] = Workgroup{Spec: spec, Declared: true, Track: decl.Sync, SSHKeys: memberSSHKeys(cfg, st, spec)}
	}
	for name, ws := range st.Workgroups {
		if _, ok := out[name]; ok || ws.Declared {
			continue
		}
		spec := workgroup.FromState(ws)
		out[name] = Workgroup{Spec: spec, SSHKeys: memberSSHKeys(cfg, st, spec)}
	}
	return out, blocked
}

func memberSSHKeys(cfg *config.Config, st *state.State, spec *workgroup.Spec) map[string]string {
	keys := map[string]string{}
	for _, m := range spec.Members {
		if k := MemberSSHKey(cfg, st, m); k != "" {
			keys[m.Name] = k
		}
	}
	return keys
}

// MemberSSHKey returns the SSH key for cloning a clone member: that of the
// tracked repo it comes from, or that config gives its URL.
func MemberSSHKey(cfg *config.Config, st *state.State, m workgroup.Member) string {
	if m.RepoKey != "" {
		rule := ""
		if r, ok := st.Repos[m.RepoKey]; ok {
			rule = r.DiscoveredBy
		}
		return cfg.ResolveSSHKey(m.RepoKey, rule)
	}
	if m.RepoURL == "" || repourl.IsHTTP(m.RepoURL) {
		return ""
	}
	if id, err := repourl.Parse(m.RepoURL); err == nil {
		return cfg.ResolveSSHKey(id.Key(), "")
	}
	return ""
}
