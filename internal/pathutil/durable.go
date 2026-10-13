package pathutil

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"syscall"
)

// WriteFileAtomic replaces the file at path with data, durably: written to a
// temporary file beside it, synced, renamed over it, and its directory
// synced, so a reader -- or the disk after a crash -- has the old file or the
// new one, never part of either.
func WriteFileAtomic(path string, data []byte, perm fs.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name) // a no-op once renamed
	_, err = tmp.Write(data)
	if err == nil {
		err = tmp.Chmod(perm)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	return SyncDir(dir)
}

// SyncDir makes durable the changes to directory dir's entries: files made,
// renamed or removed in it. A filesystem that can't sync a directory is left
// to its own schedule.
func SyncDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := f.Sync(); err != nil && !errors.Is(err, syscall.EINVAL) && !errors.Is(err, syscall.ENOTSUP) {
		return fmt.Errorf("syncing %s: %w", dir, err)
	}
	return nil
}

// SyncTree makes every file and directory under root durable, root
// included, and root's own entry in its parent.
func SyncTree(root string) error {
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			return SyncDir(path)
		case d.Type().IsRegular():
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			defer f.Close()
			if err := f.Sync(); err != nil {
				return fmt.Errorf("syncing %s: %w", path, err)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	return SyncDir(filepath.Dir(root))
}

// SyncFile makes the file at path, and its entry in its directory, durable.
func SyncFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	err = f.Sync()
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("syncing %s: %w", path, err)
	}
	return SyncDir(filepath.Dir(path))
}

// MkdirAll is os.MkdirAll, durably: each directory it makes is synced into
// its parent, so what is written in them afterwards -- and synced there --
// can't be lost with them in a crash.
func MkdirAll(path string, perm fs.FileMode) error {
	var made []string
	for dir := filepath.Clean(path); ; dir = filepath.Dir(dir) {
		if _, err := os.Lstat(dir); err == nil {
			break
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		made = append(made, dir)
		if filepath.Dir(dir) == dir {
			break
		}
	}
	if err := os.MkdirAll(path, perm); err != nil {
		return err
	}
	for _, dir := range slices.Backward(made) {
		if err := SyncDir(filepath.Dir(dir)); err != nil {
			return err
		}
	}
	return nil
}
