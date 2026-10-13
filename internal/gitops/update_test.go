package gitops_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ashtonian/git-mgr/internal/gitops"
)

// A clone is fast-forwarded only when that loses nothing; otherwise it is
// left as it was, with the reason.
func TestFastForward(t *testing.T) {
	ctx := context.Background()
	behindClone := func(t *testing.T) (clone string) {
		t.Helper()
		remote, clone := remoteAndClone(t)
		other := filepath.Join(t.TempDir(), "other")
		mustGit(t, filepath.Dir(other), "clone", "-q", remote, other)
		commitFile(t, other, "upstream.txt", "new\n")
		mustGit(t, other, "push", "-q", "origin", "HEAD:main")
		mustGit(t, clone, "fetch", "-q")
		return clone
	}
	tests := []struct {
		name    string
		prepare func(t *testing.T, clone string)
		updated bool
		skipped string
	}{
		{"behind and clean", func(t *testing.T, c string) {}, true, ""},
		{"untracked files stay", func(t *testing.T, c string) { writeFile(t, filepath.Join(c, "scratch.txt"), []byte("mine\n")) }, true, ""},
		{"uncommitted changes", func(t *testing.T, c string) { writeFile(t, filepath.Join(c, "a.txt"), []byte("edited\n")) }, false, "uncommitted changes"},
		{"diverged", func(t *testing.T, c string) { commitFile(t, c, "local.txt", "local\n") }, false, "diverged"},
		{"git busy", func(t *testing.T, c string) { writeFile(t, filepath.Join(c, ".git", "index.lock"), nil) }, false, "index.lock"},
		{"an untracked file in the way", func(t *testing.T, c string) {
			writeFile(t, filepath.Join(c, "upstream.txt"), []byte("mine\n"))
		}, false, "git left it as it was"},
		{"an ignored file in the way", func(t *testing.T, c string) {
			writeFile(t, filepath.Join(c, ".git", "info", "exclude"), []byte("upstream.txt\n"))
			writeFile(t, filepath.Join(c, "upstream.txt"), []byte("mine\n"))
		}, false, "git left it as it was"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clone := behindClone(t)
			tt.prepare(t, clone)
			before := mustGit(t, clone, "rev-parse", "HEAD")
			updated, skipped, err := gitops.FastForward(ctx, clone)
			if err != nil {
				t.Fatal(err)
			}
			if updated != tt.updated || !strings.Contains(skipped, tt.skipped) || (tt.skipped == "") != (skipped == "") {
				t.Fatalf("FastForward = %v, %q", updated, skipped)
			}
			after := mustGit(t, clone, "rev-parse", "HEAD")
			if updated {
				if after != mustGit(t, clone, "rev-parse", "origin/main") {
					t.Error("not at its upstream")
				}
				if got, _ := os.ReadFile(filepath.Join(clone, "upstream.txt")); string(got) != "new\n" {
					t.Error("working tree not updated")
				}
			} else if after != before {
				t.Error("HEAD moved")
			}
			if got, err := os.ReadFile(filepath.Join(clone, "upstream.txt")); err == nil && strings.Contains(tt.name, "in the way") && string(got) != "mine\n" {
				t.Errorf("the file in the way now holds %q", got)
			}
		})
	}
	t.Run("up to date has nothing to say", func(t *testing.T) {
		_, clone := remoteAndClone(t)
		if updated, skipped, err := gitops.FastForward(ctx, clone); updated || skipped != "" || err != nil {
			t.Errorf("FastForward = %v, %q, %v", updated, skipped, err)
		}
	})
}
