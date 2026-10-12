package gitops

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

// UniqueWork is local work in a repository that exists nowhere else, so
// deleting the repository would lose it.
type UniqueWork struct {
	Changed         int    // tracked files with staged or unstaged changes
	Untracked       int    // untracked files; ignored files don't count
	Stashes         int    // stash entries
	UnpushedCommits int    // commits on local branches or HEAD that no remote-tracking ref reaches
	DetachedCommits int    // commits reachable only from a detached HEAD
	InProgress      string // "rebase", "merge", "cherry-pick" or "revert" when one is under way
}

// LostWithClone reports whether deleting a whole clone would lose work.
func (u UniqueWork) LostWithClone() bool {
	return u.inWorkingTree() || u.Stashes > 0 || u.UnpushedCommits > 0
}

// LostWithWorktree reports whether removing a linked worktree would lose work.
// Its branches and stashes live in the shared repository and survive; its
// working tree, index and commits reachable only from a detached HEAD don't.
func (u UniqueWork) LostWithWorktree() bool {
	return u.inWorkingTree() || u.DetachedCommits > 0
}

func (u UniqueWork) inWorkingTree() bool {
	return u.Changed > 0 || u.Untracked > 0 || u.InProgress != ""
}

func (u UniqueWork) String() string {
	var parts []string
	add := func(n int, one, many string) {
		switch {
		case n == 1:
			parts = append(parts, "1 "+one)
		case n > 1:
			parts = append(parts, strconv.Itoa(n)+" "+many)
		}
	}
	add(u.Changed, "changed file", "changed files")
	add(u.Untracked, "untracked file", "untracked files")
	add(u.Stashes, "stash", "stashes")
	add(u.UnpushedCommits, "unpushed commit", "unpushed commits")
	add(u.DetachedCommits, "commit on a detached HEAD", "commits on a detached HEAD")
	if u.InProgress != "" {
		parts = append(parts, u.InProgress+" in progress")
	}
	if len(parts) == 0 {
		return "no unique work"
	}
	return strings.Join(parts, ", ")
}

// InspectUniqueWork reports the unique work in the repository at repoPath. Any
// git failure is returned as an error: a caller deciding whether to delete must
// treat "could not tell" as unsafe.
func InspectUniqueWork(ctx context.Context, repoPath string) (UniqueWork, error) {
	var u UniqueWork

	status, err := GitOutput(ctx, repoPath, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return u, fmt.Errorf("reading status of %s: %w", repoPath, err)
	}
	u.Changed, u.Untracked = countStatus(status)

	if GitExitCode(ctx, repoPath, "rev-parse", "--verify", "-q", "refs/stash") == 0 {
		out, err := Git(ctx, repoPath, "rev-list", "--walk-reflogs", "refs/stash")
		if err != nil {
			return u, fmt.Errorf("listing stashes of %s: %w", repoPath, err)
		}
		u.Stashes = countLines(out)
	}

	hasHead := GitExitCode(ctx, repoPath, "rev-parse", "--verify", "-q", "HEAD") == 0
	unpushed := []string{"rev-list", "--count", "--branches"}
	if hasHead {
		unpushed = append(unpushed, "HEAD")
	}
	if u.UnpushedCommits, err = gitCount(ctx, repoPath, append(unpushed, "--not", "--remotes")...); err != nil {
		return u, err
	}
	if hasHead && GitExitCode(ctx, repoPath, "symbolic-ref", "-q", "HEAD") != 0 {
		if u.DetachedCommits, err = gitCount(ctx, repoPath, "rev-list", "--count", "HEAD", "--not", "--branches", "--remotes"); err != nil {
			return u, err
		}
	}

	gitDir := ResolveGitDirs(ctx, repoPath).GitDir
	for _, op := range []struct{ marker, name string }{
		{"rebase-merge", "rebase"}, {"rebase-apply", "rebase"},
		{"MERGE_HEAD", "merge"}, {"CHERRY_PICK_HEAD", "cherry-pick"}, {"REVERT_HEAD", "revert"},
	} {
		if fileExists(filepath.Join(gitDir, op.marker)) {
			u.InProgress = op.name
			break
		}
	}
	return u, nil
}

// countStatus counts changed and untracked entries in `git status
// --porcelain=v1 -z` output. A rename or copy entry is followed by a separate
// field holding its original path, which is not an entry of its own.
func countStatus(out []byte) (changed, untracked int) {
	fields := bytes.Split(bytes.TrimSuffix(out, []byte{0}), []byte{0})
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		if len(f) < 3 {
			continue
		}
		if f[0] == '?' && f[1] == '?' {
			untracked++
			continue
		}
		changed++
		if f[0] == 'R' || f[0] == 'C' {
			i++
		}
	}
	return changed, untracked
}

func gitCount(ctx context.Context, repoPath string, args ...string) (int, error) {
	out, err := Git(ctx, repoPath, args...)
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(out)
	if err != nil {
		return 0, fmt.Errorf("git %s: unexpected output %q", strings.Join(args, " "), out)
	}
	return n, nil
}

func countLines(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}
