package gitops

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// WorktreeInfo describes one entry from `git worktree list --porcelain`.
type WorktreeInfo struct {
	Path     string
	Head     string
	Branch   string // short branch name; empty if detached/bare
	Bare     bool
	Detached bool
}

// WorktreeAdd creates a new worktree of repoPath at worktreePath.
//
// If branch is non-empty: an existing branch is checked out, otherwise a new
// branch is created at baseRef (or current HEAD when baseRef is empty). With an
// empty branch the worktree is checked out detached at baseRef/HEAD.
func WorktreeAdd(ctx context.Context, repoPath, worktreePath, branch, baseRef string) error {
	args := []string{"worktree", "add"}
	switch {
	case branch == "":
		args = append(args, worktreePath)
		if baseRef != "" {
			args = append(args, baseRef)
		}
	case BranchExists(ctx, repoPath, branch):
		args = append(args, worktreePath, branch)
	default:
		args = append(args, "-b", branch, worktreePath)
		if baseRef != "" {
			args = append(args, baseRef)
		}
	}
	_, err := Git(ctx, repoPath, args...)
	return err
}

// BranchExists reports whether a local branch exists in the repo.
func BranchExists(ctx context.Context, repoPath, branch string) bool {
	return GitExitCode(ctx, repoPath, "show-ref", "--verify", "--quiet", "refs/heads/"+branch) == 0
}

// WorktreeList returns the worktrees attached to repoPath (including the main one).
func WorktreeList(ctx context.Context, repoPath string) ([]WorktreeInfo, error) {
	out, err := Git(ctx, repoPath, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	var list []WorktreeInfo
	var cur *WorktreeInfo
	flush := func() {
		if cur != nil {
			list = append(list, *cur)
			cur = nil
		}
	}
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			flush()
			cur = &WorktreeInfo{Path: strings.TrimPrefix(line, "worktree ")}
		case cur == nil:
			continue
		case strings.HasPrefix(line, "HEAD "):
			cur.Head = strings.TrimPrefix(line, "HEAD ")
		case strings.HasPrefix(line, "branch "):
			cur.Branch = strings.TrimPrefix(strings.TrimPrefix(line, "branch "), "refs/heads/")
		case line == "bare":
			cur.Bare = true
		case line == "detached":
			cur.Detached = true
		}
	}
	flush()
	return list, nil
}

// LinkedWorktrees lists the paths of the repo's linked worktrees that are on
// disk; the main worktree isn't one.
func LinkedWorktrees(ctx context.Context, repoPath string) ([]string, error) {
	list, err := WorktreeList(ctx, repoPath)
	if err != nil {
		return nil, err
	}
	var linked []string
	for i, w := range list {
		if i == 0 || w.Bare {
			continue // the main worktree comes first
		}
		if _, err := os.Stat(w.Path); err == nil {
			linked = append(linked, w.Path)
		}
	}
	return linked, nil
}

// WorktreeRemove removes a worktree. force discards a dirty or locked worktree.
func WorktreeRemove(ctx context.Context, repoPath, worktreePath string, force bool) error {
	args := []string{"worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, worktreePath)
	_, err := Git(ctx, repoPath, args...)
	return err
}

// WorktreePrune prunes stale worktree administrative entries.
func WorktreePrune(ctx context.Context, repoPath string) error {
	_, err := Git(ctx, repoPath, "worktree", "prune")
	return err
}

// Upstream returns the configured upstream of the current branch
// (e.g. "origin/main"), or "" if none is set.
func Upstream(ctx context.Context, repoPath string) string {
	out, err := Git(ctx, repoPath, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}")
	if err != nil {
		return ""
	}
	return out
}

// AheadBehind reports how many commits HEAD is ahead of and behind ref
// (typically an upstream such as "origin/main").
func AheadBehind(ctx context.Context, repoPath, ref string) (ahead, behind int, err error) {
	out, err := Git(ctx, repoPath, "rev-list", "--left-right", "--count", ref+"...HEAD")
	if err != nil {
		return 0, 0, err
	}
	fields := strings.Fields(out)
	if len(fields) != 2 {
		return 0, 0, fmt.Errorf("unexpected rev-list output: %q", out)
	}
	behind, _ = strconv.Atoi(fields[0]) // commits in ref but not HEAD
	ahead, _ = strconv.Atoi(fields[1])  // commits in HEAD but not ref
	return ahead, behind, nil
}

// WorktreeMove moves the linked worktree at from to to.
func WorktreeMove(ctx context.Context, repoPath, from, to string) error {
	_, err := Git(ctx, repoPath, "worktree", "move", from, to)
	return err
}

// WorktreeRepair reconnects the linked worktrees of the repository at
// repoPath, for instance after the repository itself was moved.
func WorktreeRepair(ctx context.Context, repoPath string) error {
	_, err := Git(ctx, repoPath, "worktree", "repair")
	return err
}
