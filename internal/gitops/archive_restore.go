package gitops

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/ashtonian/git-mgr/internal/pathutil"
)

// RestoreArchive makes dest a clone holding what the archive at path -- its
// bundle or its description -- saved: branches, tags and other refs, stashes,
// HEAD, and the index, working tree and untracked files it had, with its Git
// LFS objects and its submodules' archives in their places. A complete bundle
// restores on its own. Any other archive is restored into a clone of base --
// by default the one it records -- or of its origin remote when the base is
// gone. dest must not exist, or be an empty directory; on failure what was
// made there is removed.
func RestoreArchive(ctx context.Context, path, dest, base string) (err error) {
	meta, id, err := ReadArchive(path)
	if err != nil {
		return fmt.Errorf("reading archive %s: %w", path, err)
	}
	if meta.Clone != "" {
		return fmt.Errorf("archive %s is a whole clone, at %s: move it back instead", id, meta.Clone)
	}
	if !filepath.IsAbs(dest) {
		return fmt.Errorf("restoring %s: %q is not an absolute path", id, dest)
	}
	switch entries, rerr := os.ReadDir(dest); {
	case errors.Is(rerr, fs.ErrNotExist):
	case rerr != nil:
		return fmt.Errorf("restoring %s: %w", id, rerr)
	case len(entries) > 0:
		return fmt.Errorf("restoring %s: %s is not empty", id, dest)
	}
	defer func() {
		if err != nil {
			if rmErr := pathutil.SafeRemoveAll(dest); rmErr != nil {
				err = errors.Join(err, fmt.Errorf("removing the partial restore at %s: %w", dest, rmErr))
			}
		}
	}()
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}

	git := func(args ...string) (string, error) { return Git(ctx, dest, args...) }
	if meta.Complete {
		if _, err := Git(ctx, filepath.Dir(dest), "init", "-q", dest); err != nil {
			return fmt.Errorf("restoring %s: %w", id, err)
		}
	} else {
		source := meta.Base
		if base != "" {
			source = base
		}
		if source == "" || !HasGitDir(source) {
			source = meta.Remotes["origin"]
		}
		if source == "" {
			return fmt.Errorf("restoring %s: it leans on the history of %s, which is gone, and records no origin to clone instead", id, meta.Base)
		}
		if _, err := Git(ctx, filepath.Dir(dest), "clone", "-q", "--no-checkout", source, dest); err != nil {
			return fmt.Errorf("restoring %s: cloning %s, which holds the history it leans on: %w", id, source, err)
		}
	}
	if meta.Bundle != "" {
		if _, err := git("fetch", "-q", "--update-head-ok", meta.Bundle, "+refs/*:refs/*"); err != nil {
			return fmt.Errorf("restoring %s: fetching its bundle: %w", id, err)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(meta.Remotes)) {
		verb := "add"
		if GitExitCode(ctx, dest, "remote", "get-url", name) == 0 {
			verb = "set-url"
		}
		if _, err := git("remote", verb, name, meta.Remotes[name]); err != nil {
			return fmt.Errorf("restoring %s: remote %s: %w", id, name, err)
		}
	}
	// Before checking anything out: a checkout looks for LFS objects locally
	// first.
	if meta.LFS != "" {
		if err := linkTree(meta.LFS, filepath.Join(dest, ".git", "lfs", "objects")); err != nil {
			return fmt.Errorf("restoring %s: its LFS objects: %w", id, err)
		}
	}

	refs := archiveRefPrefix + id + "/"
	if err := restoreHead(ctx, dest, meta); err != nil {
		return fmt.Errorf("restoring %s: %w", id, err)
	}
	if GitExitCode(ctx, dest, "rev-parse", "--verify", "-q", refs+"worktree") == 0 {
		if _, err := git("stash", "apply", "-q", "--index", refs+"worktree"); err != nil {
			return fmt.Errorf("restoring %s: its working tree: %w", id, err)
		}
	}
	if err := restoreStashes(ctx, dest, refs); err != nil {
		return fmt.Errorf("restoring %s: %w", id, err)
	}
	deleteRefs(ctx, dest, refs)

	for _, sub := range slices.Sorted(maps.Keys(meta.Submodules)) {
		if err := RestoreArchive(ctx, meta.Submodules[sub], filepath.Join(dest, sub), ""); err != nil {
			return fmt.Errorf("restoring %s: submodule %s: %w", id, sub, err)
		}
	}
	return nil
}

// restoreHead points dest's HEAD where the archived repository's was -- its
// branch, or the commit it was detached at -- and checks it out.
func restoreHead(ctx context.Context, dest string, meta ArchiveMeta) error {
	if meta.Branch != "" {
		branch := "refs/heads/" + meta.Branch
		// A bundle leaves out a branch its base already holds.
		if GitExitCode(ctx, dest, "rev-parse", "--verify", "-q", branch) != 0 && meta.Head != "" {
			if _, err := Git(ctx, dest, "update-ref", branch, meta.Head); err != nil {
				return fmt.Errorf("branch %s: %w", meta.Branch, err)
			}
		}
		if _, err := Git(ctx, dest, "symbolic-ref", "HEAD", branch); err != nil {
			return fmt.Errorf("branch %s: %w", meta.Branch, err)
		}
	} else if meta.Head != "" {
		if _, err := Git(ctx, dest, "update-ref", "--no-deref", "HEAD", meta.Head); err != nil {
			return fmt.Errorf("HEAD: %w", err)
		}
	}
	if GitExitCode(ctx, dest, "rev-parse", "--verify", "-q", "HEAD") != 0 {
		return nil // an unborn branch: nothing to check out
	}
	if _, err := Git(ctx, dest, "reset", "-q", "--hard"); err != nil {
		return fmt.Errorf("checking out HEAD: %w", err)
	}
	return nil
}

// restoreStashes rebuilds dest's stash list from the archive's stash-<n>
// refs under refs, stash@{0} last so it stays the newest.
func restoreStashes(ctx context.Context, dest, refs string) error {
	var stashes []string
	for i := 0; ; i++ {
		oid, err := Git(ctx, dest, "rev-parse", "--verify", "-q", refs+"stash-"+strconv.Itoa(i))
		if err != nil {
			break
		}
		stashes = append(stashes, oid)
	}
	if len(stashes) == 0 {
		return nil
	}
	// The bundle's refs/stash has no reflog: it is rebuilt entry by entry.
	if _, err := Git(ctx, dest, "update-ref", "-d", "refs/stash"); err != nil {
		return fmt.Errorf("stashes: %w", err)
	}
	for i := len(stashes) - 1; i >= 0; i-- {
		message, err := Git(ctx, dest, "log", "-1", "--format=%s", stashes[i])
		if err != nil {
			return fmt.Errorf("stash %d: %w", i, err)
		}
		if _, err := Git(ctx, dest, "stash", "store", "-q", "-m", strings.TrimSpace(message), stashes[i]); err != nil {
			return fmt.Errorf("stash %d: %w", i, err)
		}
	}
	return nil
}
