package gitops_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ashtonian/gitstead/internal/gitops"
)

// TestIsLocked_LinkedWorktree proves lock detection works in a LINKED worktree,
// where .git is a file pointing at .git/worktrees/<name>. The previous code
// hardcoded <path>/.git/index.lock, which never exists for a worktree, so locks
// were silently missed and backup/sync would run mid-operation.
func TestIsLocked_LinkedWorktree(t *testing.T) {
	ctx := context.Background()
	main := setupTestRepo(t)

	wt := filepath.Join(t.TempDir(), "wt")
	if _, err := gitops.Git(ctx, main, "worktree", "add", "-b", "wtbranch", wt); err != nil {
		t.Fatalf("worktree add: %v", err)
	}

	// Precondition for the bug: the worktree's .git is a FILE, not a directory.
	if info, err := os.Stat(filepath.Join(wt, ".git")); err != nil || info.IsDir() {
		t.Fatalf("expected linked-worktree .git to be a file (err=%v)", err)
	}

	if locked, _ := gitops.IsLocked(ctx, wt); locked {
		t.Fatal("clean worktree should not be locked")
	}

	// index.lock really lives in the per-worktree git dir.
	gitDir := gitops.ResolveGitDirs(ctx, wt).GitDir
	if err := os.WriteFile(filepath.Join(gitDir, "index.lock"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	locked, file := gitops.IsLocked(ctx, wt)
	if !locked || file != "index.lock" {
		t.Errorf("linked worktree lock not detected: locked=%v file=%q", locked, file)
	}
}

// TestGetStatus_LinkedWorktreeMidMerge proves in-progress-operation detection
// works in a linked worktree (MERGE_HEAD lives in the per-worktree git dir).
func TestGetStatus_LinkedWorktreeMidMerge(t *testing.T) {
	ctx := context.Background()
	main := setupTestRepo(t)

	wt := filepath.Join(t.TempDir(), "wt")
	if _, err := gitops.Git(ctx, main, "worktree", "add", "-b", "wt2", wt); err != nil {
		t.Fatalf("worktree add: %v", err)
	}

	gitDir := gitops.ResolveGitDirs(ctx, wt).GitDir
	if err := os.WriteFile(filepath.Join(gitDir, "MERGE_HEAD"), []byte("deadbeef\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	rs, err := gitops.GetStatus(ctx, wt)
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if !rs.InMerge {
		t.Error("expected InMerge=true for a linked worktree with MERGE_HEAD")
	}
	if rs.StatusString() != "conflict" {
		t.Errorf("expected status 'conflict', got %q", rs.StatusString())
	}
}
