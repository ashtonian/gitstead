package workgroup_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/ashtonian/git-mgr/internal/gitops"
	"github.com/ashtonian/git-mgr/internal/workgroup"
)

func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init"}, {"config", "user.email", "t@t.com"}, {"config", "user.name", "T"},
	} {
		c := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	gitops.Git(ctx, dir, "add", ".")
	gitops.Git(ctx, dir, "commit", "-m", "init")
	return dir
}

func TestEnsureWorktreeSpec(t *testing.T) {
	src := initRepo(t)
	root := filepath.Join(t.TempDir(), "wg")
	spec := &workgroup.Spec{
		Name: "demo", Kind: "worktree", Root: root, Source: src,
		Members: []workgroup.Member{
			{Name: "a", Path: filepath.Join(root, "a"), Branch: "a"},
			{Name: "b", Path: filepath.Join(root, "b"), Branch: "b"},
		},
	}
	got, err := workgroup.Ensure(context.Background(), spec, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range got {
		if s.Err != nil {
			t.Fatalf("%s: %v", s.Name, s.Err)
		}
		if !gitops.HasGitDir(s.Path) {
			t.Errorf("%s not created", s.Name)
		}
	}

	// Idempotent: a second ensure creates nothing.
	again, _ := workgroup.Ensure(context.Background(), spec, nil)
	for _, s := range again {
		if !s.Existed {
			t.Errorf("expected %s to already exist on second ensure", s.Name)
		}
	}

	workgroup.Remove(context.Background(), spec, true)
	if _, err := os.Stat(spec.Members[0].Path); !os.IsNotExist(err) {
		t.Errorf("member dir should be gone after Remove")
	}
}

func TestEnsureCloneSpec(t *testing.T) {
	src := initRepo(t)
	root := filepath.Join(t.TempDir(), "wg")
	spec := &workgroup.Spec{
		Name: "demo", Kind: "clone", Root: root,
		Members: []workgroup.Member{
			{Name: "svc", Path: filepath.Join(root, "svc"), RepoURL: src},
		},
	}
	got, err := workgroup.Ensure(context.Background(), spec, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Err != nil {
		t.Fatalf("clone: %v", got[0].Err)
	}
	if !gitops.HasGitDir(filepath.Join(root, "svc")) {
		t.Error("clone not created")
	}
}
