package gitops_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/ashtonian/git-mgr/internal/gitops"
)

// copyState is everything a backup must reproduce: status, the staged and
// unstaged changes, and every untracked file's mode and bytes.
type copyState struct {
	status, staged, unstaged string
	untracked                map[string]string
}

func captureState(t *testing.T, dir string) copyState {
	t.Helper()
	s := copyState{
		status:    mustGit(t, dir, "status", "--porcelain=v1", "--untracked-files=all"),
		staged:    mustGit(t, dir, "diff", "--cached", "--binary", "--no-renames"),
		unstaged:  mustGit(t, dir, "diff", "--binary", "--no-renames"),
		untracked: map[string]string{},
	}
	out, err := gitops.GitOutput(context.Background(), dir, "ls-files", "-z", "--others", "--exclude-standard")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00") {
		if name == "" {
			continue
		}
		path := filepath.Join(dir, name)
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			target, _ := os.Readlink(path)
			s.untracked[name] = "symlink to " + target
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		s.untracked[name] = fmt.Sprintf("%v %x", info.Mode().Perm(), sha256.Sum256(data))
	}
	return s
}

func assertSameState(t *testing.T, want, got copyState) {
	t.Helper()
	if got.status != want.status {
		t.Errorf("status differs:\n got %q\nwant %q", got.status, want.status)
	}
	if got.staged != want.staged {
		t.Errorf("staged changes differ:\n got %q\nwant %q", got.staged, want.staged)
	}
	if got.unstaged != want.unstaged {
		t.Errorf("unstaged changes differ:\n got %q\nwant %q", got.unstaged, want.unstaged)
	}
	if fmt.Sprint(got.untracked) != fmt.Sprint(want.untracked) {
		t.Errorf("untracked files differ:\n got %v\nwant %v", got.untracked, want.untracked)
	}
}

// messyRepo returns a repo whose uncommitted state exercises everything a
// backup must keep: one file both staged and unstaged, a new staged file with
// no final newline, a binary edit, a deletion, trailing whitespace, untracked
// files with a non-ASCII name, in a subdirectory, executable and as a symlink,
// and an ignored file that must not be saved.
func messyRepo(t *testing.T) string {
	t.Helper()
	dir := setupTestRepo(t)
	binary := make([]byte, 512)
	for i := range binary {
		binary[i] = byte(i)
	}
	writeFile(t, filepath.Join(dir, "notes.txt"), []byte("a\nb\n"))
	writeFile(t, filepath.Join(dir, "blob.bin"), binary)
	writeFile(t, filepath.Join(dir, "gone.txt"), []byte("delete me\n"))
	writeFile(t, filepath.Join(dir, "zz-last.txt"), []byte("1\n2\n"))
	writeFile(t, filepath.Join(dir, ".gitignore"), []byte("*.log\n"))
	mustGit(t, dir, "add", ".")
	mustGit(t, dir, "commit", "-qm", "fixtures")

	writeFile(t, filepath.Join(dir, "notes.txt"), []byte("a\nstaged\n"))
	writeFile(t, filepath.Join(dir, "staged.txt"), []byte("no final newline"))
	binary[7] ^= 0xff
	writeFile(t, filepath.Join(dir, "blob.bin"), binary)
	writeFile(t, filepath.Join(dir, "zz-staged.txt"), []byte("x\ny\n"))
	mustGit(t, dir, "add", "notes.txt", "staged.txt", "blob.bin", "zz-staged.txt")
	writeFile(t, filepath.Join(dir, "notes.txt"), []byte("a  \nB"))
	writeFile(t, filepath.Join(dir, "zz-last.txt"), []byte("1\n2\n3\n"))
	if err := os.Remove(filepath.Join(dir, "gone.txt")); err != nil {
		t.Fatal(err)
	}

	writeFile(t, filepath.Join(dir, "café notes.txt"), []byte("untracked\n"))
	if err := os.MkdirAll(filepath.Join(dir, "sub", "deeper"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "sub", "deeper", "nested.txt"), []byte("nested\n"))
	if err := os.WriteFile(filepath.Join(dir, "run.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("notes.txt", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "debug.log"), []byte("ignored\n"))
	return dir
}

func resetToHead(t *testing.T, dir string) {
	t.Helper()
	mustGit(t, dir, "reset", "-q", "--hard")
	mustGit(t, dir, "clean", "-qfdx")
}

func TestBackup_CleanRepoSavesNothing(t *testing.T) {
	dir := setupTestRepo(t)
	patchRoot := t.TempDir()

	res, err := gitops.Backup(context.Background(), dir, gitops.NewBackupConfig(gitops.WithPatchDir(patchRoot)))
	if err != nil {
		t.Fatal(err)
	}
	if res.Dirty() || res.ID != "" {
		t.Fatalf("clean repo: %+v", res)
	}
	if refs := mustGit(t, dir, "for-each-ref", "refs/git-mgr/"); refs != "" {
		t.Errorf("clean repo got refs: %s", refs)
	}
	if entries, _ := os.ReadDir(patchRoot); len(entries) != 0 {
		t.Errorf("clean repo got patch backups: %v", entries)
	}
}

// Each storage form must restore the exact state, both in place and into a
// scratch clone.
func TestBackup_RestoresExactly(t *testing.T) {
	ctx := context.Background()
	for _, strategy := range []string{"stash-ref", "patch", "both"} {
		for _, into := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/into=%v", strategy, into), func(t *testing.T) {
				dir := messyRepo(t)
				want := captureState(t, dir)
				patchRoot := t.TempDir()

				res, err := gitops.Backup(ctx, dir, gitops.NewBackupConfig(gitops.WithStrategy(strategy), gitops.WithPatchDir(patchRoot)))
				if err != nil {
					t.Fatal(err)
				}
				if res.Outcome != gitops.BackupSaved || len(res.Skipped) != 0 {
					t.Fatalf("backup: %+v", res)
				}
				assertSameState(t, want, captureState(t, dir)) // backing up changes nothing

				b, err := gitops.FindBackup(ctx, dir, patchRoot, "latest")
				if err != nil {
					t.Fatal(err)
				}
				if b.ID != res.ID {
					t.Fatalf("latest backup = %s, want %s", b.ID, res.ID)
				}
				if into {
					scratch := filepath.Join(t.TempDir(), "scratch")
					if err := gitops.RestoreBackupInto(ctx, dir, b, scratch); err != nil {
						t.Fatal(err)
					}
					assertSameState(t, want, captureState(t, scratch))
					return
				}
				resetToHead(t, dir)
				if err := gitops.ApplyBackup(ctx, dir, b); err != nil {
					t.Fatal(err)
				}
				assertSameState(t, want, captureState(t, dir))
			})
		}
	}
}

// A backup taken while an agent's git command holds index.lock must succeed
// and leave the index and the lock exactly as they were.
func TestBackup_WorksWhileAgentHoldsIndexLock(t *testing.T) {
	dir := messyRepo(t)
	indexPath := filepath.Join(dir, ".git", "index")
	indexBefore, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(dir, ".git", "index.lock")
	writeFile(t, lock, []byte("agent"))

	res, err := gitops.Backup(context.Background(), dir, gitops.NewBackupConfig(gitops.WithPatchDir(t.TempDir())))
	if err != nil {
		t.Fatalf("backup with index.lock held: %v", err)
	}
	if res.Outcome != gitops.BackupSaved {
		t.Fatalf("backup: %+v", res)
	}
	if got, _ := os.ReadFile(lock); string(got) != "agent" {
		t.Error("index.lock was touched")
	}
	if got, _ := os.ReadFile(indexPath); !bytes.Equal(got, indexBefore) {
		t.Error("the index was rewritten")
	}
}

func TestBackup_UnchangedStateIsNotSavedAgain(t *testing.T) {
	ctx := context.Background()
	dir := messyRepo(t)
	patchRoot := t.TempDir()
	clock := newTestClock()
	cfg := gitops.NewBackupConfig(gitops.WithPatchDir(patchRoot), gitops.WithClock(clock.Now))

	first, err := gitops.Backup(ctx, dir, cfg)
	if err != nil || first.Outcome != gitops.BackupSaved {
		t.Fatalf("first backup: %+v, %v", first, err)
	}
	clock.Advance(15 * time.Minute)
	second, err := gitops.Backup(ctx, dir, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if second.Outcome != gitops.BackupUnchanged || second.ID != first.ID {
		t.Fatalf("unchanged state: got %v %s, want unchanged %s", second.Outcome, second.ID, first.ID)
	}

	writeFile(t, filepath.Join(dir, "zz-last.txt"), []byte("changed again\n"))
	clock.Advance(15 * time.Minute)
	third, err := gitops.Backup(ctx, dir, cfg)
	if err != nil || third.Outcome != gitops.BackupSaved || third.ID == first.ID {
		t.Fatalf("changed state: %+v, %v", third, err)
	}
	backups, err := gitops.ListBackups(ctx, dir, patchRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 2 {
		t.Fatalf("want 2 backups, got %d", len(backups))
	}
}

// Linked worktrees share one ref namespace; each must keep its own backups
// even when both are taken in the same instant.
func TestBackup_WorktreesKeepSeparateBackups(t *testing.T) {
	ctx := context.Background()
	main := setupTestRepo(t)
	wt := filepath.Join(t.TempDir(), "agent-1")
	mustGit(t, main, "worktree", "add", "-q", "-b", "agent-1", wt)
	writeFile(t, filepath.Join(main, "README.md"), []byte("main's work\n"))
	writeFile(t, filepath.Join(wt, "README.md"), []byte("agent's work\n"))

	patchRoot := t.TempDir()
	clock := newTestClock()
	cfg := gitops.NewBackupConfig(gitops.WithPatchDir(patchRoot), gitops.WithClock(clock.Now))
	mainRes, err := gitops.Backup(ctx, main, cfg)
	if err != nil {
		t.Fatal(err)
	}
	wtRes, err := gitops.Backup(ctx, wt, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if mainRes.CopyID == wtRes.CopyID || mainRes.Ref == wtRes.Ref {
		t.Fatalf("worktrees share backups: %+v and %+v", mainRes, wtRes)
	}

	for dir, want := range map[string]string{main: "main's work\n", wt: "agent's work\n"} {
		b, err := gitops.FindBackup(ctx, dir, patchRoot, "latest")
		if err != nil {
			t.Fatal(err)
		}
		scratch := filepath.Join(t.TempDir(), "scratch")
		if err := gitops.RestoreBackupInto(ctx, dir, b, scratch); err != nil {
			t.Fatal(err)
		}
		if got, _ := os.ReadFile(filepath.Join(scratch, "README.md")); string(got) != want {
			t.Errorf("%s restored %q, want %q", dir, got, want)
		}
	}
}

// Patch backups outlive the clone: a fresh clone at the same path finds them.
func TestBackup_RecloneFindsEarlierPatchBackups(t *testing.T) {
	ctx := context.Background()
	remote, clone := remoteAndClone(t)
	writeFile(t, filepath.Join(clone, "a.txt"), []byte("lost with the clone\n"))
	patchRoot := t.TempDir()
	res, err := gitops.Backup(ctx, clone, gitops.NewBackupConfig(gitops.WithPatchDir(patchRoot)))
	if err != nil {
		t.Fatal(err)
	}

	if err := os.RemoveAll(clone); err != nil {
		t.Fatal(err)
	}
	mustGit(t, filepath.Dir(clone), "clone", "-q", remote, clone)

	b, err := gitops.FindBackup(ctx, clone, patchRoot, res.ID)
	if err != nil {
		t.Fatalf("finding the earlier copy's backup: %v", err)
	}
	if b.Ref != "" || b.PatchDir == "" {
		t.Fatalf("only the patch backup survives a re-clone: %+v", b)
	}
	if err := gitops.ApplyBackup(ctx, clone, b); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(clone, "a.txt")); string(got) != "lost with the clone\n" {
		t.Errorf("restored a.txt = %q", got)
	}
}

func TestBackup_ReportsSkippedUntrackedPaths(t *testing.T) {
	ctx := context.Background()
	dir := setupTestRepo(t)
	writeFile(t, filepath.Join(dir, "small.txt"), []byte("kept\n"))
	writeFile(t, filepath.Join(dir, "big.bin"), bytes.Repeat([]byte("x"), 2048))
	nested := filepath.Join(dir, "vendor-repo")
	mustGit(t, dir, "init", "-q", nested)
	writeFile(t, filepath.Join(nested, "f"), []byte("x"))
	if os.Geteuid() != 0 {
		writeFile(t, filepath.Join(dir, "secret.txt"), []byte("x"))
		if err := os.Chmod(filepath.Join(dir, "secret.txt"), 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(filepath.Join(dir, "secret.txt"), 0o644) })
	}

	res, err := gitops.Backup(ctx, dir, gitops.NewBackupConfig(
		gitops.WithPatchDir(t.TempDir()), gitops.WithUntrackedLimits(1024, 0)))
	if err != nil {
		t.Fatal(err)
	}
	reasons := map[string]string{}
	for _, s := range res.Skipped {
		reasons[s.Path] = s.Reason
	}
	if !strings.Contains(reasons["big.bin"], "file limit") {
		t.Errorf("big.bin: %q", reasons["big.bin"])
	}
	if reasons["vendor-repo/"] != "nested git repository" {
		t.Errorf("vendor-repo/: %q", reasons["vendor-repo/"])
	}
	if os.Geteuid() != 0 && reasons["secret.txt"] != "unreadable" {
		t.Errorf("secret.txt: %q", reasons["secret.txt"])
	}
	if got := mustGit(t, dir, "show", res.Ref+"^3:small.txt"); got != "kept" {
		t.Errorf("small.txt in snapshot = %q", got)
	}
}

// A conflicted index has no tree of its own; the backup still saves the
// conflicted files as they stand.
func TestBackup_IndexWithConflicts(t *testing.T) {
	ctx := context.Background()
	dir := setupTestRepo(t)
	base := mustGit(t, dir, "symbolic-ref", "--short", "HEAD")
	mustGit(t, dir, "switch", "-qc", "other")
	writeFile(t, filepath.Join(dir, "README.md"), []byte("other\n"))
	mustGit(t, dir, "commit", "-qam", "other")
	mustGit(t, dir, "switch", "-q", base)
	writeFile(t, filepath.Join(dir, "README.md"), []byte("mine\n"))
	mustGit(t, dir, "commit", "-qam", "mine")
	if _, err := gitops.Git(ctx, dir, "merge", "other"); err == nil {
		t.Fatal("merge should conflict")
	}
	conflicted, _ := os.ReadFile(filepath.Join(dir, "README.md"))

	res, err := gitops.Backup(ctx, dir, gitops.NewBackupConfig(gitops.WithPatchDir(t.TempDir())))
	if err != nil {
		t.Fatal(err)
	}
	if !res.IndexUnmerged || res.Outcome != gitops.BackupSaved {
		t.Fatalf("conflicted backup: %+v", res)
	}
	if got := mustGit(t, dir, "show", res.Ref+":README.md"); got != strings.TrimSpace(string(conflicted)) {
		t.Errorf("snapshot README.md = %q, want the conflicted file", got)
	}
}

func TestBackup_UnbornRepo(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	mustGit(t, dir, "init", "-q")
	writeFile(t, filepath.Join(dir, "staged.txt"), []byte("staged\n"))
	mustGit(t, dir, "add", "staged.txt")
	writeFile(t, filepath.Join(dir, "untracked.txt"), []byte("untracked\n"))
	want := captureState(t, dir)
	patchRoot := t.TempDir()

	// No user.email is configured: snapshots carry their own identity.
	res, err := gitops.Backup(ctx, dir, gitops.NewBackupConfig(gitops.WithPatchDir(patchRoot)))
	if err != nil {
		t.Fatal(err)
	}
	b, err := gitops.FindBackup(ctx, dir, patchRoot, res.ID)
	if err != nil {
		t.Fatal(err)
	}
	scratch := filepath.Join(t.TempDir(), "scratch")
	if err := gitops.RestoreBackupInto(ctx, dir, b, scratch); err != nil {
		t.Fatal(err)
	}
	assertSameState(t, want, captureState(t, scratch))

	resetToHead(t, dir)
	if err := gitops.ApplyBackup(ctx, dir, b); err != nil {
		t.Fatal(err)
	}
	assertSameState(t, want, captureState(t, dir))
}

func TestBackup_PushBranch(t *testing.T) {
	ctx := context.Background()
	remote, clone := remoteAndClone(t)
	writeFile(t, filepath.Join(clone, "a.txt"), []byte("wip\n"))
	writeFile(t, filepath.Join(clone, "local-only.txt"), []byte("untracked\n"))
	cfg := gitops.NewBackupConfig(gitops.WithStrategy("stash-ref"), gitops.WithPushBranch("origin", ""))

	res, err := gitops.Backup(ctx, clone, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if res.PushedBranch != "refs/heads/git-mgr/wip/main" {
		t.Fatalf("pushed %q", res.PushedBranch)
	}
	pushed := mustGit(t, remote, "rev-parse", "git-mgr/wip/main")
	if got := mustGit(t, remote, "show", pushed+":a.txt"); got != "wip" {
		t.Errorf("pushed a.txt = %q", got)
	}
	if _, err := gitops.Git(ctx, remote, "show", pushed+":local-only.txt"); err == nil {
		t.Error("untracked files must not be pushed")
	}
	if parents := mustGit(t, remote, "rev-list", "--parents", "-n1", pushed); len(strings.Fields(parents)) != 2 {
		t.Errorf("the WIP commit must have only the base as parent: %s", parents)
	}

	// An unchanged state pushes the same commit again.
	if _, err := gitops.Backup(ctx, clone, cfg); err != nil {
		t.Fatal(err)
	}
	if again := mustGit(t, remote, "rev-parse", "git-mgr/wip/main"); again != pushed {
		t.Errorf("unchanged state pushed a new commit %s, had %s", again, pushed)
	}
}

func TestBackup_RetentionPrunesOldBackups(t *testing.T) {
	ctx := context.Background()
	dir := setupTestRepo(t)
	patchRoot := t.TempDir()
	clock := newTestClock()
	cfg := gitops.NewBackupConfig(gitops.WithPatchDir(patchRoot), gitops.WithClock(clock.Now),
		gitops.WithRetention(gitops.Retention{KeepAll: time.Hour, KeepDaily: 48 * time.Hour}))

	var ids []string
	for i, step := range []time.Duration{0, 10 * time.Minute, 72 * time.Hour} {
		clock.Advance(step)
		writeFile(t, filepath.Join(dir, "README.md"), []byte(fmt.Sprintf("version %d\n", i)))
		res, err := gitops.Backup(ctx, dir, cfg)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, res.ID)
	}

	backups, err := gitops.ListBackups(ctx, dir, patchRoot)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, b := range backups {
		if b.Ref == "" || b.PatchDir == "" {
			t.Errorf("backup %s lost a part: %+v", b.ID, b)
		}
		got = append(got, b.ID)
	}
	if strings.Join(got, ",") != ids[2] {
		t.Errorf("after 72h only the newest backup is within retention; got %v, made %v", got, ids)
	}
}

// A backup that fails part-way leaves no partial directory and prunes nothing.
func TestBackup_FailedPatchKeepsEarlierBackups(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	ctx := context.Background()
	dir := setupTestRepo(t)
	patchRoot := t.TempDir()
	clock := newTestClock()
	cfg := gitops.NewBackupConfig(gitops.WithStrategy("patch"), gitops.WithPatchDir(patchRoot), gitops.WithClock(clock.Now),
		gitops.WithRetention(gitops.Retention{Max: 1}))

	writeFile(t, filepath.Join(dir, "README.md"), []byte("first\n"))
	first, err := gitops.Backup(ctx, dir, cfg)
	if err != nil {
		t.Fatal(err)
	}
	copyDir := filepath.Join(patchRoot, first.CopyID)
	if err := os.Chmod(copyDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(copyDir, 0o700) })

	clock.Advance(time.Hour)
	writeFile(t, filepath.Join(dir, "README.md"), []byte("second\n"))
	res, err := gitops.Backup(ctx, dir, cfg)
	if err == nil {
		t.Fatal("backup into an unwritable directory should fail")
	}
	if res.ID != "" {
		t.Errorf("failed backup reported ID %s", res.ID)
	}
	entries, _ := os.ReadDir(copyDir)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	if strings.Join(names, ",") != first.ID+",copy.json" {
		t.Fatalf("the earlier backup must survive and no partial may remain, got %v", names)
	}
}
