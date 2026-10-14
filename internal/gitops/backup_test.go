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

	"github.com/ashtonian/gitstead/internal/gitops"
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
	if refs := mustGit(t, dir, "for-each-ref", "refs/gitstead/"); refs != "" {
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

// An edit that keeps a file's size, made in the same second as git's last
// write of the index, leaves the index's cached stat data matching. git sees
// the edit only because the index is not older than the file; a backup must
// see it too.
func TestBackup_SeesSameSizeEditInTheIndexsSecond(t *testing.T) {
	dir := setupTestRepo(t)
	file := filepath.Join(dir, "f")
	writeFile(t, file, []byte("a\nb\n"))
	mustGit(t, dir, "add", "f")
	mustGit(t, dir, "commit", "-qm", "base")
	// Compare only what timestamps can pin down, so the fixture doesn't
	// depend on when it runs.
	mustGit(t, dir, "config", "core.trustctime", "false")
	mustGit(t, dir, "config", "core.checkstat", "minimal")

	stamp := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := os.Chtimes(file, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	mustGit(t, dir, "update-index", "--refresh")
	if err := os.Chtimes(filepath.Join(dir, ".git", "index"), stamp, stamp); err != nil {
		t.Fatal(err)
	}
	writeFile(t, file, []byte("a\nB\n"))
	if err := os.Chtimes(file, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if gitops.GitExitCode(context.Background(), dir, "diff", "--quiet") != 1 {
		t.Fatal("fixture: git doesn't see the edit")
	}

	snap, err := gitops.TakeSnapshot(context.Background(), dir, gitops.SnapshotLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if got := mustGit(t, dir, "show", snap.Commit+":f"); got != "a\nB" {
		t.Errorf("snapshot holds %q, want the edit", got)
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

// superWithSubmodule returns a clone with a submodule checked out at lib.
func superWithSubmodule(t *testing.T) (super, lib string) {
	t.Helper()
	root := t.TempDir()
	subRemote := filepath.Join(root, "lib.git")
	mustGit(t, root, "init", "-q", "--bare", "-b", "main", subRemote)
	seed := filepath.Join(root, "seed")
	mustGit(t, root, "clone", "-q", subRemote, seed)
	writeFile(t, filepath.Join(seed, "lib.txt"), []byte("lib\n"))
	mustGit(t, seed, "add", ".")
	mustGit(t, seed, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-qm", "lib")
	mustGit(t, seed, "push", "-q", "origin", "HEAD:main")

	super = filepath.Join(root, "super")
	mustGit(t, root, "init", "-q", "-b", "main", super)
	writeFile(t, filepath.Join(super, "app.txt"), []byte("app\n"))
	mustGit(t, super, "-c", "protocol.file.allow=always", "submodule", "add", "-q", subRemote, "lib")
	mustGit(t, super, "add", ".")
	mustGit(t, super, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-qm", "super")
	return super, filepath.Join(super, "lib")
}

// Work inside a submodule is backed up with its parent's and restored with
// it, whichever form the backup takes, even when the parent has none of its
// own.
func TestBackup_RestoresSubmoduleWork(t *testing.T) {
	ctx := context.Background()
	for _, strategy := range []string{"stash-ref", "patch", "both"} {
		for _, parentDirty := range []bool{false, true} {
			for _, into := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/parent dirty=%v/into=%v", strategy, parentDirty, into), func(t *testing.T) {
					super, lib := superWithSubmodule(t)
					writeFile(t, filepath.Join(lib, "lib.txt"), []byte("edited in the submodule\n"))
					writeFile(t, filepath.Join(lib, "new.txt"), []byte("untracked in the submodule\n"))
					if parentDirty {
						writeFile(t, filepath.Join(super, "app.txt"), []byte("edited\n"))
					}
					wantSuper, wantLib := captureState(t, super), captureState(t, lib)
					patchRoot := t.TempDir()
					cfg := gitops.NewBackupConfig(gitops.WithStrategy(strategy), gitops.WithPatchDir(patchRoot))

					res, err := gitops.Backup(ctx, super, cfg)
					if err != nil {
						t.Fatal(err)
					}
					if res.Outcome != gitops.BackupSaved || res.Submodules["lib"] == nil || res.Submodules["lib"].Outcome != gitops.BackupSaved {
						t.Fatalf("backup: %+v", res)
					}
					if again, err := gitops.Backup(ctx, super, cfg); err != nil || again.Outcome != gitops.BackupUnchanged {
						t.Fatalf("an unchanged state was saved again: %+v, %v", again, err)
					}
					b, err := gitops.FindBackup(ctx, super, patchRoot, "latest")
					if err != nil {
						t.Fatal(err)
					}
					if into {
						scratch := filepath.Join(t.TempDir(), "scratch")
						if err := gitops.RestoreBackupInto(ctx, super, b, scratch); err != nil {
							t.Fatal(err)
						}
						assertSameState(t, wantLib, captureState(t, filepath.Join(scratch, "lib")))
						if parentDirty {
							if got, _ := os.ReadFile(filepath.Join(scratch, "app.txt")); string(got) != "edited\n" {
								t.Errorf("restored app.txt = %q", got)
							}
						}
						return
					}
					resetToHead(t, lib)
					resetToHead(t, super)
					if err := gitops.ApplyBackup(ctx, super, b); err != nil {
						t.Fatal(err)
					}
					assertSameState(t, wantLib, captureState(t, lib))
					assertSameState(t, wantSuper, captureState(t, super))
				})
			}
		}
	}
}

// A submodule with work inside one whose status ignores it, so with no backup
// of its own, is restored --into a clone of that one, at the commit its
// parent records.
func TestRestoreBackupInto_NestedInASubmoduleWithoutABackup(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	seed := func(name string) string {
		remote := filepath.Join(root, name+".git")
		mustGit(t, root, "init", "-q", "--bare", "-b", "main", remote)
		dir := filepath.Join(root, "seed-"+name)
		mustGit(t, root, "clone", "-q", remote, dir)
		writeFile(t, filepath.Join(dir, name+".txt"), []byte(name+"\n"))
		mustGit(t, dir, "add", ".")
		mustGit(t, dir, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-qm", name)
		return dir
	}
	leaf := seed("leaf")
	mustGit(t, leaf, "push", "-q", "origin", "HEAD:main")
	mid := seed("mid")
	mustGit(t, mid, "-c", "protocol.file.allow=always", "submodule", "add", "-q", filepath.Join(root, "leaf.git"), "leaf")
	mustGit(t, mid, "config", "-f", ".gitmodules", "submodule.leaf.ignore", "all")
	mustGit(t, mid, "add", ".gitmodules")
	mustGit(t, mid, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-qm", "leaf")
	mustGit(t, mid, "push", "-q", "origin", "HEAD:main")
	top := seed("top")
	mustGit(t, top, "-c", "protocol.file.allow=always", "submodule", "add", "-q", filepath.Join(root, "mid.git"), "mid")
	mustGit(t, top, "-c", "protocol.file.allow=always", "submodule", "update", "-q", "--init", "--recursive")
	mustGit(t, top, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-qm", "mid")
	writeFile(t, filepath.Join(top, "mid", "leaf", "leaf.txt"), []byte("edited deep down\n"))
	midCommit := mustGit(t, filepath.Join(top, "mid"), "rev-parse", "HEAD")

	patchRoot := t.TempDir()
	res, err := gitops.Backup(ctx, top, gitops.NewBackupConfig(gitops.WithPatchDir(patchRoot)))
	if err != nil {
		t.Fatal(err)
	}
	if res.Submodules["mid/leaf"] == nil || res.Submodules["mid"] != nil {
		t.Fatalf("want a backup of mid/leaf and none of mid: %+v", res.Submodules)
	}
	b, err := gitops.FindBackup(ctx, top, patchRoot, "latest")
	if err != nil {
		t.Fatal(err)
	}
	scratch := filepath.Join(t.TempDir(), "scratch")
	if err := gitops.RestoreBackupInto(ctx, top, b, scratch); err != nil {
		t.Fatal(err)
	}
	if got := mustGit(t, filepath.Join(scratch, "mid"), "rev-parse", "HEAD"); got != midCommit {
		t.Errorf("mid is at %s, want %s", got, midCommit)
	}
	if got, _ := os.ReadFile(filepath.Join(scratch, "mid", "mid.txt")); string(got) != "mid\n" {
		t.Errorf("mid/mid.txt = %q; mid should be checked out", got)
	}
	if got, _ := os.ReadFile(filepath.Join(scratch, "mid", "leaf", "leaf.txt")); string(got) != "edited deep down\n" {
		t.Errorf("mid/leaf/leaf.txt = %q", got)
	}
}

// A submodule's new work makes a new backup of its parent, even when the
// parent's own work is unchanged.
func TestBackup_SubmoduleChangeIsANewBackup(t *testing.T) {
	ctx := context.Background()
	super, lib := superWithSubmodule(t)
	clock := newTestClock()
	cfg := gitops.NewBackupConfig(gitops.WithPatchDir(t.TempDir()), gitops.WithClock(clock.Now))
	writeFile(t, filepath.Join(lib, "lib.txt"), []byte("one\n"))
	first, err := gitops.Backup(ctx, super, cfg)
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(time.Minute)
	writeFile(t, filepath.Join(lib, "lib.txt"), []byte("two\n"))
	second, err := gitops.Backup(ctx, super, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if second.Outcome != gitops.BackupSaved || second.ID == first.ID {
		t.Fatalf("second backup: %+v, first %s", second, first.ID)
	}
}

// A parent backup names the submodule backups it restores, so they are kept
// as long as it is: a run whose parent backup fails after saving the
// submodule's leaves the previous backup restorable, and a later run prunes
// what no parent backup names.
func TestBackup_SubmoduleBackupsLiveAsLongAsTheirParents(t *testing.T) {
	ctx := context.Background()
	super, lib := superWithSubmodule(t)
	clock := newTestClock()
	patchRoot := t.TempDir()
	cfg := gitops.NewBackupConfig(gitops.WithStrategy("patch"), gitops.WithPatchDir(patchRoot), gitops.WithClock(clock.Now),
		gitops.WithRetention(gitops.Retention{Max: 1}))

	writeFile(t, filepath.Join(lib, "lib.txt"), []byte("first\n"))
	first, err := gitops.Backup(ctx, super, cfg)
	if err != nil {
		t.Fatal(err)
	}

	// The next run saves the submodule's new state, then fails to write the
	// parent's: its patch folder is read-only.
	clock.Advance(time.Minute)
	writeFile(t, filepath.Join(lib, "lib.txt"), []byte("second\n"))
	parentDir := filepath.Join(patchRoot, first.CopyID)
	if err := os.Chmod(parentDir, 0o500); err != nil {
		t.Fatal(err)
	}
	second, err := gitops.Backup(ctx, super, cfg)
	if err == nil || second.Submodules["lib"] == nil || second.Submodules["lib"].Outcome != gitops.BackupSaved {
		t.Fatalf("want the submodule saved and the parent failed: %+v, %v", second, err)
	}
	if err := os.Chmod(parentDir, 0o700); err != nil {
		t.Fatal(err)
	}

	b, err := gitops.FindBackup(ctx, super, patchRoot, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	scratch := filepath.Join(t.TempDir(), "scratch")
	if err := gitops.RestoreBackupInto(ctx, super, b, scratch); err != nil {
		t.Fatalf("the earlier backup is no longer restorable: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(scratch, "lib", "lib.txt")); string(got) != "first\n" {
		t.Errorf("restored lib.txt = %q", got)
	}

	// A successful run replaces the parent's backup; the submodule backup
	// only the old one named goes with it.
	clock.Advance(time.Minute)
	third, err := gitops.Backup(ctx, super, cfg)
	if err != nil {
		t.Fatal(err)
	}
	subBackups, err := gitops.ListBackups(ctx, lib, patchRoot)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, sb := range subBackups {
		ids = append(ids, sb.ID)
	}
	if want := third.Submodules["lib"].ID; len(ids) != 1 || ids[0] != want {
		t.Errorf("submodule backups = %v, want only %s", ids, want)
	}
}
