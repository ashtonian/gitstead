package pathutil

import (
	"errors"
	"io/fs"
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
	must(t, move(src, dst, fsOps{rename: exdev, syncDir: SyncDir}))
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

	if err := move(src, filepath.Join(parent, "repo-1"), fsOps{rename: exdev, syncDir: SyncDir}); err == nil {
		t.Fatal("copying an unreadable file must fail")
	}
	if _, err := os.Stat(filepath.Join(src, "run.sh")); err != nil {
		t.Errorf("the source must stay whole: %v", err)
	}
	if n := entries(t, parent); n != 0 {
		t.Errorf("a failed copy left %d entries behind", n)
	}
}

// A rename made but not synced is a move all the same: the tree is at the
// destination, and the error says so.
func TestMove_UnsyncedRenameIsAMove(t *testing.T) {
	src := tree(t)
	dst := filepath.Join(t.TempDir(), "archive", "repo-1")
	eio := &os.PathError{Op: "sync", Path: filepath.Dir(dst), Err: syscall.EIO}
	err := move(src, dst, fsOps{rename: RenameNoReplace, syncDir: func(string) error { return eio }})
	var moved *MovedError
	if !errors.As(err, &moved) || moved.Dst != dst || !errors.Is(err, syscall.EIO) {
		t.Fatalf("move = %v, want a *MovedError to %s", err, dst)
	}
	checkTree(t, dst)
	gone(t, src)
}

// Across filesystems, a copy that can't be made durable is undone before the
// source is touched.
func TestMove_UnsyncedCopyKeepsTheSource(t *testing.T) {
	src := tree(t)
	parent := filepath.Join(t.TempDir(), "archive")
	eio := &os.PathError{Op: "sync", Path: parent, Err: syscall.EIO}
	err := move(src, filepath.Join(parent, "repo-1"), fsOps{rename: exdev, syncDir: func(string) error { return eio }})
	var moved *MovedError
	if err == nil || errors.As(err, &moved) {
		t.Fatalf("move = %v, want a plain failure", err)
	}
	if _, err := os.Stat(filepath.Join(src, "run.sh")); err != nil {
		t.Errorf("the source must stay whole: %v", err)
	}
	if n := entries(t, parent); n != 0 {
		t.Errorf("an undone copy left %d entries behind", n)
	}
}

// MkdirAll makes every missing directory, and leaves existing ones be.
func TestMkdirAll(t *testing.T) {
	root := t.TempDir()
	deep := filepath.Join(root, "a", "b", "c")
	must(t, MkdirAll(deep, 0o700))
	if info, err := os.Stat(deep); err != nil || !info.IsDir() {
		t.Fatalf("%s not made: %v", deep, err)
	}
	must(t, MkdirAll(deep, 0o700))
	must(t, os.WriteFile(filepath.Join(root, "file"), nil, 0o600))
	if err := MkdirAll(filepath.Join(root, "file", "x"), 0o700); err == nil {
		t.Error("MkdirAll under a file must fail")
	}
}

// RenameNoReplace never replaces anything, not even an empty directory.
func TestRenameNoReplace(t *testing.T) {
	for _, existing := range []string{"", "file", "empty dir"} {
		t.Run("over "+existing, func(t *testing.T) {
			root := t.TempDir()
			src, dst := filepath.Join(root, "src"), filepath.Join(root, "dst")
			if err := os.Mkdir(src, 0o755); err != nil {
				t.Fatal(err)
			}
			switch existing {
			case "file":
				if err := os.WriteFile(dst, []byte("x"), 0o644); err != nil {
					t.Fatal(err)
				}
			case "empty dir":
				if err := os.Mkdir(dst, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			err := RenameNoReplace(src, dst)
			if existing == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if !errors.Is(err, fs.ErrExist) {
				t.Fatalf("RenameNoReplace = %v, want fs.ErrExist", err)
			}
			if _, err := os.Stat(src); err != nil {
				t.Errorf("source gone: %v", err)
			}
		})
	}
}
