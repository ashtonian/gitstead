package cmd

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/ashtonian/gitten/internal/gitops"
)

// A repo renamed since its mirror was made keeps the mirror, moved to the
// new name, and updated there.
func TestRunMirror_FollowsARename(t *testing.T) {
	// Not the developer's git config: a global commit.gpgsign would prompt.
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	ctx := context.Background()
	root := t.TempDir()
	remote, work := filepath.Join(root, "remote.git"), filepath.Join(root, "work")
	for _, args := range [][]string{
		{"init", "-q", "--bare", "-b", "main", remote},
		{"init", "-q", "-b", "main", work},
		{"-C", work, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "x"},
		{"-C", work, "push", "-q", remote, "main"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	old := filepath.Join(root, "mirrors", "github.com", "acme", "old.git")
	if _, err := gitops.Mirror(ctx, remote, old, gitops.MirrorOptions{}); err != nil {
		t.Fatal(err)
	}
	renamed := filepath.Join(root, "mirrors", "github.com", "acme", "new.git")
	o := runMirror(ctx, mirrorJob{key: "github.com/acme/new", url: remote, path: renamed, from: old})
	if o.err != nil || o.created {
		t.Fatalf("runMirror = created %v, %v; want an update of the moved mirror", o.created, o.err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("the old mirror should have moved: %v", err)
	}
	if _, err := gitops.Git(ctx, renamed, "rev-parse", "--verify", "-q", "refs/heads/main"); err != nil {
		t.Errorf("the moved mirror lacks main: %v", err)
	}
}
