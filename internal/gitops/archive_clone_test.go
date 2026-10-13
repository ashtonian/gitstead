package gitops_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ashtonian/git-mgr/internal/gitops"
)

// The whole clone is moved, nested repository and all, and described beside.
func TestArchiveClone_MovesTheWholeClone(t *testing.T) {
	ctx := context.Background()
	_, clone := remoteAndClone(t)
	commitFile(t, clone, "unpushed.txt", "u\n")
	writeFile(t, filepath.Join(clone, "notes.txt"), []byte("untracked\n"))
	nested := filepath.Join(clone, "vendor-repo")
	mustGit(t, clone, "init", "-q", nested)
	writeFile(t, filepath.Join(nested, "only-copy.txt"), []byte("x"))
	tip := mustGit(t, clone, "rev-parse", "HEAD")

	dir := filepath.Join(t.TempDir(), "archive")
	dest, err := gitops.ArchiveClone(ctx, clone, dir, "github.com/acme/app")
	if err != nil {
		t.Fatalf("ArchiveClone: %v", err)
	}
	if filepath.Dir(dest) != dir || !strings.HasPrefix(filepath.Base(dest), "github.com_acme_app-") {
		t.Errorf("archived to %s, want github.com_acme_app-<time> in %s", dest, dir)
	}
	if _, err := os.Lstat(clone); !os.IsNotExist(err) {
		t.Errorf("the clone should have moved: %v", err)
	}
	if got := mustGit(t, dest, "rev-parse", "HEAD"); got != tip {
		t.Errorf("archived HEAD = %s, want %s", got, tip)
	}
	for _, f := range []string{"notes.txt", "vendor-repo/only-copy.txt"} {
		if _, err := os.Stat(filepath.Join(dest, f)); err != nil {
			t.Errorf("%s didn't move with the clone: %v", f, err)
		}
	}

	meta, id, err := gitops.ReadArchive(dest)
	if err != nil {
		t.Fatalf("ReadArchive: %v", err)
	}
	if id != filepath.Base(dest) || meta.Clone != dest || meta.Source != clone || meta.Bundle != "" {
		t.Errorf("id %s, meta %+v", id, meta)
	}
	if !strings.HasPrefix(meta.Work, "the whole clone, with ") || !strings.Contains(meta.Work, "1 unpushed commit") {
		t.Errorf("work = %q", meta.Work)
	}
	if meta.Remotes["origin"] == "" || !strings.Contains(meta.Restore, "git-mgr archives restore "+id) {
		t.Errorf("remotes %v, restore %q", meta.Remotes, meta.Restore)
	}
}

func TestArchiveClone_KeepsACloneWithWorktrees(t *testing.T) {
	ctx := context.Background()
	repo := setupTestRepo(t)
	wt := filepath.Join(t.TempDir(), "agent-1")
	if err := gitops.WorktreeAdd(ctx, repo, wt, "agent-1", ""); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if _, err := gitops.ArchiveClone(ctx, repo, dir, "repo"); !errors.Is(err, gitops.ErrHasWorktrees) {
		t.Fatalf("ArchiveClone = %v, want ErrHasWorktrees", err)
	}
	if !gitops.HasGitDir(repo) || !gitops.HasGitDir(wt) {
		t.Fatal("the clone or its worktree moved")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("a refused archive left %d files behind", len(entries))
	}
}

func TestArchiveClone_AlreadyGone(t *testing.T) {
	dest, err := gitops.ArchiveClone(context.Background(), filepath.Join(t.TempDir(), "gone"), t.TempDir(), "x")
	if err != nil || dest != "" {
		t.Fatalf("a clone already gone has nothing to archive: dest=%q err=%v", dest, err)
	}
}

func TestDeleteArchive(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	_, clone := remoteAndClone(t)
	dest, err := gitops.ArchiveClone(ctx, clone, dir, "clone")
	if err != nil {
		t.Fatal(err)
	}
	_, other := remoteAndClone(t)
	writeFile(t, filepath.Join(other, "notes.txt"), []byte("untracked\n"))
	bundle, err := gitops.RemoveClone(ctx, other, dir, "bundle", gitops.RemoveOptions{})
	if err != nil || bundle == "" {
		t.Fatalf("RemoveClone = %q, %v", bundle, err)
	}

	for _, path := range []string{dest, bundle} {
		if err := gitops.DeleteArchive(path); err != nil {
			t.Fatalf("DeleteArchive(%s): %v", path, err)
		}
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("left behind: %v", names)
	}
	if err := gitops.DeleteArchive(dest); err != nil {
		t.Errorf("deleting an archive already gone: %v", err)
	}
}

// A description naming files elsewhere never gets them deleted.
func TestDeleteArchive_OnlyDeletesItsOwnFiles(t *testing.T) {
	dir, elsewhere := t.TempDir(), t.TempDir()
	victim := filepath.Join(elsewhere, "keep.bundle")
	writeFile(t, victim, []byte("x"))
	writeFile(t, filepath.Join(dir, "a.json"), []byte(`{"submodules": {"sub": "`+victim+`"}}`))
	if err := gitops.DeleteArchive(filepath.Join(dir, "a.json")); err == nil {
		t.Error("DeleteArchive must refuse a submodule archive outside its own directory")
	}
	if _, err := os.Stat(victim); err != nil {
		t.Errorf("a file outside the archive was deleted: %v", err)
	}
}

// A catalog entry pointing at a directory that isn't an archived clone --
// recorded wrongly, or hand-edited -- never gets it deleted.
func TestDeleteArchive_LeavesADirectoryNotDescribedAsAClone(t *testing.T) {
	_, clone := remoteAndClone(t)
	if err := gitops.DeleteArchive(clone); err == nil {
		t.Fatal("DeleteArchive must refuse a directory with no description naming it")
	}
	if _, err := os.Stat(filepath.Join(clone, "a.txt")); err != nil {
		t.Fatalf("the working copy was deleted: %v", err)
	}
}

func TestArchiveClone_RefusesASymlink(t *testing.T) {
	_, clone := remoteAndClone(t)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(clone, link); err != nil {
		t.Fatal(err)
	}
	if _, err := gitops.ArchiveClone(context.Background(), link, t.TempDir(), "x"); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("ArchiveClone of a symlink = %v", err)
	}
	if _, err := os.Lstat(link); err != nil {
		t.Error("the link moved")
	}
}
