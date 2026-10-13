package gitops

import (
	"bytes"
	"context"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// UniqueWork is local work in a repository that exists nowhere else, so
// deleting the repository would lose it.
type UniqueWork struct {
	Changed         int // tracked files with staged or unstaged changes
	Untracked       int // untracked files; ignored files don't count
	Stashes         int // stash entries
	UnpushedCommits int // commits on local branches or HEAD that no remote-tracking ref reaches
	DetachedCommits int // commits reachable only from a detached HEAD
	// OtherRefCommits counts commits that only tags or other refs of the
	// repository's own reach: no branch, HEAD, stash or remote does.
	OtherRefCommits int
	InProgress      string // "rebase", "merge", "cherry-pick" or "revert" when one is under way
	// Submodules holds, by path, the unique work of each checked-out
	// submodule that has some. A submodule's repository goes with the copy
	// that checked it out, so all of it is lost.
	Submodules map[string]UniqueWork
}

// LostWithClone reports whether deleting a whole clone would lose work.
func (u UniqueWork) LostWithClone() bool {
	return u.inWorkingTree() || u.Stashes > 0 || u.UnpushedCommits > 0 || u.OtherRefCommits > 0 || len(u.Submodules) > 0
}

// LostWithWorktree reports whether removing a linked worktree would lose work.
// Its branches, tags and stashes live in the shared repository and survive; its
// working tree, index, submodules and commits reachable only from a detached
// HEAD don't.
func (u UniqueWork) LostWithWorktree() bool {
	return u.inWorkingTree() || u.DetachedCommits > 0 || len(u.Submodules) > 0
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
	add(u.OtherRefCommits, "commit only a tag or other ref reaches", "commits only tags or other refs reach")
	if u.InProgress != "" {
		parts = append(parts, u.InProgress+" in progress")
	}
	for _, path := range slices.Sorted(maps.Keys(u.Submodules)) {
		parts = append(parts, "submodule "+path+": "+u.Submodules[path].String())
	}
	if len(parts) == 0 {
		return "no unique work"
	}
	return strings.Join(parts, ", ")
}

// InspectUniqueWork reports the unique work in the repository at repoPath and
// in its checked-out submodules. Any git failure is returned as an error: a
// caller deciding whether to delete must treat "could not tell" as unsafe.
func InspectUniqueWork(ctx context.Context, repoPath string) (UniqueWork, error) {
	u, err := inspectRepo(ctx, repoPath)
	if err != nil {
		return u, err
	}
	subs, err := Submodules(ctx, repoPath)
	if err != nil {
		return u, err
	}
	for _, sub := range subs {
		w, err := inspectRepo(ctx, filepath.Join(repoPath, sub))
		if err != nil {
			return u, fmt.Errorf("submodule %s: %w", sub, err)
		}
		if w.LostWithClone() {
			if u.Submodules == nil {
				u.Submodules = map[string]UniqueWork{}
			}
			u.Submodules[sub] = w
		}
	}
	return u, nil
}

// Submodules lists the paths of the repository's checked-out submodules,
// nested ones included, relative to it.
func Submodules(ctx context.Context, repoPath string) ([]string, error) {
	if !fileExists(filepath.Join(repoPath, ".gitmodules")) {
		return nil, nil
	}
	out, err := Git(ctx, repoPath, "submodule", "status", "--recursive")
	if err != nil {
		return nil, fmt.Errorf("listing submodules of %s: %w", repoPath, err)
	}
	var paths []string
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		// "-<sha> path" is a submodule that isn't checked out.
		if len(f) < 2 || strings.HasPrefix(line, "-") {
			continue
		}
		paths = append(paths, f[1])
	}
	return paths, nil
}

// inspectRepo reports the unique work of one repository, not its
// submodules.
func inspectRepo(ctx context.Context, repoPath string) (UniqueWork, error) {
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
	// git-mgr's own refs hold backups of work counted here already, and
	// refs/prefetch holds copies of remote branches.
	others := []string{"rev-list", "--count"}
	for _, ref := range []string{"refs/heads/*", "refs/remotes/*", "refs/stash", "refs/git-mgr/*", "refs/prefetch/*"} {
		others = append(others, "--exclude="+ref)
	}
	others = append(others, "--all", "--not", "--branches", "--remotes")
	if hasHead {
		others = append(others, "HEAD")
	}
	if u.OtherRefCommits, err = gitCount(ctx, repoPath, others...); err != nil {
		return u, err
	}
	if hasHead && GitExitCode(ctx, repoPath, "symbolic-ref", "-q", "HEAD") != 0 {
		if u.DetachedCommits, err = gitCount(ctx, repoPath, "rev-list", "--count", "HEAD", "--not", "--branches", "--remotes"); err != nil {
			return u, err
		}
	}

	u.InProgress = operationInProgress(ResolveGitDirs(ctx, repoPath).GitDir)
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
