// Package workgroup materialises and manages "workgroups": parent directories
// holding N member working directories that are operated on as a unit. A
// workgroup is either N worktrees of a single source repo (kind "worktree") or
// N independent clones (kind "clone"). Group commands treat members uniformly;
// only creation and "in sync" semantics differ by kind.
package workgroup

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ashtonian/git-mgr/internal/config"
	"github.com/ashtonian/git-mgr/internal/gitops"
	"github.com/ashtonian/git-mgr/internal/repourl"
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

// SpecFromWorkgroup resolves an arbitrary config.Workgroup into a Spec, whether
// it was declared in the config file or synthesized from CLI flags. cfg supplies
// the workgroup_base_dir default; st resolves a worktree source given as a
// tracked-repo key.
func SpecFromWorkgroup(cfg *config.Config, st *state.State, wg *config.Workgroup) (*Spec, error) {
	return resolveConfig(cfg, st, wg)
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
		if err := repourl.CheckNoCredentials(m.Repo); err != nil {
			return nil, fmt.Errorf("workgroup %q: %w", wg.Name, err)
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

// UniqueWorkOf reports the work that removing member m would lose: the whole
// clone for a clone member, the working tree and detached-HEAD commits for a
// worktree member. A member missing from disk has none.
func UniqueWorkOf(ctx context.Context, spec *Spec, m Member) (work gitops.UniqueWork, lost bool, err error) {
	if _, statErr := os.Stat(m.Path); errors.Is(statErr, fs.ErrNotExist) {
		return work, false, nil
	}
	work, err = gitops.InspectUniqueWork(ctx, m.Path)
	if err != nil {
		return work, false, err
	}
	if spec.Kind == "worktree" {
		return work, work.LostWithWorktree(), nil
	}
	return work, work.LostWithClone(), nil
}

// RemoveMember deletes member m from disk. A worktree member is removed from
// its source repo, where its branch stays; a clone member is deleted. Unless
// discard is set, work removal would lose is first saved as a git bundle in
// archiveDir, and on any failure the member is left in place. It returns the
// archive path, or "" when nothing needed saving.
func RemoveMember(ctx context.Context, spec *Spec, m Member, archiveDir string, discard bool) (string, error) {
	if _, err := os.Stat(m.Path); errors.Is(err, fs.ErrNotExist) {
		if spec.Kind == "worktree" && spec.Source != "" {
			_ = gitops.WorktreePrune(ctx, spec.Source) // forget a deleted worktree
		}
		return "", nil
	}
	if !gitops.HasGitDir(m.Path) {
		return "", fmt.Errorf("%s is not a git working directory; left in place", m.Path)
	}
	name := spec.Name + "-" + m.Name
	if spec.Kind == "worktree" && spec.Source != "" {
		return gitops.RemoveWorktree(ctx, spec.Source, m.Path, archiveDir, name, discard)
	}
	return gitops.RemoveClone(ctx, m.Path, archiveDir, name, discard)
}

// RemoveEmptyRoot deletes the workgroup's root directory if nothing is left
// in it.
func RemoveEmptyRoot(spec *Spec) {
	_ = os.Remove(spec.Root) // fails, harmlessly, unless empty
}

// TrackRepos registers the workgroup's underlying repos as tracked, by hand,
// so `git-mgr sync` keeps them fetched: the shared source for a worktree
// group, each member clone for a clone group. Repos already tracked are left
// alone. It returns how many it added and the paths it skipped because their
// origin remote could not be parsed or embeds a credential.
func TrackRepos(ctx context.Context, st *state.State, spec *Spec) (added int, skipped []string) {
	track := func(localPath string) {
		if localPath == "" {
			return
		}
		for _, r := range st.Repos {
			if r.LocalPath == localPath {
				return
			}
		}
		id, err := gitops.ParseRemoteURL(ctx, localPath)
		if err != nil || repourl.HasCredentials(id.URL) {
			skipped = append(skipped, localPath)
			return
		}
		key := id.Key()
		if _, ok := st.Repos[key]; ok {
			return
		}
		tags := []string{"workgroup:" + spec.Name}
		st.Repos[key] = &state.RepoState{
			CloneURL: id.URL, Host: id.Host, Owner: id.Owner, Name: id.Name,
			FullName: id.FullName, Provider: id.Host, LocalPath: localPath,
			CopyID: gitops.ReadCopyID(ctx, localPath), Cloned: true, ManuallyAdded: true,
			Status: "clean", Tags: tags, UserTags: tags, FirstSeen: time.Now(),
		}
		added++
	}
	if spec.Kind == "worktree" {
		track(spec.Source)
	} else {
		for _, m := range spec.Members {
			track(m.Path)
		}
	}
	return added, skipped
}

// MoveMember moves member m's directory to dest: with `git worktree move` for
// a worktree member, so its source repo follows, or a rename for a clone.
func MoveMember(ctx context.Context, spec *Spec, m Member, dest string) error {
	if _, err := os.Lstat(dest); err == nil {
		return fmt.Errorf("cannot move %s: %s already exists", m.Path, dest)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	if spec.Kind == "worktree" && spec.Source != "" {
		return gitops.WorktreeMove(ctx, spec.Source, m.Path, dest)
	}
	return os.Rename(m.Path, dest)
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
		if !e.IsDir() || gitops.IsStaging(e.Name()) {
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
