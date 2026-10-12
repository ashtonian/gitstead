package reconcile

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"slices"

	"golang.org/x/sync/errgroup"

	"github.com/ashtonian/git-mgr/internal/gitops"
	"github.com/ashtonian/git-mgr/internal/repourl"
	"github.com/ashtonian/git-mgr/internal/state"
)

// Copy is what is at one path on disk.
type Copy struct {
	Path   string
	Exists bool   // anything is there
	IsRepo bool   // a git working copy
	Origin string // origin URL; "" if none
	CopyID string // "" if none recorded
	// CommonDir is the repository's shared git directory; linked worktrees
	// of one repository have the same one.
	CommonDir string
	Config    map[string]string // repo-local git config, by lower-case key
	Err       error             // the copy exists but could not be read
}

// Is reports whether the copy is a clone of the repo with key, judged by its
// copy ID when both have one, else by its origin URL.
func (c Copy) Is(key, copyID string) bool {
	if !c.IsRepo || c.Err != nil {
		return false
	}
	if copyID != "" && c.CopyID != "" {
		return c.CopyID == copyID
	}
	id, err := repourl.Parse(c.Origin)
	return err == nil && id.Key() == key
}

// Seen is what was observed, by path.
type Seen map[string]Copy

// At is what was seen at path; nothing exists there if it wasn't observed.
func (s Seen) At(path string) Copy {
	if c, ok := s[path]; ok {
		return c
	}
	return Copy{Path: path}
}

// Observe reads every path in paths, using up to concurrency git processes.
func Observe(ctx context.Context, paths []string, concurrency int) Seen {
	paths = slices.DeleteFunc(slices.Clone(paths), func(p string) bool { return p == "" })
	slices.Sort(paths)
	paths = slices.Compact(paths)
	copies := make([]Copy, len(paths))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(max(concurrency, 1))
	for i, p := range paths {
		g.Go(func() error {
			copies[i] = ObserveCopy(gctx, p)
			return nil
		})
	}
	_ = g.Wait()
	seen := Seen{}
	for _, c := range copies {
		seen[c.Path] = c
	}
	return seen
}

// ObserveCopy reads what is at path.
func ObserveCopy(ctx context.Context, path string) Copy {
	c := Copy{Path: path}
	if path == "" {
		return c
	}
	if _, err := os.Lstat(path); err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			c.Exists, c.Err = true, err
		}
		return c
	}
	c.Exists = true
	if !gitops.HasGitDir(path) {
		return c
	}
	c.IsRepo = true
	cfg, err := gitops.LocalConfig(ctx, path)
	if err != nil {
		c.Err = err
		return c
	}
	c.Config, c.Origin = cfg, cfg["remote.origin.url"]
	c.CopyID = gitops.ReadCopyID(ctx, path)
	c.CommonDir = gitops.ResolveGitDirs(ctx, path).CommonDir
	return c
}

// RepoPaths lists the paths a repo plan needs observed: each desired path,
// each recorded path, and the recorded path of any repo a desired one may
// have been renamed from.
func RepoPaths(desired map[string]Repo, st *state.State) []string {
	var paths []string
	for key, d := range desired {
		paths = append(paths, d.Path)
		if a := st.Repos[key]; a != nil {
			paths = append(paths, a.LocalPath)
		} else if old := renamedFrom(d, desired, st); old != "" {
			paths = append(paths, st.Repos[old].LocalPath)
		}
	}
	return paths
}
