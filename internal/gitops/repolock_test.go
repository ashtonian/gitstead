package gitops_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ashtonian/gitten/internal/gitops"
)

func TestLockRepo(t *testing.T) {
	ctx := context.Background()

	t.Run("copies share it; a delete waits for them", func(t *testing.T) {
		repo := setupTestRepo(t)
		first, err := gitops.LockRepo(ctx, repo, false, 0)
		if err != nil {
			t.Fatal(err)
		}
		second, err := gitops.LockRepo(ctx, repo, false, 0)
		if err != nil {
			t.Fatalf("a second copy: %v", err)
		}
		if _, err := gitops.LockRepo(ctx, repo, true, 0); !errors.Is(err, gitops.ErrRepoInUse) {
			t.Fatalf("a delete during copies = %v, want ErrRepoInUse", err)
		}
		first()
		second()
		release, err := gitops.LockRepo(ctx, repo, true, 0)
		if err != nil {
			t.Fatalf("a delete after them: %v", err)
		}
		if _, err := gitops.LockRepo(ctx, repo, false, 0); !errors.Is(err, gitops.ErrRepoInUse) {
			t.Fatalf("a copy during a delete = %v, want ErrRepoInUse", err)
		}
		release()
	})

	t.Run("a repo replaced at the path has a lock of its own", func(t *testing.T) {
		repo := setupTestRepo(t)
		release, err := gitops.LockRepo(ctx, repo, true, 0)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(repo, repo+"-moved"); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
		release()
		if _, err := gitops.LockRepo(ctx, repo, false, 0); err != nil {
			t.Fatalf("a new repo at the path: %v", err)
		}
	})

	t.Run("no repo", func(t *testing.T) {
		if _, err := gitops.LockRepo(ctx, t.TempDir(), false, 0); !errors.Is(err, gitops.ErrRepoGone) {
			t.Fatalf("LockRepo = %v, want ErrRepoGone", err)
		}
	})

	t.Run("its own lock doesn't make the repo busy", func(t *testing.T) {
		repo := setupTestRepo(t)
		release, err := gitops.LockRepo(ctx, repo, true, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		if _, err := gitops.RemoveClone(ctx, repo, t.TempDir(), "x", gitops.RemoveOptions{Discard: true}); err != nil {
			t.Fatalf("RemoveClone under the lock: %v", err)
		}
	})
}
