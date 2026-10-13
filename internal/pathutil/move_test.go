package pathutil

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

var stamp = time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)

// tree makes a directory holding what a clone can: nested directories, a
// read-only file, an executable, a symlink and a read-only directory.
func tree(t *testing.T) string {
	t.Helper()
	src := filepath.Join(t.TempDir(), "host", "owner", "repo")
	must(t, os.MkdirAll(filepath.Join(src, ".git", "objects", "pack"), 0o755))
	must(t, os.WriteFile(filepath.Join(src, ".git", "objects", "pack", "p.pack"), []byte("pack"), 0o444))
	must(t, os.WriteFile(filepath.Join(src, "run.sh"), []byte("#!/bin/sh\n"), 0o755))
	must(t, os.Chtimes(filepath.Join(src, "run.sh"), stamp, stamp))
	must(t, os.Symlink("run.sh", filepath.Join(src, "link")))
	must(t, os.MkdirAll(filepath.Join(src, "locked"), 0o755))
	must(t, os.WriteFile(filepath.Join(src, "locked", "f"), []byte("f"), 0o644))
	must(t, os.Chmod(filepath.Join(src, "locked"), 0o555))
	t.Cleanup(func() { os.Chmod(filepath.Join(src, "locked"), 0o755) })
	return src
}

func checkTree(t *testing.T, dst string) {
	t.Helper()
	t.Cleanup(func() { os.Chmod(filepath.Join(dst, "locked"), 0o755) })
	mode := func(rel string) os.FileMode {
		info, err := os.Stat(filepath.Join(dst, rel))
		must(t, err)
		return info.Mode().Perm()
	}
	if m := mode(".git/objects/pack/p.pack"); m != 0o444 {
		t.Errorf("pack mode %v, want 0444", m)
	}
	if m := mode("run.sh"); m != 0o755 {
		t.Errorf("run.sh mode %v, want 0755", m)
	}
	if m := mode("locked"); m != 0o555 {
		t.Errorf("locked mode %v, want 0555", m)
	}
	if info, err := os.Stat(filepath.Join(dst, "run.sh")); err != nil || !info.ModTime().Equal(stamp) {
		t.Errorf("run.sh's modification time wasn't kept: %v %v", info.ModTime(), err)
	}
	if link, err := os.Readlink(filepath.Join(dst, "link")); err != nil || link != "run.sh" {
		t.Errorf("link = %q, %v; want run.sh", link, err)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func gone(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Errorf("%s should be gone: %v", path, err)
	}
}

func entries(t *testing.T, dir string) int {
	t.Helper()
	list, err := os.ReadDir(dir)
	must(t, err)
	return len(list)
}

func TestMove(t *testing.T) {
	src := tree(t)
	dst := filepath.Join(t.TempDir(), "archive", "repo-1")
	must(t, Move(src, dst))
	checkTree(t, dst)
	gone(t, src)
}

func TestMove_RefusesAnExistingDestination(t *testing.T) {
	src := tree(t)
	if err := Move(src, t.TempDir()); err == nil {
		t.Fatal("Move must refuse a destination that exists")
	}
	if _, err := os.Stat(filepath.Join(src, "run.sh")); err != nil {
		t.Errorf("the source must stay: %v", err)
	}
}

func exdev(old, new string) error {
	return &os.LinkError{Op: "rename", Old: old, New: new, Err: syscall.EXDEV}
}

// Across filesystems the tree is copied, whole, and only then deleted.
func TestMove_AcrossFilesystemsCopies(t *testing.T) {
	src := tree(t)
	parent := filepath.Join(t.TempDir(), "archive")
	dst := filepath.Join(parent, "repo-1")
	must(t, move(src, dst, exdev))
	checkTree(t, dst)
	gone(t, src)
	if n := entries(t, parent); n != 1 {
		t.Errorf("%d entries beside the destination; a staging directory was left behind", n)
	}
}

// A copy that fails leaves the source whole and nothing at the destination.
func TestMove_FailedCopyKeepsTheSource(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads unreadable files")
	}
	src := tree(t)
	must(t, os.WriteFile(filepath.Join(src, "secret"), []byte("s"), 0o000))
	parent := filepath.Join(t.TempDir(), "archive")

	if err := move(src, filepath.Join(parent, "repo-1"), exdev); err == nil {
		t.Fatal("copying an unreadable file must fail")
	}
	if _, err := os.Stat(filepath.Join(src, "run.sh")); err != nil {
		t.Errorf("the source must stay whole: %v", err)
	}
	if n := entries(t, parent); n != 0 {
		t.Errorf("a failed copy left %d entries behind", n)
	}
}
