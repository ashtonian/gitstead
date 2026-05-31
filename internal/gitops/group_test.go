package gitops_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ashtonian/git-mgr/internal/gitops"
)

func TestCommit(t *testing.T) {
	dir := setupTestRepo(t)
	ctx := context.Background()

	// Clean repo: nothing to commit.
	committed, err := gitops.Commit(ctx, dir, "noop", false)
	if err != nil {
		t.Fatal(err)
	}
	if committed {
		t.Error("expected nothing to commit on a clean repo")
	}

	// Tracked modification gets committed.
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("changed"), 0644)
	committed, err = gitops.Commit(ctx, dir, "update readme", false)
	if err != nil {
		t.Fatal(err)
	}
	if !committed {
		t.Error("expected a commit for a tracked modification")
	}

	// Untracked file is skipped without addUntracked, committed with it.
	os.WriteFile(filepath.Join(dir, "new.txt"), []byte("x"), 0644)
	if committed, _ = gitops.Commit(ctx, dir, "skip", false); committed {
		t.Error("untracked file should not be committed without addUntracked")
	}
	if committed, _ = gitops.Commit(ctx, dir, "add new", true); !committed {
		t.Error("addUntracked should commit the new file")
	}
}
