package gitops_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ashtonian/gitstead/internal/gitops"
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
		{"pushed and clean", func(t *testing.T, c string) {}, func(u gitops.UniqueWork) bool { return reflect.DeepEqual(u, gitops.UniqueWork{}) }, false},
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
		{"commit only a tag reaches", func(t *testing.T, c string) {
			commitFile(t, c, "t.txt", "t\n")
			mustGit(t, c, "tag", "kept")
			mustGit(t, c, "reset", "-q", "--hard", "origin/main")
		}, func(u gitops.UniqueWork) bool {
			return u.OtherRefCommits == 1 && u.UnpushedCommits == 0 && !u.LostWithWorktree()
		}, true},
		{"gitstead's own backup refs aren't work", func(t *testing.T, c string) {
			mustGit(t, c, "update-ref", "refs/gitstead/backups/x", mustGit(t, c, "commit-tree", "-m", "wip", "HEAD^{tree}"))
		}, func(u gitops.UniqueWork) bool { return reflect.DeepEqual(u, gitops.UniqueWork{}) }, false},
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

// Everything deleting a clone would lose comes back from its archive alone:
// the upstream is gone too, as it is when stale cleanup deletes a clone.
func TestRemoveClone_ArchiveRestoresWithoutTheUpstream(t *testing.T) {
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
	bundle, err := gitops.RemoveClone(ctx, clone, archiveDir, "github.com/acme/app", gitops.RemoveOptions{})
	if err != nil {
		t.Fatalf("RemoveClone: %v", err)
	}
	if bundle == "" {
		t.Fatal("unique work must produce an archive")
	}
	if _, err := os.Stat(clone); !os.IsNotExist(err) {
		t.Fatalf("clone should be deleted, stat err = %v", err)
	}
	if err := os.RemoveAll(remote); err != nil {
		t.Fatal(err)
	}

	restored := filepath.Join(t.TempDir(), "restored")
	if err := gitops.RestoreArchive(ctx, bundle, restored, ""); err != nil {
		t.Fatalf("RestoreArchive: %v", err)
	}
	if got := mustGit(t, restored, "symbolic-ref", "--short", "HEAD"); got != "feature" {
		t.Errorf("HEAD = %s, want feature", got)
	}
	if got := mustGit(t, restored, "rev-parse", "HEAD"); got != featureTip {
		t.Errorf("feature = %s, want %s", got, featureTip)
	}
	for name, want := range map[string]string{"a.txt": "dirty\n", "notes.txt": "untracked\n", "feature.txt": "unpushed\n"} {
		if got, _ := os.ReadFile(filepath.Join(restored, name)); string(got) != want {
			t.Errorf("restored %s = %q, want %q", name, got, want)
		}
	}
	if got := mustGit(t, restored, "show", "stash@{0}:a.txt"); got != "stashed" {
		t.Errorf("restored stash = %q", got)
	}
	if got := mustGit(t, restored, "remote", "get-url", "origin"); got != remote {
		t.Errorf("origin = %s, want %s", got, remote)
	}
	if refs := mustGit(t, restored, "for-each-ref", "refs/gitstead/archive/"); refs != "" {
		t.Errorf("the archive's own refs are left in the restore:\n%s", refs)
	}
}

// A commit only a tag reaches is unique work: deleting the clone would lose it.
func TestRemoveClone_ArchivesTagOnlyCommits(t *testing.T) {
	ctx := context.Background()
	remote, clone := remoteAndClone(t)
	commitFile(t, clone, "treasure.txt", "unique\n")
	tip := mustGit(t, clone, "rev-parse", "HEAD")
	mustGit(t, clone, "tag", "saved-work")
	mustGit(t, clone, "reset", "-q", "--hard", "origin/main")

	bundle, err := gitops.RemoveClone(ctx, clone, t.TempDir(), "app", gitops.RemoveOptions{})
	if err != nil || bundle == "" {
		t.Fatalf("RemoveClone = %q, %v; want an archive of the tagged commit", bundle, err)
	}
	if err := os.RemoveAll(remote); err != nil {
		t.Fatal(err)
	}
	restored := filepath.Join(t.TempDir(), "restored")
	if err := gitops.RestoreArchive(ctx, bundle, restored, ""); err != nil {
		t.Fatal(err)
	}
	if got := mustGit(t, restored, "rev-parse", "saved-work^{commit}"); got != tip {
		t.Errorf("saved-work = %s, want %s", got, tip)
	}
}

// Deleting a clone git is busy in could catch it half-changed, so it is kept,
// whether its work is saved or discarded.
func TestRemoveClone_KeepsAcloneGitIsBusyIn(t *testing.T) {
	tests := []struct {
		name string
		busy func(t *testing.T, gitDir string)
	}{
		{"index.lock", func(t *testing.T, g string) { writeFile(t, filepath.Join(g, "index.lock"), nil) }},
		{"a ref's lock", func(t *testing.T, g string) { writeFile(t, filepath.Join(g, "refs", "heads", "main.lock"), nil) }},
		{"gc running", func(t *testing.T, g string) { writeFile(t, filepath.Join(g, "gc.pid"), []byte("1 host")) }},
		{"rebase in progress", func(t *testing.T, g string) {
			if err := os.Mkdir(filepath.Join(g, "rebase-merge"), 0o755); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tt := range tests {
		for _, discard := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/discard=%v", tt.name, discard), func(t *testing.T) {
				_, clone := remoteAndClone(t)
				tt.busy(t, filepath.Join(clone, ".git"))
				_, err := gitops.RemoveClone(context.Background(), clone, t.TempDir(), "app", gitops.RemoveOptions{Discard: discard})
				if !errors.Is(err, gitops.ErrBusy) {
					t.Fatalf("RemoveClone = %v, want ErrBusy", err)
				}
				if !gitops.HasGitDir(clone) {
					t.Fatal("the clone was deleted")
				}
			})
		}
	}
}

// A workspace copy's bundle leans on the repo it was made from, which keeps
// the history the copy shares with its remotes; when that repo lacks some of
// it, the bundle holds everything instead.
func TestRemoveClone_BundleLeansOnItsBase(t *testing.T) {
	ctx := context.Background()
	remote, base := remoteAndClone(t)
	newCopy := func(t *testing.T) string {
		copyPath := filepath.Join(t.TempDir(), "copy")
		mustGit(t, base, "clone", "-q", remote, copyPath)
		mustGit(t, copyPath, "config", "user.email", "test@test.com")
		mustGit(t, copyPath, "config", "user.name", "Test")
		return copyPath
	}
	readMeta := func(t *testing.T, bundle string) gitops.ArchiveMeta {
		meta, _, err := gitops.ReadArchive(bundle)
		if err != nil {
			t.Fatal(err)
		}
		return meta
	}

	t.Run("base holds the history", func(t *testing.T) {
		copyPath := newCopy(t)
		commitFile(t, copyPath, "agent.txt", "work\n")
		tip := mustGit(t, copyPath, "rev-parse", "HEAD")
		bundle, err := gitops.RemoveClone(ctx, copyPath, t.TempDir(), "copy", gitops.RemoveOptions{Base: base, Repo: "example.com/o/app"})
		if err != nil {
			t.Fatal(err)
		}
		if meta := readMeta(t, bundle); meta.Complete || meta.Base != base || meta.Repo != "example.com/o/app" {
			t.Fatalf("meta = %+v, want a bundle leaning on example.com/o/app at %s", meta, base)
		}
		restored := filepath.Join(t.TempDir(), "restored")
		if err := gitops.RestoreArchive(ctx, bundle, restored, ""); err != nil {
			t.Fatal(err)
		}
		if got := mustGit(t, restored, "rev-parse", "HEAD"); got != tip {
			t.Errorf("HEAD = %s, want %s", got, tip)
		}
	})

	t.Run("base lacks some of it", func(t *testing.T) {
		copyPath := newCopy(t)
		other := filepath.Join(t.TempDir(), "other")
		mustGit(t, base, "clone", "-q", remote, other)
		mustGit(t, other, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "upstream moved")
		mustGit(t, other, "push", "-q", "origin", "HEAD:main")
		mustGit(t, copyPath, "pull", "-q", "--ff-only")
		commitFile(t, copyPath, "agent.txt", "work\n")

		bundle, err := gitops.RemoveClone(ctx, copyPath, t.TempDir(), "copy", gitops.RemoveOptions{Base: base})
		if err != nil {
			t.Fatal(err)
		}
		if meta := readMeta(t, bundle); !meta.Complete {
			t.Fatalf("meta = %+v, want a complete bundle", meta)
		}
	})
}

// A repo other archives lean on is archived whole, unique work or not.
func TestRemoveClone_KeepHistoryArchivesAcleanClone(t *testing.T) {
	ctx := context.Background()
	remote, clone := remoteAndClone(t)
	tip := mustGit(t, clone, "rev-parse", "HEAD")
	bundle, err := gitops.RemoveClone(ctx, clone, t.TempDir(), "app", gitops.RemoveOptions{KeepHistory: true})
	if err != nil || bundle == "" {
		t.Fatalf("RemoveClone = %q, %v; want an archive", bundle, err)
	}
	if err := os.RemoveAll(remote); err != nil {
		t.Fatal(err)
	}
	restored := filepath.Join(t.TempDir(), "restored")
	if err := gitops.RestoreArchive(ctx, bundle, restored, ""); err != nil {
		t.Fatal(err)
	}
	if got := mustGit(t, restored, "rev-parse", "origin/main"); got != tip {
		t.Errorf("origin/main = %s, want %s", got, tip)
	}
}

func TestRemoveClone_NothingUniqueNeedsNoArchive(t *testing.T) {
	_, clone := remoteAndClone(t)
	bundle, err := gitops.RemoveClone(context.Background(), clone, t.TempDir(), "x", gitops.RemoveOptions{})
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
	if _, err := gitops.RemoveClone(context.Background(), dir, t.TempDir(), "x", gitops.RemoveOptions{}); err == nil {
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

	if _, err := gitops.RemoveClone(context.Background(), clone, t.TempDir(), "x", gitops.RemoveOptions{}); err == nil {
		t.Fatal("RemoveClone must refuse a clone holding a nested repository")
	}
	if _, err := os.Stat(filepath.Join(nested, "only-copy.txt")); err != nil {
		t.Fatalf("nested repository was deleted: %v", err)
	}
}

func TestRemoveClone_AlreadyGoneIsRemoved(t *testing.T) {
	gone := filepath.Join(t.TempDir(), "a", "b", "gone")
	bundle, err := gitops.RemoveClone(context.Background(), gone, t.TempDir(), "x", gitops.RemoveOptions{})
	if err != nil || bundle != "" {
		t.Fatalf("a clone already gone counts as removed: bundle=%q err=%v", bundle, err)
	}
}

func TestRemoveClone_RefusesRelativePath(t *testing.T) {
	if _, err := gitops.RemoveClone(context.Background(), "repo", t.TempDir(), "x", gitops.RemoveOptions{}); err == nil {
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

	bundle, err := gitops.RemoveWorktree(ctx, source, wt, t.TempDir(), "app-agent-1", gitops.RemoveOptions{})
	if err != nil {
		t.Fatalf("RemoveWorktree: %v", err)
	}
	if refs := mustGit(t, source, "for-each-ref", "refs/gitstead/backups/"); refs != "" {
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
	mustGit(t, source, "fetch", "-q", bundle, "+refs/gitstead/archive/*:refs/restored/*")
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

	// Its branch commit is in no remote, only in the repository it was a
	// worktree of, which the archive records as its base.
	restored := filepath.Join(t.TempDir(), "restored")
	if err := gitops.RestoreArchive(ctx, bundle, restored, ""); err != nil {
		t.Fatalf("restoring the worktree's archive: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(restored, "agent.txt")); string(got) != "uncommitted\n" {
		t.Errorf("restored agent.txt = %q", got)
	}
}

// A submodule's own unpushed commits and changes are unique work of the copy
// that checked it out, and are archived before the copy is deleted.
func TestRemoveClone_ArchivesSubmoduleWork(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	subRemote := filepath.Join(root, "sub.git")
	mustGit(t, root, "init", "-q", "--bare", "-b", "main", subRemote)
	seed := filepath.Join(root, "seed")
	mustGit(t, root, "clone", "-q", subRemote, seed)
	mustGit(t, seed, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "s")
	mustGit(t, seed, "push", "-q", "origin", "HEAD:main")

	superRemote := filepath.Join(root, "super.git")
	mustGit(t, root, "init", "-q", "--bare", "-b", "main", superRemote)
	clone := filepath.Join(root, "clone")
	mustGit(t, root, "clone", "-q", superRemote, clone)
	mustGit(t, clone, "-c", "protocol.file.allow=always", "submodule", "add", "-q", subRemote, "lib")
	mustGit(t, clone, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "-m", "add lib")
	mustGit(t, clone, "push", "-q", "origin", "HEAD:main")

	sub := filepath.Join(clone, "lib")
	mustGit(t, sub, "checkout", "-q", "-b", "work")
	mustGit(t, sub, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "unpushed")
	writeFile(t, filepath.Join(sub, "wip.txt"), []byte("wip\n"))

	work, err := gitops.InspectUniqueWork(ctx, clone)
	if err != nil {
		t.Fatal(err)
	}
	if w, ok := work.Submodules["lib"]; !ok || w.UnpushedCommits != 1 || w.Untracked != 1 || !work.LostWithClone() || !work.LostWithWorktree() {
		t.Fatalf("work = %+v", work)
	}
	archive := filepath.Join(root, "archive")
	path, err := gitops.RemoveClone(ctx, clone, archive, "super", gitops.RemoveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(clone); !os.IsNotExist(err) {
		t.Fatal("the clone wasn't removed")
	}
	var meta gitops.ArchiveMeta
	data, err := os.ReadFile(strings.TrimSuffix(path, ".bundle") + ".json")
	if err != nil || json.Unmarshal(data, &meta) != nil {
		t.Fatalf("meta: %s, %v", data, err)
	}
	subBundle := meta.Submodules["lib"]
	if subBundle == "" {
		t.Fatalf("no submodule archive in %+v", meta)
	}
	heads := mustGit(t, root, "bundle", "list-heads", subBundle)
	if !strings.Contains(heads, "refs/heads/work") || !strings.Contains(heads, "worktree") {
		t.Errorf("the submodule's bundle lacks its branch or working tree:\n%s", heads)
	}

	restored := filepath.Join(root, "restored")
	if err := gitops.RestoreArchive(ctx, path, restored, ""); err != nil {
		t.Fatal(err)
	}
	lib := filepath.Join(restored, "lib")
	if got := mustGit(t, lib, "symbolic-ref", "--short", "HEAD"); got != "work" {
		t.Errorf("restored submodule is on %s, want work", got)
	}
	if got, _ := os.ReadFile(filepath.Join(lib, "wip.txt")); string(got) != "wip\n" {
		t.Errorf("restored submodule's wip.txt = %q", got)
	}
}

// A clone's Git LFS objects aren't in a bundle, which holds pointers to
// them: they are kept beside it, hardlinked.
func TestRemoveClone_KeepsLFSObjects(t *testing.T) {
	ctx := context.Background()
	_, clone := remoteAndClone(t)
	objects := filepath.Join(clone, ".git", "lfs", "objects", "ab", "cd")
	if err := os.MkdirAll(objects, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(objects, "abcd1234"), []byte("big file\n"))
	writeFile(t, filepath.Join(clone, "wip.txt"), []byte("wip\n"))

	path, err := gitops.RemoveClone(ctx, clone, filepath.Join(t.TempDir(), "archive"), "app", gitops.RemoveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var meta gitops.ArchiveMeta
	data, _ := os.ReadFile(strings.TrimSuffix(path, ".bundle") + ".json")
	if err := json.Unmarshal(data, &meta); err != nil || meta.LFS == "" {
		t.Fatalf("meta: %s, %v", data, err)
	}
	if got, err := os.ReadFile(filepath.Join(meta.LFS, "ab", "cd", "abcd1234")); err != nil || string(got) != "big file\n" {
		t.Errorf("the LFS object wasn't kept: %q, %v", got, err)
	}
}

// A token someone put in a remote's URL never reaches an archive's
// description, which gitstead prints and serves as JSON.
func TestRemoveClone_ArchiveKeepsNoCredentials(t *testing.T) {
	ctx := context.Background()
	_, clone := remoteAndClone(t)
	writeFile(t, filepath.Join(clone, "wip.txt"), []byte("wip\n"))
	mustGit(t, clone, "remote", "add", "backup", "https://alice:not-a-real-token@example.invalid/o/r.git")
	bundle, err := gitops.RemoveClone(ctx, clone, t.TempDir(), "app", gitops.RemoveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(strings.TrimSuffix(bundle, ".bundle") + ".json")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "not-a-real-token") || !strings.Contains(string(data), "https://example.invalid/o/r.git") {
		t.Errorf("archive description:\n%s", data)
	}
}

// A restore that fails leaves nothing at its destination, nor beside it.
func TestRestoreArchive_FailureLeavesNothing(t *testing.T) {
	ctx := context.Background()
	remote, base := remoteAndClone(t)
	copyPath := filepath.Join(t.TempDir(), "copy")
	mustGit(t, base, "clone", "-q", remote, copyPath)
	mustGit(t, copyPath, "config", "user.email", "t@t")
	mustGit(t, copyPath, "config", "user.name", "t")
	commitFile(t, copyPath, "agent.txt", "work\n")
	mustGit(t, copyPath, "remote", "remove", "origin")
	mustGit(t, copyPath, "remote", "add", "origin", filepath.Join(t.TempDir(), "gone.git"))
	bundle, err := gitops.RemoveClone(ctx, copyPath, t.TempDir(), "copy", gitops.RemoveOptions{Base: base})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(base); err != nil { // the base it leans on, gone; origin never existed
		t.Fatal(err)
	}
	parent := t.TempDir()
	dest := filepath.Join(parent, "restored")
	if err := gitops.RestoreArchive(ctx, bundle, dest, ""); err == nil {
		t.Fatal("restoring without the history it leans on succeeded")
	}
	if entries, _ := os.ReadDir(parent); len(entries) != 0 {
		t.Errorf("left behind: %v", entries)
	}

	// Something already at the destination is never touched.
	write := filepath.Join(dest, "valuable")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, write, []byte("mine\n"))
	if err := gitops.RestoreArchive(ctx, bundle, dest, ""); err == nil {
		t.Fatal("restored over an occupied destination")
	}
	if got, _ := os.ReadFile(write); string(got) != "mine\n" {
		t.Errorf("the occupant now holds %q", got)
	}
}

// A clone on an unborn branch -- staged and untracked files, no commit --
// comes back from its archive as it was.
func TestRestoreArchive_UnbornBranch(t *testing.T) {
	ctx := context.Background()
	_, clone := remoteAndClone(t)
	mustGit(t, clone, "checkout", "-q", "--orphan", "newborn")
	mustGit(t, clone, "rm", "-q", "-rf", ".")
	writeFile(t, filepath.Join(clone, "staged.txt"), []byte("staged\n"))
	mustGit(t, clone, "add", "staged.txt")
	writeFile(t, filepath.Join(clone, "untracked.txt"), []byte("untracked\n"))
	bundle, err := gitops.RemoveClone(ctx, clone, t.TempDir(), "app", gitops.RemoveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	restored := filepath.Join(t.TempDir(), "restored")
	if err := gitops.RestoreArchive(ctx, bundle, restored, ""); err != nil {
		t.Fatal(err)
	}
	if got := mustGit(t, restored, "symbolic-ref", "--short", "HEAD"); got != "newborn" {
		t.Errorf("HEAD = %s", got)
	}
	if got := mustGit(t, restored, "diff", "--cached", "--name-only"); got != "staged.txt" {
		t.Errorf("staged = %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(restored, "untracked.txt")); string(got) != "untracked\n" {
		t.Errorf("untracked.txt = %q", got)
	}
}

// Work in a nested submodule is archived once, within its parent
// submodule's archive, and restored once.
func TestRestoreArchive_NestedSubmodules(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	bare := func(name string) string {
		remote := filepath.Join(root, name+".git")
		mustGit(t, root, "init", "-q", "--bare", "-b", "main", remote)
		seed := filepath.Join(root, "seed-"+name)
		mustGit(t, root, "clone", "-q", remote, seed)
		writeFile(t, filepath.Join(seed, name+".txt"), []byte(name+"\n"))
		mustGit(t, seed, "add", ".")
		mustGit(t, seed, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-qm", name)
		return seed
	}
	leaf := bare("leaf")
	mustGit(t, leaf, "push", "-q", "origin", "HEAD:main")
	mid := bare("mid")
	mustGit(t, mid, "-c", "protocol.file.allow=always", "submodule", "add", "-q", filepath.Join(root, "leaf.git"), "leaf")
	mustGit(t, mid, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-qm", "leaf")
	mustGit(t, mid, "push", "-q", "origin", "HEAD:main")
	top := bare("top")
	mustGit(t, top, "-c", "protocol.file.allow=always", "submodule", "add", "-q", filepath.Join(root, "mid.git"), "mid")
	mustGit(t, top, "-c", "protocol.file.allow=always", "submodule", "update", "-q", "--init", "--recursive")
	mustGit(t, top, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-qm", "mid")
	mustGit(t, top, "push", "-q", "origin", "HEAD:main")
	writeFile(t, filepath.Join(top, "mid", "leaf", "leaf.txt"), []byte("edited deep down\n"))

	path, err := gitops.RemoveClone(ctx, top, filepath.Join(root, "archive"), "top", gitops.RemoveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	meta, _, err := gitops.ReadArchive(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := meta.Submodules["mid/leaf"]; ok || meta.Submodules["mid"] == "" {
		t.Fatalf("the top archive should name mid only: %v", meta.Submodules)
	}
	restored := filepath.Join(root, "restored")
	if err := gitops.RestoreArchive(ctx, path, restored, ""); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(restored, "mid", "leaf", "leaf.txt")); string(got) != "edited deep down\n" {
		t.Errorf("mid/leaf/leaf.txt = %q", got)
	}
}
