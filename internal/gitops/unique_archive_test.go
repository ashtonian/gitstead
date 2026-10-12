package gitops_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ashtonian/git-mgr/internal/gitops"
)

// remoteAndClone returns a bare remote with one pushed commit and a clone of it.
func remoteAndClone(t *testing.T) (remote, clone string) {
	t.Helper()
	root := t.TempDir()
	remote = filepath.Join(root, "remote.git")
	mustGit(t, root, "init", "-q", "--bare", "-b", "main", remote)
	clone = filepath.Join(root, "clone")
	mustGit(t, root, "clone", "-q", remote, clone)
	mustGit(t, clone, "config", "user.email", "test@test.com")
	mustGit(t, clone, "config", "user.name", "Test")
	writeFile(t, filepath.Join(clone, "a.txt"), []byte("a\n"))
	mustGit(t, clone, "add", ".")
	mustGit(t, clone, "commit", "-qm", "initial")
	mustGit(t, clone, "push", "-q", "origin", "HEAD:main")
	return remote, clone
}

func commitFile(t *testing.T, dir, name, content string) {
	t.Helper()
	writeFile(t, filepath.Join(dir, name), []byte(content))
	mustGit(t, dir, "add", name)
	mustGit(t, dir, "commit", "-qm", "add "+name)
}

func TestInspectUniqueWork(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name      string
		setup     func(t *testing.T, clone string)
		want      func(u gitops.UniqueWork) bool
		lostClone bool
	}{
		{"pushed and clean", func(t *testing.T, c string) {}, func(u gitops.UniqueWork) bool { return u == gitops.UniqueWork{} }, false},
		{"changed file", func(t *testing.T, c string) { writeFile(t, filepath.Join(c, "a.txt"), []byte("b\n")) },
			func(u gitops.UniqueWork) bool { return u.Changed == 1 }, true},
		{"untracked file", func(t *testing.T, c string) { writeFile(t, filepath.Join(c, "u.txt"), []byte("u\n")) },
			func(u gitops.UniqueWork) bool { return u.Untracked == 1 && u.Changed == 0 }, true},
		{"rename counts once", func(t *testing.T, c string) { mustGit(t, c, "mv", "a.txt", "b.txt") },
			func(u gitops.UniqueWork) bool { return u.Changed == 1 }, true},
		{"unpushed commit on another branch", func(t *testing.T, c string) {
			mustGit(t, c, "switch", "-qc", "feature")
			commitFile(t, c, "f.txt", "f\n")
			mustGit(t, c, "switch", "-q", "main")
		}, func(u gitops.UniqueWork) bool { return u.UnpushedCommits == 1 }, true},
		{"stash", func(t *testing.T, c string) {
			writeFile(t, filepath.Join(c, "a.txt"), []byte("stashed\n"))
			mustGit(t, c, "stash", "-q")
		}, func(u gitops.UniqueWork) bool { return u.Stashes == 1 && u.Changed == 0 }, true},
		{"detached HEAD commit", func(t *testing.T, c string) {
			mustGit(t, c, "switch", "-q", "--detach")
			commitFile(t, c, "d.txt", "d\n")
		}, func(u gitops.UniqueWork) bool { return u.DetachedCommits == 1 && u.UnpushedCommits == 1 }, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, clone := remoteAndClone(t)
			tt.setup(t, clone)
			u, err := gitops.InspectUniqueWork(ctx, clone)
			if err != nil {
				t.Fatal(err)
			}
			if !tt.want(u) {
				t.Errorf("unexpected unique work: %+v", u)
			}
			if u.LostWithClone() != tt.lostClone {
				t.Errorf("LostWithClone = %v, want %v (%s)", u.LostWithClone(), tt.lostClone, u)
			}
		})
	}
}

func TestInspectUniqueWork_NoRemoteMeansEverythingIsUnique(t *testing.T) {
	u, err := gitops.InspectUniqueWork(context.Background(), setupTestRepo(t))
	if err != nil {
		t.Fatal(err)
	}
	if u.UnpushedCommits != 1 || !u.LostWithClone() {
		t.Fatalf("a repo with no remote holds only unique commits, got %+v", u)
	}
}

func TestInspectUniqueWork_NotARepoIsAnError(t *testing.T) {
	if _, err := gitops.InspectUniqueWork(context.Background(), t.TempDir()); err == nil {
		t.Fatal("a directory that is not a repo must be an error, never \"no unique work\"")
	}
}

// Everything deleting a clone would lose comes back from the archive.
func TestRemoveClone_ArchiveRestoresUniqueWork(t *testing.T) {
	ctx := context.Background()
	remote, clone := remoteAndClone(t)
	mustGit(t, clone, "switch", "-qc", "feature")
	commitFile(t, clone, "feature.txt", "unpushed\n")
	writeFile(t, filepath.Join(clone, "a.txt"), []byte("stashed\n"))
	mustGit(t, clone, "stash", "-q")
	writeFile(t, filepath.Join(clone, "a.txt"), []byte("dirty\n"))
	writeFile(t, filepath.Join(clone, "notes.txt"), []byte("untracked\n"))
	featureTip := mustGit(t, clone, "rev-parse", "feature")

	archiveDir := t.TempDir()
	bundle, err := gitops.RemoveClone(ctx, clone, archiveDir, "github.com/acme/app", false)
	if err != nil {
		t.Fatalf("RemoveClone: %v", err)
	}
	if bundle == "" {
		t.Fatal("unique work must produce an archive")
	}
	if _, err := os.Stat(clone); !os.IsNotExist(err) {
		t.Fatalf("clone should be deleted, stat err = %v", err)
	}
	if _, err := os.Stat(strings.TrimSuffix(bundle, ".bundle") + ".json"); err != nil {
		t.Fatalf("archive metadata missing: %v", err)
	}

	restored := filepath.Join(t.TempDir(), "restored")
	mustGit(t, filepath.Dir(restored), "clone", "-q", remote, restored)
	mustGit(t, restored, "fetch", "-q", bundle, "+refs/*:refs/restored/*")

	if got := mustGit(t, restored, "rev-parse", "refs/restored/heads/feature"); got != featureTip {
		t.Errorf("restored feature = %s, want %s", got, featureTip)
	}
	refs := mustGit(t, restored, "for-each-ref", "--format=%(refname)", "refs/restored/git-mgr/archive/")
	var worktreeRef, stashRef string
	for _, r := range strings.Split(refs, "\n") {
		switch {
		case strings.HasSuffix(r, "/worktree"):
			worktreeRef = r
		case strings.HasSuffix(r, "/stash-0"):
			stashRef = r
		}
	}
	if worktreeRef == "" || stashRef == "" {
		t.Fatalf("archive must carry worktree and stash refs, got:\n%s", refs)
	}
	mustGit(t, restored, "checkout", "-q", "--detach", featureTip)
	mustGit(t, restored, "stash", "apply", "-q", "--index", worktreeRef)
	for name, want := range map[string]string{"a.txt": "dirty\n", "notes.txt": "untracked\n"} {
		if got, _ := os.ReadFile(filepath.Join(restored, name)); string(got) != want {
			t.Errorf("restored %s = %q, want %q", name, got, want)
		}
	}
	if got := mustGit(t, restored, "show", stashRef+":a.txt"); got != "stashed" {
		t.Errorf("restored stash = %q", got)
	}
}

func TestRemoveClone_NothingUniqueNeedsNoArchive(t *testing.T) {
	_, clone := remoteAndClone(t)
	bundle, err := gitops.RemoveClone(context.Background(), clone, t.TempDir(), "x", false)
	if err != nil || bundle != "" {
		t.Fatalf("clean pushed clone: bundle=%q err=%v", bundle, err)
	}
	if _, err := os.Stat(clone); !os.IsNotExist(err) {
		t.Fatal("clone should be deleted")
	}
}

func TestRemoveClone_UninspectableIsKept(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a", "b", "not-a-repo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "precious.txt"), []byte("keep\n"))
	if _, err := gitops.RemoveClone(context.Background(), dir, t.TempDir(), "x", false); err == nil {
		t.Fatal("RemoveClone must fail when it cannot inspect the directory")
	}
	if _, err := os.Stat(filepath.Join(dir, "precious.txt")); err != nil {
		t.Fatalf("an uninspectable directory must be kept: %v", err)
	}
}

// Removing a worktree keeps its branch in the source repo and archives only
// the working tree.
// A nested repository can't go into the archive, so the clone is kept.
func TestRemoveClone_NestedRepoIsKept(t *testing.T) {
	_, clone := remoteAndClone(t)
	nested := filepath.Join(clone, "vendor-repo")
	mustGit(t, clone, "init", "-q", nested)
	writeFile(t, filepath.Join(nested, "only-copy.txt"), []byte("x"))

	if _, err := gitops.RemoveClone(context.Background(), clone, t.TempDir(), "x", false); err == nil {
		t.Fatal("RemoveClone must refuse a clone holding a nested repository")
	}
	if _, err := os.Stat(filepath.Join(nested, "only-copy.txt")); err != nil {
		t.Fatalf("nested repository was deleted: %v", err)
	}
}

func TestRemoveClone_AlreadyGoneIsRemoved(t *testing.T) {
	gone := filepath.Join(t.TempDir(), "a", "b", "gone")
	bundle, err := gitops.RemoveClone(context.Background(), gone, t.TempDir(), "x", false)
	if err != nil || bundle != "" {
		t.Fatalf("a clone already gone counts as removed: bundle=%q err=%v", bundle, err)
	}
}

func TestRemoveClone_RefusesRelativePath(t *testing.T) {
	if _, err := gitops.RemoveClone(context.Background(), "repo", t.TempDir(), "x", false); err == nil {
		t.Fatal("RemoveClone must refuse a relative path")
	}
}

func TestRemoveWorktree_KeepsBranchAndArchivesWorkingTree(t *testing.T) {
	ctx := context.Background()
	_, source := remoteAndClone(t)
	wt := filepath.Join(t.TempDir(), "agent-1")
	mustGit(t, source, "worktree", "add", "-q", "-b", "agents/app/agent-1", wt)
	commitFile(t, wt, "agent.txt", "committed\n")
	writeFile(t, filepath.Join(wt, "agent.txt"), []byte("uncommitted\n"))
	tip := mustGit(t, wt, "rev-parse", "HEAD")

	if _, err := gitops.Backup(ctx, wt, gitops.NewBackupConfig(gitops.WithStrategy("stash-ref"))); err != nil {
		t.Fatal(err)
	}

	bundle, err := gitops.RemoveWorktree(ctx, source, wt, t.TempDir(), "app-agent-1", false)
	if err != nil {
		t.Fatalf("RemoveWorktree: %v", err)
	}
	if refs := mustGit(t, source, "for-each-ref", "refs/git-mgr/backups/"); refs != "" {
		t.Errorf("a removed worktree's backup refs must go with it:\n%s", refs)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Fatal("worktree directory should be removed")
	}
	if got := mustGit(t, source, "rev-parse", "agents/app/agent-1"); got != tip {
		t.Errorf("branch must survive in the source repo: got %s want %s", got, tip)
	}
	if bundle == "" {
		t.Fatal("uncommitted worktree changes must be archived")
	}
	mustGit(t, source, "fetch", "-q", bundle, "+refs/git-mgr/archive/*:refs/restored/*")
	refs := mustGit(t, source, "for-each-ref", "--format=%(refname)", "refs/restored/")
	var worktreeRef string
	for _, r := range strings.Split(refs, "\n") {
		if strings.HasSuffix(r, "/worktree") {
			worktreeRef = r
		}
	}
	if worktreeRef == "" {
		t.Fatalf("missing worktree snapshot ref in:\n%s", refs)
	}
	if got := mustGit(t, source, "show", worktreeRef+":agent.txt"); got != "uncommitted" {
		t.Errorf("restored worktree file = %q", got)
	}
}
