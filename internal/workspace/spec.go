// Package workspace is git-mgr's workspaces subsystem: repos worked on by
// several agents at once, each in a replica of its own. A replica holds the
// agent's copy of each of the workspace's repos -- a linked worktree of the
// repo's tracked clone by default, or a clone taking its objects from it --
// on a branch of the agent's own; with shared isolation every agent works in
// one folder.
//
// It depends on the inventory (tracked repos, reconcile's plan engine) and
// never the reverse: a workspace names its repos by their tracked keys, and
// its changes are planned and applied as serial reconcile actions, after the
// repos they are made from.
package workspace

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/ashtonian/git-mgr/internal/config"
	"github.com/ashtonian/git-mgr/internal/hook"
	"github.com/ashtonian/git-mgr/internal/reconcile"
	"github.com/ashtonian/git-mgr/internal/repourl"
	"github.com/ashtonian/git-mgr/internal/state"
)

// Spec is a workspace as config wants it, or as state records it.
type Spec struct {
	Name      string
	Dir       string
	Isolation string
	BaseRef   string
	Declared  bool
	// Folder is the folder a workspace over a path shares; "" otherwise.
	Folder   string
	Replicas []Replica
	// Settings are what config says of its agents' commands.
	Settings AgentSettings
}

// Replica is one agent's folder.
type Replica struct {
	Agent  string
	Path   string
	Copies []Copy
}

// Copy is one checkout of a repo in a replica.
type Copy struct {
	// Repo is the tracked repo's key; "" for a copy of a repo git-mgr
	// doesn't track.
	Repo string
	// Source is the path of the repo's tracked clone, which copies are made
	// from; "" when Repo is.
	Source string
	// URL is the repo's upstream, a clone's origin.
	URL    string
	Path   string
	Branch string
	// SSHKey is the key git-mgr fetches and pushes with; Config the git
	// config it manages in a clone (worktrees share their repo's).
	SSHKey string
	Config map[string]string
	// Clone is the repo's clone config: how much of it a copy checks out,
	// and how its submodules are cloned.
	Clone config.CloneConfig
	// PostCreate is run in a new copy: the repo's post_create hooks, then the
	// workspace's.
	PostCreate []string
}

// Shared reports whether every agent of the workspace works in one folder.
func (s *Spec) Shared() bool { return s.Isolation == config.IsolationShared }

// Copies lists every copy of every replica.
func (s *Spec) Copies() []Located {
	var out []Located
	for _, r := range s.Replicas {
		for _, c := range r.Copies {
			out = append(out, Located{Workspace: s.Name, Agent: r.Agent, Copy: c})
		}
	}
	return out
}

// Located is a copy with the workspace and agent it belongs to.
type Located struct {
	Workspace, Agent string
	Copy
}

// Label names the copy for people: workspace/agent, plus its folder when its
// replica holds several; just the workspace for a shared folder.
func (l Located) Label(spec *Spec) string {
	if l.Agent == "" {
		return l.Workspace
	}
	for _, r := range spec.Replicas {
		if r.Agent == l.Agent {
			return copyTarget(l.Workspace, l.Agent, r.Path, l.Path)
		}
	}
	return l.Workspace + "/" + l.Agent
}

// LabelOf names a recorded copy for people, as Located.Label does.
func LabelOf(ws *state.WorkspaceState, agent, path string) string {
	if r := ws.Replica(agent); r != nil {
		return copyTarget(ws.Name, agent, r.Path, path)
	}
	return ws.Name + "/" + agent
}

// Resolve returns the declared workspace decl as config wants it. st must
// track every repo decl names, as Compute arranges.
func Resolve(cfg *config.Config, st *state.State, decl config.Workspace) (*Spec, error) {
	w := cfg.WithDefaults(decl)
	agents, err := w.AgentNames()
	if err != nil {
		return nil, err
	}
	spec := &Spec{Name: w.Name, Dir: w.Dir(), Isolation: w.Isolation, BaseRef: w.BaseRef, Declared: true, Settings: SettingsFor(cfg, w.Name)}

	if w.Path != "" {
		spec.Folder, spec.Dir = w.Path, w.Path
		for _, a := range agents {
			spec.Replicas = append(spec.Replicas, Replica{Agent: a, Path: w.Path})
		}
		return spec, nil
	}
	keys, err := RepoKeys(cfg, st, decl)
	if err != nil {
		return nil, err
	}
	if spec.Shared() {
		spec.Dir = st.Repos[keys[0]].LocalPath
		for _, a := range agents {
			spec.Replicas = append(spec.Replicas, Replica{Agent: a, Path: spec.Dir})
		}
		return spec, nil
	}

	if spec.Isolation != config.IsolationWorktree {
		for _, key := range keys {
			if cfg.CloneFor(key, st.Repos[key].DiscoveredBy).Filter != "" {
				return nil, fmt.Errorf("%s is cloned with clone.filter, so only worktree copies can be made from it", key)
			}
		}
	}
	dirs := copyDirs(keys)
	for n, agent := range agents {
		r := Replica{Agent: agent, Path: filepath.Join(spec.Dir, agent)}
		for _, key := range keys {
			repo := st.Repos[key]
			branch, err := w.BranchFor(agent, n+1, repo.Name)
			if err != nil {
				return nil, err
			}
			c := Copy{Repo: key, Source: repo.LocalPath, URL: repo.CloneURL, Path: r.Path, Branch: branch}
			if len(keys) > 1 {
				c.Path = filepath.Join(r.Path, dirs[key])
			}
			c.SSHKey, c.Config = reconcile.CopyAccess(cfg, key, repo.DiscoveredBy, repo.CloneURL)
			c.Clone = cfg.CloneFor(key, repo.DiscoveredBy)
			c.PostCreate = postCreate(cfg, key, repo.DiscoveredBy, w)
			if spec.Isolation == config.IsolationWorktree {
				c.Config = nil // worktrees share their repo's
			}
			r.Copies = append(r.Copies, c)
		}
		spec.Replicas = append(spec.Replicas, r)
	}
	return spec, nil
}

// FromState returns a recorded workspace as it is, with the access, clone
// config and hooks config gives its copies of tracked repos.
func FromState(cfg *config.Config, st *state.State, ws *state.WorkspaceState) *Spec {
	spec := &Spec{Name: ws.Name, Dir: ws.Dir, Isolation: ws.Isolation, BaseRef: ws.BaseRef, Declared: ws.Declared}
	var decl config.Workspace
	if cfg != nil {
		spec.Settings = SettingsFor(cfg, ws.Name)
		decl = config.Workspace{Name: ws.Name}
		if d := cfg.FindWorkspace(ws.Name); d != nil {
			decl = *d
		}
		decl = cfg.WithDefaults(decl)
	}
	for _, rs := range ws.Replicas {
		r := Replica{Agent: rs.Agent, Path: rs.Path}
		for _, cs := range rs.Copies {
			c := Copy{Repo: cs.Repo, URL: cs.URL, Path: cs.Path, Branch: cs.Branch}
			if repo := st.Repos[cs.Repo]; repo != nil {
				c.Source = repo.LocalPath
				if c.URL == "" {
					c.URL = repo.CloneURL
				}
				if cfg != nil {
					c.SSHKey, c.Config = reconcile.CopyAccess(cfg, cs.Repo, repo.DiscoveredBy, c.URL)
					c.Clone = cfg.CloneFor(cs.Repo, repo.DiscoveredBy)
					c.PostCreate = postCreate(cfg, cs.Repo, repo.DiscoveredBy, decl)
					if spec.Isolation == config.IsolationWorktree {
						c.Config = nil // worktrees share their repo's
					}
				}
			}
			r.Copies = append(r.Copies, c)
		}
		spec.Replicas = append(spec.Replicas, r)
	}
	return spec
}

// postCreate is what runs in a new copy of repo key in workspace w, which
// has its defaults: the repo's post_create hooks, then the workspace's.
func postCreate(cfg *config.Config, key, rule string, w config.Workspace) []string {
	commands := slices.Clone(cfg.HooksFor(key, rule).PostCreate)
	return append(commands, w.PostCreate()...)
}

// PostCreateHook is the post_create hook of copy c of agent's replica in
// workspace ws, whose commands run with the agent's environment, vars.
func PostCreateHook(ws, agent string, c Copy, vars []string) hook.Hook {
	name := ws + "/" + agent
	if c.Repo != "" {
		name += "/" + c.Repo
	}
	return hook.Hook{
		Event: hook.PostCreate, Commands: c.PostCreate, Dir: c.Path, Name: name,
		Env:  hook.Env{Repo: c.Repo, Source: c.Source, Workspace: ws, Agent: agent, Branch: c.Branch},
		Vars: vars,
	}
}

// ErrNoRepos reports a workspace whose repos select no tracked repo.
var ErrNoRepos = errors.New("selects no tracked repo")

// RepoKeys returns the tracked repos a declared workspace names, in the
// order it names them: tracked keys, globs over them, local paths and URLs
// of tracked repos, or the repos of a named set.
func RepoKeys(cfg *config.Config, st *state.State, decl config.Workspace) ([]string, error) {
	var keys []string
	add := func(key string) {
		if !slices.Contains(keys, key) {
			keys = append(keys, key)
		}
	}
	if decl.Set != "" {
		set, ok := cfg.FindSet(decl.Set)
		if !ok {
			return nil, fmt.Errorf("unknown set %q", decl.Set)
		}
		for _, key := range slices.Sorted(maps.Keys(st.Repos)) {
			r := st.Repos[key]
			if set.Matches(key, r.HasTag, r.IsArchived) {
				add(key)
			}
		}
		if len(keys) == 0 {
			return nil, fmt.Errorf("set %q %w", decl.Set, ErrNoRepos)
		}
		return keys, nil
	}
	for _, entry := range decl.Repos {
		switch ref := ParseRef(entry); ref.Kind {
		case RefGlob:
			var matched []string
			for key := range st.Repos {
				if m, _ := filepath.Match(entry, key); m {
					matched = append(matched, key)
				}
			}
			if len(matched) == 0 {
				return nil, fmt.Errorf("%s %w", entry, ErrNoRepos)
			}
			sort.Strings(matched)
			for _, key := range matched {
				add(key)
			}
		case RefPath:
			key := trackedAt(st, ref.Path)
			if key == "" {
				return nil, fmt.Errorf("%s is not a tracked repo", ref.Path)
			}
			add(key)
		default:
			if st.Repos[ref.Key] == nil {
				return nil, fmt.Errorf("%s is not a tracked repo; give its URL or local path and git-mgr tracks it", entry)
			}
			add(ref.Key)
		}
	}
	return keys, nil
}

// RefKind is what a workspace's repos entry names.
type RefKind int

const (
	RefKey  RefKind = iota // a tracked repo key: host/owner/name
	RefGlob                // a glob over tracked keys
	RefPath                // a local path
	RefURL                 // a clone URL
)

// Ref is a parsed repos entry.
type Ref struct {
	Kind RefKind
	Key  string            // RefKey and RefURL
	Path string            // RefPath: absolute
	ID   *repourl.Identity // RefURL
}

// ParseRef classifies a workspace's repos entry.
func ParseRef(entry string) Ref {
	switch {
	case strings.HasPrefix(entry, "/") || strings.HasPrefix(entry, "~") || strings.HasPrefix(entry, "."):
		p := config.ExpandHome(entry)
		if abs, err := filepath.Abs(p); err == nil {
			p = abs
		}
		return Ref{Kind: RefPath, Path: filepath.Clean(p)}
	case strings.ContainsAny(entry, "*?["):
		return Ref{Kind: RefGlob}
	case strings.Contains(entry, "://") || strings.Contains(entry, "@") && strings.Contains(entry, ":"):
		if id, err := repourl.Parse(entry); err == nil {
			return Ref{Kind: RefURL, Key: id.Key(), ID: id}
		}
	}
	return Ref{Kind: RefKey, Key: strings.TrimSuffix(entry, ".git")}
}

// trackedAt returns the key of the tracked repo whose clone is at path.
func trackedAt(st *state.State, path string) string {
	want := realPath(path)
	for key, r := range st.Repos {
		if r.LocalPath != "" && (r.LocalPath == path || realPath(r.LocalPath) == want) {
			return key
		}
	}
	return ""
}

func realPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

// copyDirs names each repo's copy in a replica holding several: by name,
// or by as much of its key as tells it apart from the others.
func copyDirs(keys []string) map[string]string {
	dirs := map[string]string{}
	for depth := 1; len(dirs) < len(keys); depth++ {
		count := map[string]int{}
		for _, key := range keys {
			count[suffix(key, depth)]++
		}
		for _, key := range keys {
			if _, done := dirs[key]; !done && (count[suffix(key, depth)] == 1 || depth >= strings.Count(key, "/")+1) {
				dirs[key] = suffix(key, depth)
			}
		}
	}
	return dirs
}

// suffix is the last n segments of a repo key, as a relative path.
func suffix(key string, n int) string {
	parts := strings.Split(key, "/")
	if n > len(parts) {
		n = len(parts)
	}
	return filepath.Join(parts[len(parts)-n:]...)
}

// exists reports whether something is at path.
func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}
