package gitops_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ashtonian/git-mgr/internal/gitops"
)

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gitops.Git(context.Background(), dir, args...)
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return out
}

// patchDirFor finds the single published patch backup for repo under patchRoot.
func patchDirFor(t *testing.T, patchRoot, repo string) string {
	t.Helper()
	base := filepath.Join(patchRoot, filepath.Base(filepath.Dir(repo)), filepath.Base(repo))
	entries, err := os.ReadDir(base)
	if err != nil {
		t.Fatalf("reading %s: %v", base, err)
	}
	if len(entries) != 1 {
		t.Fatalf("want one backup in %s, got %d", base, len(entries))
	}
	return filepath.Join(base, entries[0].Name())
}

// Saved patches must reproduce the working tree and index byte for byte,
// including text without a final newline, trailing whitespace and binary data.
func TestBackup_PatchesRestoreExactly(t *testing.T) {
	dir := setupTestRepo(t)
	ctx := context.Background()

	writeFile(t, filepath.Join(dir, "notes.txt"), []byte("a\nb\n"))
	binary := make([]byte, 512)
	for i := range binary {
		binary[i] = byte(i)
	}
	writeFile(t, filepath.Join(dir, "blob.bin"), binary)
	writeFile(t, filepath.Join(dir, "zz-last.txt"), []byte("1\n2\n"))
	mustGit(t, dir, "add", ".")
	mustGit(t, dir, "commit", "-m", "fixtures")

	// Staged: a new file without a final newline and a binary edit.
	writeFile(t, filepath.Join(dir, "staged.txt"), []byte("no final newline"))
	binary[7] ^= 0xff
	writeFile(t, filepath.Join(dir, "blob.bin"), binary)
	// Each patch ends with a newline-terminated line (zz-* sort last), which
	// is what trimming breaks.
	writeFile(t, filepath.Join(dir, "zz-staged.txt"), []byte("x\ny\n"))
	mustGit(t, dir, "add", "staged.txt", "blob.bin", "zz-staged.txt")
	// Unstaged: trailing whitespace, a changed last line, an appended line.
	writeFile(t, filepath.Join(dir, "notes.txt"), []byte("a  \nB"))
	writeFile(t, filepath.Join(dir, "zz-last.txt"), []byte("1\n2\n3\n"))

	want := map[string][]byte{}
	for _, f := range []string{"staged.txt", "blob.bin", "notes.txt", "zz-staged.txt", "zz-last.txt"} {
		b, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil {
			t.Fatal(err)
		}
		want[f] = b
	}
	wantIndex := mustGit(t, dir, "diff", "--cached", "--binary")

	patchRoot := t.TempDir()
	res, err := gitops.Backup(ctx, dir, gitops.NewBackupConfig(gitops.WithStrategy("patch"), gitops.WithPatchDir(patchRoot)))
	if err != nil || !res.HasPatch {
		t.Fatalf("backup: err=%v hasPatch=%v", err, res.HasPatch)
	}
	saved := patchDirFor(t, patchRoot, dir)

	// Back to HEAD, then rebuild the index and tree from the patches alone.
	mustGit(t, dir, "reset", "--hard", "-q")
	mustGit(t, dir, "clean", "-fdq")
	mustGit(t, dir, "apply", "--index", filepath.Join(saved, "staged.patch"))
	mustGit(t, dir, "apply", filepath.Join(saved, "unstaged.patch"))

	for f, b := range want {
		got, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, b) {
			t.Errorf("%s differs after restore:\n got %q\nwant %q", f, got, b)
		}
	}
	if got := mustGit(t, dir, "diff", "--cached", "--binary"); got != wantIndex {
		t.Errorf("index differs after restore")
	}
}

// A backup that fails part-way must leave no partial directory and must not
// prune the earlier backups it would otherwise replace.
func TestBackup_FailedPatchKeepsEarlierBackups(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read the unreadable file")
	}
	dir := setupTestRepo(t)
	ctx := context.Background()
	patchRoot := t.TempDir()
	base := filepath.Join(patchRoot, filepath.Base(filepath.Dir(dir)), filepath.Base(dir))
	for _, ts := range []string{"20260101T000000Z", "20260102T000000Z"} {
		if err := os.MkdirAll(filepath.Join(base, ts), 0o700); err != nil {
			t.Fatal(err)
		}
	}

	// An untracked file tar cannot read makes the patch backup fail.
	locked := filepath.Join(dir, "secret.txt")
	writeFile(t, locked, []byte("x"))
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o644) })

	cfg := gitops.NewBackupConfig(gitops.WithStrategy("patch"), gitops.WithPatchDir(patchRoot), gitops.WithMaxPerRepo(2))
	if _, err := gitops.Backup(ctx, dir, cfg); err == nil {
		t.Fatal("backup of an unreadable untracked file should fail")
	}

	entries, err := os.ReadDir(base)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if strings.Join(names, ",") != "20260101T000000Z,20260102T000000Z" {
		t.Fatalf("earlier backups must survive and no partial may remain, got %v", names)
	}
}
