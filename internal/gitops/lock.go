package gitops

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// ErrLocked indicates an operation was skipped because a git lock file
// (e.g. .git/index.lock) was present, signalling another git process is
// active on the repo. It is transient, not a failure.
var ErrLocked = errors.New("repo locked")

// GitDirs holds a repo's resolved git directories. For a normal repo (or the
// main worktree) GitDir and CommonDir are both <repo>/.git. For a LINKED
// worktree -- where .git is a file, not a directory -- GitDir is the
// per-worktree dir (HEAD, index, MERGE_HEAD, rebase state, index.lock) and
// CommonDir is the shared dir (config, packed-refs, refs/stash and their locks).
type GitDirs struct {
	GitDir    string
	CommonDir string
}

// ResolveGitDirs resolves a repo's per-worktree and common git directories so
// lock and in-progress-operation checks are correct for linked worktrees (where
// .git is a file pointing at .git/worktrees/<name>). When .git is a real
// directory -- the common case -- it is both dirs and no git process is spawned;
// only linked worktrees pay for the rev-parse calls. Falls back to <repo>/.git.
func ResolveGitDirs(ctx context.Context, repoPath string) GitDirs {
	dotGit := filepath.Join(repoPath, ".git")
	if info, err := os.Stat(dotGit); err == nil && info.IsDir() {
		return GitDirs{GitDir: dotGit, CommonDir: dotGit}
	}

	dirs := GitDirs{GitDir: dotGit, CommonDir: dotGit}
	gitDir, err := Git(ctx, repoPath, "rev-parse", "--absolute-git-dir")
	if err != nil || gitDir == "" {
		return dirs
	}
	dirs.GitDir = filepath.Clean(gitDir)
	dirs.CommonDir = dirs.GitDir

	commonDir, err := Git(ctx, repoPath, "rev-parse", "--git-common-dir")
	if err != nil || commonDir == "" {
		return dirs
	}
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(repoPath, commonDir)
	}
	dirs.CommonDir = filepath.Clean(commonDir)
	return dirs
}

// Lock files whose presence signals an in-progress git operation, split by
// where they live: per-worktree state vs. state shared across all worktrees.
var (
	perWorktreeLockFiles = []string{"index.lock", "HEAD.lock"}
	sharedLockFiles      = []string{"config.lock", "packed-refs.lock", "shallow.lock", filepath.Join("refs", "stash.lock")}
)

// IsLocked reports whether any git lock file is present, resolving the repo's
// real git directories first so it is correct for linked worktrees. The
// returned string is the lock file's name (e.g. "index.lock").
func IsLocked(ctx context.Context, repoPath string) (bool, string) {
	dirs := ResolveGitDirs(ctx, repoPath)
	for _, lf := range perWorktreeLockFiles {
		if _, err := os.Stat(filepath.Join(dirs.GitDir, lf)); err == nil {
			return true, lf
		}
	}
	for _, lf := range sharedLockFiles {
		if _, err := os.Stat(filepath.Join(dirs.CommonDir, lf)); err == nil {
			return true, lf
		}
	}
	return false, ""
}

// ErrBusy reports a repository a git command is working in: it holds one of
// the repository's locks, or a rebase, merge, cherry-pick or revert is under
// way. Archiving or deleting it then could catch it half-changed.
var ErrBusy = errors.New("git is busy in it")

// CheckIdle returns an error wrapping ErrBusy when git is busy in the
// repository at repoPath or in one of its checked-out submodules. A lock a
// crashed git command left behind counts too: only a person can tell it is
// stale. Without operations, in-progress rebases and the like are allowed:
// moving a whole clone keeps them.
func CheckIdle(ctx context.Context, repoPath string, operations bool) error {
	repos := []string{repoPath}
	subs, err := Submodules(ctx, repoPath)
	if err != nil {
		return err
	}
	for _, sub := range subs {
		repos = append(repos, filepath.Join(repoPath, sub))
	}
	for _, repo := range repos {
		if lock := heldLock(ctx, repo); lock != "" {
			return fmt.Errorf("%w: %s exists; if no git command is running there, delete it", ErrBusy, lock)
		}
		if !operations {
			continue
		}
		if op := operationInProgress(ResolveGitDirs(ctx, repo).GitDir); op != "" {
			return fmt.Errorf("%w: a %s is in progress in %s; finish or abort it first", ErrBusy, op, repo)
		}
	}
	return nil
}

// heldLock returns the path of a lock git holds in the repository at
// repoPath, or "": the locks IsLocked checks, a lock on any ref, and gc's.
func heldLock(ctx context.Context, repoPath string) string {
	dirs := ResolveGitDirs(ctx, repoPath)
	if locked, name := IsLocked(ctx, repoPath); locked {
		if slices.Contains(perWorktreeLockFiles, name) {
			return filepath.Join(dirs.GitDir, name)
		}
		return filepath.Join(dirs.CommonDir, name)
	}
	if gcPID := filepath.Join(dirs.CommonDir, "gc.pid"); fileExists(gcPID) {
		return gcPID
	}
	var found string
	_ = filepath.WalkDir(filepath.Join(dirs.CommonDir, "refs"), func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(path, ".lock") {
			found = path
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

// operationInProgress names the rebase, merge, cherry-pick or revert under
// way in a worktree's git dir, or "".
func operationInProgress(gitDir string) string {
	for _, op := range []struct{ marker, name string }{
		{"rebase-merge", "rebase"}, {"rebase-apply", "rebase"},
		{"MERGE_HEAD", "merge"}, {"CHERRY_PICK_HEAD", "cherry-pick"}, {"REVERT_HEAD", "revert"},
	} {
		if fileExists(filepath.Join(gitDir, op.marker)) {
			return op.name
		}
	}
	return ""
}

// IsBareRepo checks if the repo at repoPath is a bare repository.
func IsBareRepo(ctx context.Context, repoPath string) bool {
	out, err := Git(ctx, repoPath, "rev-parse", "--is-bare-repository")
	return err == nil && out == "true"
}

// HasGitDir checks if the path contains a .git directory or file.
func HasGitDir(path string) bool {
	_, err := os.Stat(filepath.Join(path, ".git"))
	return err == nil
}
