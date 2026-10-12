package gitops_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/ashtonian/git-mgr/internal/gitops"
)

func TestClone(t *testing.T) {
	ctx := context.Background()
	remote, _ := remoteAndClone(t)
	parent := t.TempDir()

	// What a killed run left behind is swept; a running clone's is not.
	dead := filepath.Join(parent, ".git-mgr-clone-app-dead")
	running := filepath.Join(parent, ".git-mgr-clone-app-running")
	for _, d := range []string{dead, running} {
		if err := os.MkdirAll(filepath.Join(d, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	held, err := os.Open(running)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	if err := syscall.Flock(int(held.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}

	dest := filepath.Join(parent, "app")
	if err := gitops.Clone(ctx, remote, dest, ""); err != nil {
		t.Fatal(err)
	}
	if !gitops.HasGitDir(dest) {
		t.Fatalf("%s is not a clone", dest)
	}
	if _, err := os.Stat(dead); !os.IsNotExist(err) {
		t.Errorf("a dead staging dir was kept")
	}
	if _, err := os.Stat(running); err != nil {
		t.Errorf("a running clone's staging dir was removed")
	}
	if got := stagingDirs(t, parent); len(got) != 1 {
		t.Errorf("staging dirs left after the clone: %v", got)
	}
}

func TestClone_Failure_LeavesNothing(t *testing.T) {
	parent := t.TempDir()
	dest := filepath.Join(parent, "app")
	err := gitops.Clone(context.Background(), filepath.Join(parent, "no-such-remote.git"), dest, "")
	if err == nil {
		t.Fatal("cloning a missing remote succeeded")
	}
	if _, err := os.Lstat(dest); !os.IsNotExist(err) {
		t.Errorf("a failed clone left %s", dest)
	}
	if got := stagingDirs(t, parent); len(got) != 0 {
		t.Errorf("a failed clone left staging dirs: %v", got)
	}
}

func TestClone_KeepsWhatIsThere(t *testing.T) {
	remote, _ := remoteAndClone(t)
	dest := filepath.Join(t.TempDir(), "app")
	if err := os.Mkdir(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dest, "notes.txt"), []byte("mine\n"))

	err := gitops.Clone(context.Background(), remote, dest, "")
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("Clone onto a non-empty dir = %v", err)
	}
	if data, _ := os.ReadFile(filepath.Join(dest, "notes.txt")); string(data) != "mine\n" {
		t.Errorf("the existing file changed: %q", data)
	}

	// An empty directory is a place to clone into.
	empty := filepath.Join(t.TempDir(), "empty")
	if err := os.Mkdir(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := gitops.Clone(context.Background(), remote, empty, ""); err != nil {
		t.Fatalf("Clone into an empty dir: %v", err)
	}
}

func stagingDirs(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if gitops.IsStaging(e.Name()) {
			out = append(out, e.Name())
		}
	}
	return out
}
