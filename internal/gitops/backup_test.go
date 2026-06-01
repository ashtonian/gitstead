package gitops_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ashtonian/git-mgr/internal/gitops"
)

func TestBackup_CleanRepo(t *testing.T) {
	dir := setupTestRepo(t)
	ctx := context.Background()

	cfg := gitops.NewBackupConfig(
		gitops.WithPatchDir(t.TempDir()),
	)
	result, err := gitops.Backup(ctx, dir, cfg)
	if err != nil {
		t.Fatalf("backup failed: %v", err)
	}
	if result.IsDirty {
		t.Error("expected clean repo to not be dirty")
	}
}

func TestBackup_StashRef(t *testing.T) {
	dir := setupTestRepo(t)
	ctx := context.Background()

	// Make repo dirty
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("# modified"), 0644)

	cfg := gitops.NewBackupConfig(
		gitops.WithStrategy("stash-ref"),
		gitops.WithPatchDir(t.TempDir()),
	)
	result, err := gitops.Backup(ctx, dir, cfg)
	if err != nil {
		t.Fatalf("backup failed: %v", err)
	}
	if !result.IsDirty {
		t.Error("expected dirty result")
	}
	if result.RefName == "" {
		t.Error("expected stash ref to be created")
	}

	// Verify ref exists
	refs, err := gitops.ListBackupRefs(ctx, dir)
	if err != nil {
		t.Fatalf("listing refs failed: %v", err)
	}
	if len(refs) == 0 {
		t.Error("expected at least one backup ref")
	}

	// Verify working tree was NOT modified
	out, _ := gitops.Git(ctx, dir, "diff", "--stat")
	if out == "" {
		t.Error("working tree should still be dirty after backup")
	}
}

func TestBackup_PatchFiles(t *testing.T) {
	dir := setupTestRepo(t)
	ctx := context.Background()
	patchDir := t.TempDir()

	// Make staged + unstaged changes
	os.WriteFile(filepath.Join(dir, "staged.txt"), []byte("staged content"), 0644)
	gitops.Git(ctx, dir, "add", "staged.txt")
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("# unstaged change"), 0644)

	// Create an untracked file
	os.WriteFile(filepath.Join(dir, "untracked.txt"), []byte("untracked"), 0644)

	cfg := gitops.NewBackupConfig(
		gitops.WithStrategy("patch"),
		gitops.WithPatchDir(patchDir),
	)
	result, err := gitops.Backup(ctx, dir, cfg)
	if err != nil {
		t.Fatalf("backup failed: %v", err)
	}
	if !result.IsDirty {
		t.Error("expected dirty result")
	}
	if !result.HasPatch {
		t.Error("expected patch result")
	}

	// Find the created patch directory
	entries, _ := os.ReadDir(patchDir)
	if len(entries) == 0 {
		t.Fatal("expected patch directory to be created")
	}

	// Walk to the timestamp dir
	var found string
	filepath.WalkDir(patchDir, func(path string, d os.DirEntry, err error) error {
		if d != nil && d.Name() == "metadata.json" {
			found = filepath.Dir(path)
		}
		return nil
	})
	if found == "" {
		t.Fatal("expected metadata.json in patch dir")
	}

	// Verify patch files exist
	for _, name := range []string{"staged.patch", "unstaged.patch", "untracked.list", "metadata.json"} {
		if _, err := os.Stat(filepath.Join(found, name)); err != nil {
			t.Errorf("expected %s to exist: %v", name, err)
		}
	}
}

func TestBackup_SkipsLockedRepo(t *testing.T) {
	dir := setupTestRepo(t)
	ctx := context.Background()

	os.WriteFile(filepath.Join(dir, "README.md"), []byte("# dirty"), 0644)
	os.WriteFile(filepath.Join(dir, ".git", "index.lock"), []byte(""), 0644)

	cfg := gitops.NewBackupConfig(gitops.WithPatchDir(t.TempDir()))
	result, err := gitops.Backup(ctx, dir, cfg)
	if err != nil {
		t.Fatalf("backup should not error on locked repo: %v", err)
	}
	if result.IsDirty {
		t.Error("locked repo should be skipped, not marked dirty")
	}
}

func TestBackup_Both(t *testing.T) {
	dir := setupTestRepo(t)
	ctx := context.Background()

	os.WriteFile(filepath.Join(dir, "README.md"), []byte("# both test"), 0644)

	cfg := gitops.NewBackupConfig(
		gitops.WithStrategy("both"),
		gitops.WithPatchDir(t.TempDir()),
	)
	result, err := gitops.Backup(ctx, dir, cfg)
	if err != nil {
		t.Fatalf("backup failed: %v", err)
	}
	if !result.IsDirty {
		t.Error("expected dirty")
	}
	if result.RefName == "" {
		t.Error("expected stash ref from 'both' strategy")
	}
	if !result.HasPatch {
		t.Error("expected patches from 'both' strategy")
	}
}

func TestBackup_PushBranch(t *testing.T) {
	ctx := context.Background()

	// A bare repo acts as the "remote"; a local-path remote needs no SSH key.
	remote := t.TempDir()
	if _, err := gitops.Git(ctx, remote, "init", "--bare"); err != nil {
		t.Fatalf("init bare: %v", err)
	}

	dir := setupTestRepo(t)
	if _, err := gitops.Git(ctx, dir, "remote", "add", "origin", remote); err != nil {
		t.Fatalf("remote add: %v", err)
	}

	// Dirty the working tree with a tracked change.
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("# wip changes"), 0644)

	cfg := gitops.NewBackupConfig(
		gitops.WithStrategy("stash-ref"),
		gitops.WithPatchDir(t.TempDir()),
		gitops.WithPushBranch("origin", ""),
	)
	result, err := gitops.Backup(ctx, dir, cfg)
	if err != nil {
		t.Fatalf("backup failed: %v", err)
	}
	if result.PushedBranch == "" {
		t.Fatal("expected a WIP branch to be pushed")
	}
	if !strings.HasPrefix(result.PushedBranch, "refs/heads/git-mgr/wip/") {
		t.Errorf("unexpected pushed ref: %s", result.PushedBranch)
	}

	// The branch must actually exist on the remote.
	ls, err := gitops.Git(ctx, dir, "ls-remote", "origin", "refs/heads/git-mgr/wip/*")
	if err != nil {
		t.Fatalf("ls-remote: %v", err)
	}
	if !strings.Contains(ls, "git-mgr/wip/") {
		t.Errorf("WIP branch not found on remote: %q", ls)
	}

	// Re-running force-updates the same ref (no "already exists" failure).
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("# wip changes v2"), 0644)
	if _, err := gitops.Backup(ctx, dir, cfg); err != nil {
		t.Fatalf("second push-branch backup failed: %v", err)
	}

	// Working tree must remain dirty — backup never touches it.
	if out, _ := gitops.Git(ctx, dir, "diff", "--stat"); out == "" {
		t.Error("working tree should still be dirty after push-branch backup")
	}
}

func TestBackup_Pruning(t *testing.T) {
	dir := setupTestRepo(t)
	ctx := context.Background()

	cfg := gitops.NewBackupConfig(
		gitops.WithStrategy("stash-ref"),
		gitops.WithMaxPerRepo(2),
		gitops.WithPatchDir(t.TempDir()),
	)

	// Create 3 backups
	for i := 0; i < 3; i++ {
		os.WriteFile(filepath.Join(dir, "README.md"), []byte("# version "+string(rune('0'+i))), 0644)
		_, err := gitops.Backup(ctx, dir, cfg)
		if err != nil {
			t.Fatalf("backup %d failed: %v", i, err)
		}
	}

	refs, _ := gitops.ListBackupRefs(ctx, dir)
	if len(refs) > 2 {
		t.Errorf("expected at most 2 refs after pruning, got %d", len(refs))
	}
}
