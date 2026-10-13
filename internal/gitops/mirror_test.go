package gitops_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ashtonian/git-mgr/internal/gitops"
)

// A mirror has every ref; an update follows moved refs and keeps ones
// deleted upstream.
func TestMirror(t *testing.T) {
	ctx := context.Background()
	remote, clone := remoteAndClone(t)
	mustGit(t, clone, "push", "-q", "origin", "HEAD:refs/heads/doomed", "HEAD:refs/tags/v1", "HEAD:refs/pull/1/head")
	path := filepath.Join(t.TempDir(), "mirrors", "local", "remote.git")

	created, err := gitops.Mirror(ctx, remote, path, gitops.MirrorOptions{})
	if err != nil || !created {
		t.Fatalf("Mirror = %v, %v; want created", created, err)
	}
	for _, ref := range []string{"refs/heads/main", "refs/heads/doomed", "refs/tags/v1", "refs/pull/1/head"} {
		if _, err := gitops.Git(ctx, path, "rev-parse", "--verify", "-q", ref); err != nil {
			t.Errorf("the mirror lacks %s", ref)
		}
	}

	commitFile(t, clone, "b.txt", "b\n")
	mustGit(t, clone, "push", "-q", "origin", "HEAD:main")
	mustGit(t, clone, "push", "-q", "origin", ":refs/heads/doomed")
	tip := mustGit(t, clone, "rev-parse", "HEAD")
	moved := filepath.Join(filepath.Dir(remote), "moved.git")
	if err := os.Rename(remote, moved); err != nil {
		t.Fatal(err)
	}

	created, err = gitops.Mirror(ctx, moved, path, gitops.MirrorOptions{})
	if err != nil || created {
		t.Fatalf("update = %v, %v", created, err)
	}
	if got := mustGit(t, path, "rev-parse", "refs/heads/main"); got != tip {
		t.Errorf("main = %s, want %s", got, tip)
	}
	if _, err := gitops.Git(ctx, path, "rev-parse", "--verify", "-q", "refs/heads/doomed"); err != nil {
		t.Error("a branch deleted upstream must stay in the mirror")
	}
	if got := mustGit(t, path, "remote", "get-url", "origin"); got != moved {
		t.Errorf("origin = %s, want the new URL %s", got, moved)
	}
}

// Something at the mirror's path that isn't a mirror is left alone.
func TestMirror_RefusesWhatIsntAMirror(t *testing.T) {
	remote, clone := remoteAndClone(t)
	_, err := gitops.Mirror(context.Background(), remote, clone, gitops.MirrorOptions{})
	if !errors.Is(err, gitops.ErrNotAMirror) {
		t.Fatalf("Mirror over a working clone = %v, want ErrNotAMirror", err)
	}
	if mustGit(t, clone, "status", "--porcelain") != "" {
		t.Error("the clone was touched")
	}
}

// With LFS, the mirror holds the repo's Git LFS content too.
func TestMirror_LFS(t *testing.T) {
	f := shapedRemote(t)
	if !f.lfs {
		t.Skip("git-lfs is not installed")
	}
	path := filepath.Join(t.TempDir(), "remote.git")
	if _, err := gitops.Mirror(context.Background(), f.url, path, gitops.MirrorOptions{LFS: true}); err != nil {
		t.Fatal(err)
	}
	var objects []string
	filepath.WalkDir(filepath.Join(path, "lfs", "objects"), func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			objects = append(objects, p)
		}
		return nil
	})
	if len(objects) != 1 {
		t.Fatalf("the mirror should hold the one LFS object, has %v", objects)
	}
	if got, _ := os.ReadFile(objects[0]); string(got) != string(f.content) {
		t.Error("the LFS object isn't the content")
	}
}
