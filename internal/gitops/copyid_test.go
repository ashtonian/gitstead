package gitops_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/ashtonian/gitten/internal/gitops"
)

func TestCopyID_StablePerCopyAndFollowsAMove(t *testing.T) {
	ctx := context.Background()
	main := setupTestRepo(t)
	wt := filepath.Join(t.TempDir(), "agent-1")
	mustGit(t, main, "worktree", "add", "-q", "-b", "agent-1", wt)

	mainID := copyID(t, main)
	wtID := copyID(t, wt)
	if mainID == wtID {
		t.Fatal("a worktree must not share its repo's copy ID")
	}
	if again := copyID(t, main); again != mainID {
		t.Errorf("copy ID changed: %s then %s", mainID, again)
	}

	moved := filepath.Join(t.TempDir(), "agent-1-moved")
	mustGit(t, main, "worktree", "move", wt, moved)
	if got := copyID(t, moved); got != wtID {
		t.Errorf("moved worktree's copy ID = %s, want %s", got, wtID)
	}
	if _, err := gitops.CopyID(ctx, t.TempDir()); err == nil {
		t.Error("a directory that isn't a working copy has no copy ID")
	}
}

func copyID(t *testing.T, dir string) string {
	t.Helper()
	id, err := gitops.CopyID(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	return id
}
