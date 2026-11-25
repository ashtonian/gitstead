package gitops_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ashtonian/gitstead/internal/gitops"
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

// behindBranch returns a clone on agents/x, a branch without an upstream one
// commit behind origin/main: that commit changes a.txt and adds upstream.txt,
// and leaves b.txt alone.
func behindBranch(t *testing.T) (clone string) {
	t.Helper()
	remote, clone := remoteAndClone(t)
	commitFile(t, clone, "b.txt", "b\n")
	mustGit(t, clone, "push", "-q", "origin", "HEAD:main")
	mustGit(t, clone, "checkout", "-q", "--no-track", "-b", "agents/x")
	other := filepath.Join(t.TempDir(), "other")
	mustGit(t, filepath.Dir(other), "clone", "-q", remote, other)
	mustGit(t, other, "config", "user.email", "test@test.com")
	mustGit(t, other, "config", "user.name", "Test")
	writeFile(t, filepath.Join(other, "a.txt"), []byte("upstream\n"))
	writeFile(t, filepath.Join(other, "upstream.txt"), []byte("new\n"))
	mustGit(t, other, "add", ".")
	mustGit(t, other, "commit", "-qm", "upstream")
	mustGit(t, other, "push", "-q", "origin", "HEAD:main")
	mustGit(t, clone, "fetch", "-q")
	return clone
}

// A branch without an upstream is fast-forwarded to a ref of the caller's
// choosing, carrying uncommitted changes across as git pull --ff-only does;
// whatever can't be is left as it was, with the reason.
func TestFastForwardTo(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name    string
		prepare func(t *testing.T, clone string)
		updated bool
		skipped string
	}{
		{"behind and clean", func(t *testing.T, c string) {}, true, ""},
		{"uncommitted changes carry across", func(t *testing.T, c string) { writeFile(t, filepath.Join(c, "b.txt"), []byte("mine\n")) }, true, ""},
		{"a change in the way", func(t *testing.T, c string) { writeFile(t, filepath.Join(c, "a.txt"), []byte("mine\n")) }, false, "git left it as it was"},
		{"an untracked file in the way", func(t *testing.T, c string) { writeFile(t, filepath.Join(c, "upstream.txt"), []byte("mine\n")) }, false, "git left it as it was"},
		{"diverged", func(t *testing.T, c string) { commitFile(t, c, "local.txt", "local\n") }, false, "diverged from origin/main: 1 ahead, 1 behind"},
		{"git busy", func(t *testing.T, c string) { writeFile(t, filepath.Join(c, ".git", "index.lock"), nil) }, false, "index.lock"},
		{"detached", func(t *testing.T, c string) { mustGit(t, c, "checkout", "-q", "--detach") }, false, "not on a branch"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clone := behindBranch(t)
			tt.prepare(t, clone)
			before := mustGit(t, clone, "rev-parse", "HEAD")
			b, _ := os.ReadFile(filepath.Join(clone, "b.txt"))
			updated, skipped, err := gitops.FastForwardTo(ctx, clone, "origin/main")
			if err != nil {
				t.Fatal(err)
			}
			if updated != tt.updated || !strings.Contains(skipped, tt.skipped) || (tt.skipped == "") != (skipped == "") {
				t.Fatalf("FastForwardTo = %v, %q", updated, skipped)
			}
			after := mustGit(t, clone, "rev-parse", "HEAD")
			switch {
			case updated && after != mustGit(t, clone, "rev-parse", "origin/main"):
				t.Error("not at origin/main")
			case updated && mustGit(t, clone, "rev-parse", "--abbrev-ref", "HEAD") != "agents/x":
				t.Error("no longer on its branch")
			case !updated && after != before:
				t.Error("HEAD moved")
			}
			if got, _ := os.ReadFile(filepath.Join(clone, "b.txt")); string(got) != string(b) {
				t.Errorf("b.txt was %q, now %q", b, got)
			}
			if got, _ := os.ReadFile(filepath.Join(clone, "upstream.txt")); strings.Contains(tt.name, "untracked") && string(got) != "mine\n" {
				t.Errorf("the file in the way now holds %q", got)
			}
		})
	}
	t.Run("up to date has nothing to say", func(t *testing.T) {
		clone := behindBranch(t)
		mustGit(t, clone, "reset", "-q", "--hard", "origin/main")
		if updated, skipped, err := gitops.FastForwardTo(ctx, clone, "origin/main"); updated || skipped != "" || err != nil {
			t.Errorf("FastForwardTo = %v, %q, %v", updated, skipped, err)
		}
	})
	t.Run("a ref that doesn't resolve is an error", func(t *testing.T) {
		if _, _, err := gitops.FastForwardTo(ctx, behindBranch(t), "origin/nowhere"); err == nil {
			t.Error("no error")
		}
	})
}

// A branch is rebased onto a ref when that loses nothing: a rebase git
// refuses or that stops on a conflict leaves the branch, its working tree
// and any operation already under way as they were.
func TestRebase(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name    string
		prepare func(t *testing.T, clone string)
		updated bool
		skipped string
		ahead   int // of origin/main afterwards, when updated
	}{
		{"behind", func(t *testing.T, c string) {}, true, "", 0},
		{"diverged", func(t *testing.T, c string) { commitFile(t, c, "local.txt", "local\n") }, true, "", 1},
		{"a conflict is aborted", func(t *testing.T, c string) { commitFile(t, c, "a.txt", "mine\n") }, false, "stopped, so it was aborted", 0},
		{"uncommitted changes", func(t *testing.T, c string) {
			commitFile(t, c, "local.txt", "local\n")
			writeFile(t, filepath.Join(c, "b.txt"), []byte("mine\n"))
		}, false, "git left it as it was", 0},
		{"a merge under way is left alone", func(t *testing.T, c string) {
			commitFile(t, c, "a.txt", "mine\n")
			if _, err := gitops.Git(ctx, c, "merge", "-q", "origin/main"); err == nil {
				t.Fatal("the merge didn't stop")
			}
		}, false, "merge is in progress", 0},
		{"detached", func(t *testing.T, c string) { mustGit(t, c, "checkout", "-q", "--detach") }, false, "not on a branch", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clone := behindBranch(t)
			tt.prepare(t, clone)
			before := mustGit(t, clone, "rev-parse", "HEAD")
			status := mustGit(t, clone, "status", "--porcelain")
			updated, skipped, err := gitops.Rebase(ctx, clone, "origin/main")
			if err != nil {
				t.Fatal(err)
			}
			if updated != tt.updated || !strings.Contains(skipped, tt.skipped) || (tt.skipped == "") != (skipped == "") {
				t.Fatalf("Rebase = %v, %q", updated, skipped)
			}
			if !updated {
				if after := mustGit(t, clone, "rev-parse", "HEAD"); after != before {
					t.Error("HEAD moved")
				}
				if after := mustGit(t, clone, "status", "--porcelain"); after != status {
					t.Errorf("status was\n%s\nnow\n%s", status, after)
				}
				return
			}
			ahead, behind, err := gitops.AheadBehind(ctx, clone, "origin/main")
			if err != nil || ahead != tt.ahead || behind != 0 {
				t.Errorf("after the rebase: %d ahead, %d behind (%v)", ahead, behind, err)
			}
			if got := mustGit(t, clone, "rev-parse", "--abbrev-ref", "HEAD"); got != "agents/x" {
				t.Errorf("on %q", got)
			}
		})
	}
	t.Run("up to date has nothing to say", func(t *testing.T) {
		clone := behindBranch(t)
		mustGit(t, clone, "reset", "-q", "--hard", "origin/main")
		commitFile(t, clone, "local.txt", "local\n")
		if updated, skipped, err := gitops.Rebase(ctx, clone, "origin/main"); updated || skipped != "" || err != nil {
			t.Errorf("Rebase = %v, %q, %v", updated, skipped, err)
		}
	})
}
