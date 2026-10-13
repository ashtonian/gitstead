package gitops

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// RestoreArchive makes dest a clone holding what the archive at path -- its
// bundle or its description -- saved: branches, tags and other refs, stashes,
// HEAD, and the index, working tree and untracked files it had, with its Git
// LFS objects and its submodules' archives in their places. A complete bundle
// restores on its own. Any other archive is restored into a clone of base --
// by default the one it records -- or of its origin remote when the base is
// gone. dest must not exist, or be an empty directory. The clone is built
// beside dest and published as clones are, so a failed restore leaves
// nothing there, and whatever appears at dest meanwhile is kept.
func RestoreArchive(ctx context.Context, path, dest, base string) error {
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
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	if err := staged(dest, func(staging string) error {
		return restoreArchiveInto(ctx, meta, id, staging, base)
	}); err != nil {
		return fmt.Errorf("restoring %s: %w", id, err)
	}
	return nil
}

// restoreArchiveInto restores archive id, described by meta, into the empty
// directory dest.
func restoreArchiveInto(ctx context.Context, meta ArchiveMeta, id, dest, base string) error {
	git := func(args ...string) (string, error) { return Git(ctx, dest, args...) }
	if meta.Complete {
		if _, err := git("init", "-q"); err != nil {
			return err
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
			return fmt.Errorf("it leans on the history of %s, which is gone, and records no origin to clone instead", meta.Base)
		}
		if _, err := Git(ctx, filepath.Dir(dest), "clone", "-q", "--no-checkout", source, dest); err != nil {
			return fmt.Errorf("cloning %s, which holds the history it leans on: %w", source, err)
		}
	}
	if meta.Bundle != "" {
		if _, err := git("fetch", "-q", "--update-head-ok", meta.Bundle, "+refs/*:refs/*"); err != nil {
			return fmt.Errorf("fetching its bundle: %w", err)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(meta.Remotes)) {
		verb := "add"
		if GitExitCode(ctx, dest, "remote", "get-url", name) == 0 {
			verb = "set-url"
		}
		if _, err := git("remote", verb, name, meta.Remotes[name]); err != nil {
			return fmt.Errorf("remote %s: %w", name, err)
		}
	}
	// Before checking anything out: a checkout looks for LFS objects locally
	// first.
	if meta.LFS != "" {
		if err := linkTree(meta.LFS, filepath.Join(dest, ".git", "lfs", "objects")); err != nil {
			return fmt.Errorf("its LFS objects: %w", err)
		}
	}

	refs := archiveRefPrefix + id + "/"
	if err := restoreHead(ctx, dest, meta); err != nil {
		return err
	}
	// The snapshot is applied as a backup's is: with git stash apply, or
	// tree by tree on an unborn branch, where git stash can't.
	if snapshot, err := Git(ctx, dest, "rev-parse", "--verify", "-q", refs+"worktree"); err == nil {
		if err := applySnapshot(ctx, dest, snapshot); err != nil {
			return fmt.Errorf("its working tree: %w", err)
		}
	}
	if err := restoreStashes(ctx, dest, refs); err != nil {
		return err
	}
	deleteRefs(ctx, dest, refs)

	for _, sub := range slices.Sorted(maps.Keys(meta.Submodules)) {
		if err := RestoreArchive(ctx, meta.Submodules[sub], filepath.Join(dest, sub), ""); err != nil {
			return fmt.Errorf("submodule %s: %w", sub, err)
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
