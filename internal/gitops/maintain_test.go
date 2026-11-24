package gitops_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/ashtonian/gitstead/internal/gitops"
)

// maintainedRepo is a repo with one pack and temporary packs planted beside
// it as an interrupted fetch and repack leave them: two older than a day,
// one written an hour before the clock's now.
func maintainedRepo(t *testing.T, clock *testClock) (repo string, old, young []string) {
	t.Helper()
	repo = t.TempDir()
	mustGit(t, repo, "init", "-q", "-b", "main")
	writeFile(t, filepath.Join(repo, "a.txt"), []byte("a"))
	mustGit(t, repo, "add", ".")
	mustGit(t, repo, "commit", "-qm", "a")
	mustGit(t, repo, "repack", "-adq")
	pack := filepath.Join(repo, ".git", "objects", "pack")
	plant := func(name string, age time.Duration) string {
		path := filepath.Join(pack, name)
		writeFile(t, path, make([]byte, 4096))
		at := clock.Now().Add(-age)
		if err := os.Chtimes(path, at, at); err != nil {
			t.Fatal(err)
		}
		return path
	}
	old = []string{plant("tmp_pack_x1Yz9Q", 48*time.Hour), plant(".tmp-4242-pack-5f3c.pack", 30*time.Hour)}
	young = []string{plant("tmp_pack_Ab3dE7", time.Hour)}
	return repo, old, young
}

func TestMaintain(t *testing.T) {
	ctx := context.Background()
	clock := newTestClock()
	opts := gitops.MaintainOptions{TempPackAge: 24 * time.Hour, Now: clock.Now}

	t.Run("removes old temporary packs and runs maintenance", func(t *testing.T) {
		repo, old, young := maintainedRepo(t, clock)
		res, err := gitops.Maintain(ctx, repo, opts)
		if err != nil {
			t.Fatal(err)
		}
		slices.Sort(old)
		slices.Sort(res.Removed)
		if !slices.Equal(res.Removed, old) || res.Bytes != 2*4096 || !res.Maintained {
			t.Errorf("result = %+v, want %v removed (8192 bytes) and maintenance run", res, old)
		}
		for _, p := range old {
			if exists(t, p) {
				t.Errorf("%s should be removed", p)
			}
		}
		for _, p := range young {
			if !exists(t, p) {
				t.Errorf("%s is younger than temp_pack_age and should stay", p)
			}
		}
		mustGit(t, repo, "fsck", "--no-progress")
	})

	t.Run("a dry run lists and changes nothing", func(t *testing.T) {
		repo, old, _ := maintainedRepo(t, clock)
		dry := opts
		dry.DryRun = true
		res, err := gitops.Maintain(ctx, repo, dry)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Removed) != len(old) || res.Maintained {
			t.Errorf("result = %+v, want %d listed and no maintenance", res, len(old))
		}
		for _, p := range old {
			if !exists(t, p) {
				t.Errorf("a dry run removed %s", p)
			}
		}
	})

	t.Run("a repo git is busy in is left alone", func(t *testing.T) {
		repo, old, _ := maintainedRepo(t, clock)
		writeFile(t, filepath.Join(repo, ".git", "index.lock"), nil)
		res, err := gitops.Maintain(ctx, repo, opts)
		if !errors.Is(err, gitops.ErrBusy) {
			t.Fatalf("err = %v, want ErrBusy", err)
		}
		if len(res.Removed) != 0 || !exists(t, old[0]) {
			t.Errorf("a busy repo was changed: %+v", res)
		}
	})

	t.Run("a repo another gitstead command holds is left alone", func(t *testing.T) {
		repo, old, _ := maintainedRepo(t, clock)
		release, err := gitops.LockRepo(ctx, repo, true, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		if _, err := gitops.Maintain(ctx, repo, opts); !errors.Is(err, gitops.ErrRepoInUse) {
			t.Fatalf("err = %v, want ErrRepoInUse", err)
		}
		if !exists(t, old[0]) {
			t.Error("a repo in use was changed")
		}
	})
}
