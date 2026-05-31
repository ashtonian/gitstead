// Package workgroup materialises and manages "workgroups": parent directories
// holding N member working directories that are operated on as a unit. A
// workgroup is either N worktrees of a single source repo (kind "worktree") or
// N independent clones (kind "clone"). Group commands treat members uniformly;
// only creation and "in sync" semantics differ by kind.
package workgroup

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ashtonian/git-mgr/internal/config"
	"github.com/ashtonian/git-mgr/internal/gitops"
	"github.com/ashtonian/git-mgr/internal/state"
)

// Member is one resolved working directory within a workgroup.
type Member struct {
	Name    string
	Path    string
	Branch  string // worktree kind: branch to create/check out
	RepoURL string // clone kind: URL to clone
	RepoKey string // clone kind: originating tracked-repo key, if any (for ssh-key resolution)
}

// Spec is a fully resolved workgroup with member paths computed.
type Spec struct {
	Name    string
	Kind    string // "worktree" | "clone"
	Root    string // absolute parent directory
	Source  string // worktree kind: absolute path to the source repo
	BaseRef string // worktree kind: fork point (empty -> source HEAD)
	Members []Member
}

// FromConfig resolves a workgroup declared in config into a Spec.
func FromConfig(cfg *config.Config, st *state.State, name string) (*Spec, error) {
	for i := range cfg.Workgroups {
		if cfg.Workgroups[i].Name == name {
			return resolveConfig(cfg, st, &cfg.Workgroups[i])
		}
	}
	return nil, fmt.Errorf("workgroup %q is not declared in config", name)
}

func resolveConfig(cfg *config.Config, st *state.State, wg *config.Workgroup) (*Spec, error) {
	root := wg.BaseDir
	if root == "" {
		root = filepath.Join(cfg.WorkgroupBaseDir, wg.Name)
	}
	spec := &Spec{Name: wg.Name, Kind: wg.Kind, Root: root, BaseRef: wg.BaseRef}

	switch wg.Kind {
	case "worktree":
		src, err := resolveSourcePath(st, wg.Source)
		if err != nil {
			return nil, fmt.Errorf("workgroup %q: %w", wg.Name, err)
		}
		spec.Source = src
		if len(wg.Members) == 0 {
			return nil, fmt.Errorf("workgroup %q: worktree kind requires members", wg.Name)
		}
		for _, m := range wg.Members {
			name, branch := m.Name, m.Branch
			if name == "" {
				name = branch
			}
			if branch == "" {
				branch = name
			}
			if name == "" {
				return nil, fmt.Errorf("workgroup %q: each member needs a name or branch", wg.Name)
			}
			spec.Members = append(spec.Members, Member{
				Name:   name,
				Path:   filepath.Join(root, name),
				Branch: branch,
			})
		}
	case "clone":
		members, err := resolveCloneMembers(st, wg, root)
		if err != nil {
			return nil, err
		}
		spec.Members = members
	default:
		return nil, fmt.Errorf("workgroup %q: unknown kind %q", wg.Name, wg.Kind)
	}
	return spec, nil
}

func resolveSourcePath(st *state.State, source string) (string, error) {
	if source == "" {
		return "", fmt.Errorf("worktree kind requires 'source'")
	}
	if r, ok := st.Repos[source]; ok && r.LocalPath != "" {
		return r.LocalPath, nil
	}
	p := config.ExpandHome(source)
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	if !gitops.HasGitDir(p) {
		return "", fmt.Errorf("source %q is neither a tracked repo key nor a git repo path", source)
	}
	return p, nil
}

func resolveCloneMembers(st *state.State, wg *config.Workgroup, root string) ([]Member, error) {
	var members []Member
	if wg.FromTag != "" {
		var keys []string
		for key, r := range st.Repos {
			if r.HasTag(wg.FromTag) {
				keys = append(keys, key)
			}
		}
		sort.Strings(keys)
		for _, key := range keys {
			r := st.Repos[key]
			members = append(members, Member{
				Name:    r.Name,
				Path:    filepath.Join(root, r.Name),
				RepoURL: r.CloneURL,
				RepoKey: key,
			})
		}
		if len(members) == 0 {
			return nil, fmt.Errorf("workgroup %q: no tracked repos have tag %q", wg.Name, wg.FromTag)
		}
		return members, nil
	}
	for _, m := range wg.Members {
		if m.Repo == "" {
			return nil, fmt.Errorf("workgroup %q: each clone member needs 'repo'", wg.Name)
		}
		url, name, key := m.Repo, m.Name, ""
		if r, ok := st.Repos[m.Repo]; ok {
			url, key = r.CloneURL, m.Repo
			if name == "" {
				name = r.Name
			}
		}
		if name == "" {
			name = repoNameFromURL(url)
		}
		members = append(members, Member{Name: name, Path: filepath.Join(root, name), RepoURL: url, RepoKey: key})
	}
	return members, nil
}

func repoNameFromURL(url string) string {
	url = strings.TrimSuffix(url, ".git")
	if i := strings.LastIndexAny(url, "/:"); i >= 0 {
		return url[i+1:]
	}
	return url
}

// FromState resolves a workgroup recorded in state into a Spec.
func FromState(ws *state.WorkgroupState) *Spec {
	spec := &Spec{Name: ws.Name, Kind: ws.Kind, Root: ws.Root, Source: ws.Source, BaseRef: ws.BaseRef}
	for _, m := range ws.Members {
		spec.Members = append(spec.Members, Member{
			Name: m.Name, Path: m.Path, Branch: m.Branch, RepoURL: m.RepoURL,
			RepoKey: m.RepoKey,
		})
	}
	return spec
}

// ToState converts a Spec into a persistable WorkgroupState.
func (s *Spec) ToState(now time.Time) *state.WorkgroupState {
	ws := &state.WorkgroupState{
		Name: s.Name, Kind: s.Kind, Root: s.Root, Source: s.Source, BaseRef: s.BaseRef, CreatedAt: now,
	}
	for _, m := range s.Members {
		ws.Members = append(ws.Members, state.WorkgroupMemberState{
			Name: m.Name, Path: m.Path, Branch: m.Branch, RepoURL: m.RepoURL,
			RepoKey: m.RepoKey,
		})
	}
	return ws
}

// MemberStatus reports the outcome of ensuring one member exists.
type MemberStatus struct {
	Member
	Existed bool
	Created bool
	Err     error
}

// Ensure materialises every member that does not yet exist on disk.
// sshKeyFor resolves the SSH key for a clone member (may be nil).
func Ensure(ctx context.Context, spec *Spec, sshKeyFor func(Member) string) ([]MemberStatus, error) {
	if err := os.MkdirAll(spec.Root, 0755); err != nil {
		return nil, fmt.Errorf("creating workgroup root %s: %w", spec.Root, err)
	}
	// Clear stale worktree registrations so a member whose directory was
	// deleted can be recreated (git refuses to re-add a registered path).
	if spec.Kind == "worktree" && spec.Source != "" {
		_ = gitops.WorktreePrune(ctx, spec.Source)
	}
	var out []MemberStatus
	for _, m := range spec.Members {
		ms := MemberStatus{Member: m}
		if gitops.HasGitDir(m.Path) {
			ms.Existed = true
			out = append(out, ms)
			continue
		}
		switch spec.Kind {
		case "worktree":
			ms.Err = gitops.WorktreeAdd(ctx, spec.Source, m.Path, m.Branch, spec.BaseRef)
		case "clone":
			key := ""
			if sshKeyFor != nil {
				key = sshKeyFor(m)
			}
			ms.Err = gitops.Clone(ctx, m.RepoURL, m.Path, key)
		default:
			ms.Err = fmt.Errorf("unknown kind %q", spec.Kind)
		}
		ms.Created = ms.Err == nil
		out = append(out, ms)
	}
	return out, nil
}

// Remove tears down a workgroup's members. For worktree kind it detaches each
// worktree from the source repo; deleteFiles also removes member directories.
// It returns human-readable action lines.
func Remove(ctx context.Context, spec *Spec, deleteFiles bool) []string {
	var actions []string
	for _, m := range spec.Members {
		if spec.Kind == "worktree" && spec.Source != "" && gitops.HasGitDir(m.Path) {
			if err := gitops.WorktreeRemove(ctx, spec.Source, m.Path, true); err != nil {
				actions = append(actions, fmt.Sprintf("%s: worktree remove failed: %v", m.Name, err))
			} else {
				actions = append(actions, fmt.Sprintf("%s: worktree removed", m.Name))
				continue
			}
		}
		if deleteFiles {
			if err := safeRemove(m.Path); err != nil {
				actions = append(actions, fmt.Sprintf("%s: delete failed: %v", m.Name, err))
			} else {
				actions = append(actions, fmt.Sprintf("%s: deleted %s", m.Name, m.Path))
			}
		}
	}
	if spec.Kind == "worktree" && spec.Source != "" {
		_ = gitops.WorktreePrune(ctx, spec.Source)
	}
	if deleteFiles {
		_ = os.Remove(spec.Root) // succeeds only if now empty
	}
	return actions
}

// ScanAdopt builds a clone-kind Spec from the immediate git subdirectories of dir.
func ScanAdopt(name, dir string) (*Spec, error) {
	dir = config.ExpandHome(dir)
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return nil, fmt.Errorf("scanning %s: %w", abs, err)
	}
	spec := &Spec{Name: name, Kind: "clone", Root: abs}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		p := filepath.Join(abs, e.Name())
		if gitops.HasGitDir(p) {
			spec.Members = append(spec.Members, Member{Name: e.Name(), Path: p})
		}
	}
	if len(spec.Members) == 0 {
		return nil, fmt.Errorf("no git repositories found under %s", abs)
	}
	return spec, nil
}

// safeRemove refuses to delete obviously dangerous paths.
func safeRemove(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if abs == "/" || abs == os.Getenv("HOME") || strings.Count(abs, string(os.PathSeparator)) < 3 {
		return fmt.Errorf("refusing to delete unsafe path: %s", abs)
	}
	return os.RemoveAll(abs)
}
