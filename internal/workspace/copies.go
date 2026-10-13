package workspace

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/ashtonian/git-mgr/internal/config"
	"github.com/ashtonian/git-mgr/internal/gitops"
	"github.com/ashtonian/git-mgr/internal/reconcile"
)

// ErrUnknownSource reports a copy whose repo git-mgr doesn't track and whose
// source can't be read from disk.
var ErrUnknownSource = errors.New("its repo isn't tracked, so git-mgr doesn't know what to make it from")

// CreateCopy makes copy c at its path from its source, by isolation: a
// linked worktree, or a clone taking its objects from the source. It checks
// out as much of the repo as the repo's clone config asks for. A copy whose
// submodules can't all be checked out is made otherwise, and a
// *gitops.SubmoduleError says so.
func CreateCopy(ctx context.Context, isolation, baseRef string, c Copy) error {
	if c.Source == "" {
		return ErrUnknownSource
	}
	if !gitops.HasGitDir(c.Source) {
		return fmt.Errorf("the repo's clone at %s is missing; git-mgr sync clones it again", c.Source)
	}
	opts := reconcile.CloneOptions(c.SSHKey, c.Clone)
	var made error
	switch isolation {
	case config.IsolationWorktree:
		// A worktree deleted by hand stays registered and blocks its path.
		_ = gitops.WorktreePrune(ctx, c.Source)
		made = gitops.AddWorktree(ctx, c.Source, c.Path, c.Branch, baseRef, opts)
	case config.IsolationClone, config.IsolationReferenceClone:
		made = gitops.CloneFromSource(ctx, c.Source, c.Path, c.URL, c.Branch, baseRef, isolation == config.IsolationReferenceClone, opts)
	default:
		return fmt.Errorf("isolation %s makes no copies", isolation)
	}
	if submodules := (*gitops.SubmoduleError)(nil); made != nil && !errors.As(made, &submodules) {
		return made
	}
	if _, err := gitops.CopyID(ctx, c.Path); err != nil {
		return err
	}
	return made
}

// SourceOf returns where copy c's repo is: its tracked clone, else what the
// copy on disk says it was made from.
func SourceOf(ctx context.Context, c Copy) string {
	if c.Source != "" {
		return c.Source
	}
	return gitops.Source(ctx, c.Path)
}

// UniqueWorkOf reports the work removing copy c would lose: a worktree's
// working tree and detached commits (its branch stays in the repo), all of a
// clone's unique work. A copy missing from disk has none.
func UniqueWorkOf(ctx context.Context, isolation string, c Copy) (work gitops.UniqueWork, lost bool, err error) {
	if _, statErr := os.Lstat(c.Path); errors.Is(statErr, fs.ErrNotExist) {
		return work, false, nil
	}
	work, err = gitops.InspectUniqueWork(ctx, c.Path)
	if err != nil {
		return work, false, err
	}
	if isolation == config.IsolationWorktree {
		return work, work.LostWithWorktree(), nil
	}
	return work, work.LostWithClone(), nil
}

// RemoveCopy deletes copy c from disk: a worktree from its repo, where its
// branch stays, or a whole clone. Unless discard is set, work removal would
// lose is first saved as a git bundle in archiveDir named name, and on any
// failure the copy is left in place. It returns the archive path, or "" when
// nothing needed saving.
func RemoveCopy(ctx context.Context, isolation string, c Copy, archiveDir, name string, discard bool) (string, error) {
	if _, err := os.Lstat(c.Path); errors.Is(err, fs.ErrNotExist) {
		if isolation == config.IsolationWorktree && c.Source != "" {
			_ = gitops.WorktreePrune(ctx, c.Source) // forget a deleted worktree
		}
		return "", nil
	}
	if !gitops.HasGitDir(c.Path) {
		return "", fmt.Errorf("%s is not a git working copy; left in place", c.Path)
	}
	if isolation == config.IsolationWorktree {
		source := SourceOf(ctx, c)
		if source == "" {
			return "", fmt.Errorf("%s: %w", c.Path, ErrUnknownSource)
		}
		return gitops.RemoveWorktree(ctx, source, c.Path, archiveDir, name, gitops.RemoveOptions{Discard: discard, Repo: c.Repo})
	}
	// The repo the copy was made from keeps what the copy shares with its
	// remotes, so its bundle needn't hold that again.
	base := SourceOf(ctx, c)
	if !gitops.HasGitDir(base) {
		base = ""
	}
	return gitops.RemoveClone(ctx, c.Path, archiveDir, name, gitops.RemoveOptions{Discard: discard, Base: base, Repo: c.Repo})
}

// MoveCopy moves copy c to dest: with `git worktree move` for a worktree, so
// its repo follows, or a rename for a clone.
func MoveCopy(ctx context.Context, isolation string, c Copy, dest string) error {
	if exists(dest) {
		return fmt.Errorf("cannot move %s: %s already exists", c.Path, dest)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	if isolation == config.IsolationWorktree {
		source := SourceOf(ctx, c)
		if source == "" {
			return fmt.Errorf("%s: %w", c.Path, ErrUnknownSource)
		}
		return gitops.WorktreeMove(ctx, source, c.Path, dest)
	}
	return os.Rename(c.Path, dest)
}
