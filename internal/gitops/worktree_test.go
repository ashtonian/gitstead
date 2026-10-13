package gitops_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ashtonian/git-mgr/internal/gitops"
)

func TestWorktreeAddListRemove(t *testing.T) {
	repo := setupTestRepo(t)
	ctx := context.Background()
	wt := filepath.Join(t.TempDir(), "agent-1")

	if err := gitops.WorktreeAdd(ctx, repo, wt, "agent-1", ""); err != nil {
		t.Fatalf("worktree add: %v", err)
	}
	if !gitops.HasGitDir(wt) {
		t.Fatal("expected the new worktree to have a .git entry")
	}

	list, err := gitops.WorktreeList(ctx, repo)
	if err != nil {
		t.Fatalf("worktree list: %v", err)
	}
	if len(list) != 2 { // main worktree + the one we added
		t.Fatalf("expected 2 worktrees, got %d: %+v", len(list), list)
	}
	found := false
	for _, w := range list {
		if w.Branch == "agent-1" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a worktree on branch agent-1, got %+v", list)
	}

	if err := gitops.WorktreeRemove(ctx, repo, wt, true); err != nil {
		t.Fatalf("worktree remove: %v", err)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Errorf("expected worktree dir to be removed")
	}
}

func TestBranchExists(t *testing.T) {
	repo := setupTestRepo(t)
	ctx := context.Background()

	cur, _ := gitops.Git(ctx, repo, "rev-parse", "--abbrev-ref", "HEAD")
	if !gitops.BranchExists(ctx, repo, cur) {
		t.Errorf("expected current branch %q to exist", cur)
	}
	if gitops.BranchExists(ctx, repo, "no-such-branch") {
		t.Error("did not expect a nonexistent branch to report as existing")
	}
}

func TestUpstream_None(t *testing.T) {
	repo := setupTestRepo(t)
	if up := gitops.Upstream(context.Background(), repo); up != "" {
		t.Errorf("expected no upstream for a fresh repo, got %q", up)
	}
}

func TestRemoveClone_KeepsAcloneWithWorktrees(t *testing.T) {
	repo := setupTestRepo(t)
	ctx := context.Background()
	wt := filepath.Join(t.TempDir(), "agent-1")
	if err := gitops.WorktreeAdd(ctx, repo, wt, "agent-1", ""); err != nil {
		t.Fatal(err)
	}
	_, err := gitops.RemoveClone(ctx, repo, t.TempDir(), "repo", gitops.RemoveOptions{Discard: true})
	if !errors.Is(err, gitops.ErrHasWorktrees) {
		t.Fatalf("RemoveClone = %v, want ErrHasWorktrees even when discarding", err)
	}
	if !gitops.HasGitDir(repo) || !gitops.HasGitDir(wt) {
		t.Fatal("the clone or its worktree was removed")
	}

	// Once its worktree is gone, it can be removed.
	if err := gitops.WorktreeRemove(ctx, repo, wt, true); err != nil {
		t.Fatal(err)
	}
	if _, err := gitops.RemoveClone(ctx, repo, t.TempDir(), "repo", gitops.RemoveOptions{Discard: true}); err != nil {
		t.Fatalf("RemoveClone without worktrees = %v", err)
	}
}
