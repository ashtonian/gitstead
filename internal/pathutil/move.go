package pathutil

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"syscall"
)

// MovedError reports a move that put the whole tree at Dst, and then failed:
// syncing the directories the rename changed, so a crash could still undo
// it, or deleting what was left at Src after copying it across filesystems.
// The tree is to be found at Dst.
type MovedError struct {
	Src, Dst string
	Err      error
}

func (e *MovedError) Error() string {
	return fmt.Sprintf("moved %s to %s, but %v", e.Src, e.Dst, e.Err)
}

func (e *MovedError) Unwrap() error { return e.Err }

// Move moves the directory tree at src to dst, which must not exist. It
// renames when it can. Across filesystems it copies the tree into a staging
// directory beside dst, renames that into place and only then deletes src,
// so a failed move leaves src whole and nothing at dst -- except that a
// *MovedError means the tree is whole at dst and what failed came after.
func Move(src, dst string) error {
	return move(src, dst, osOps)
}

// fsOps are the filesystem calls a move makes that tests stand in for.
type fsOps struct {
	rename  func(oldpath, newpath string) error
	syncDir func(dir string) error
}

var osOps = fsOps{rename: RenameNoReplace, syncDir: SyncDir}

func move(src, dst string, ops fsOps) error {
	if _, err := os.Lstat(dst); err == nil {
		return fmt.Errorf("moving %s: %s already exists", src, dst)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	err := ops.rename(src, dst)
	if err == nil {
		// Both directories' entries changed; a crash mustn't undo the move
		// in one and not the other.
		if err := errors.Join(ops.syncDir(filepath.Dir(dst)), ops.syncDir(filepath.Dir(src))); err != nil {
			return &MovedError{Src: src, Dst: dst, Err: fmt.Errorf("a crash could still undo it: %w", err)}
		}
		return nil
	}
	if !errors.Is(err, syscall.EXDEV) {
		return err
	}

	staging, err := os.MkdirTemp(filepath.Dir(dst), "."+filepath.Base(dst)+".moving-")
	if err != nil {
		return err
	}
	if err := copyTree(src, staging); err != nil {
		_ = SafeRemoveAll(staging) // the copy's error is the one to report
		return fmt.Errorf("copying %s to %s: %w", src, dst, err)
	}
	// The copy is on disk, whole, before the source is deleted.
	if err := SyncTree(staging); err != nil {
		_ = SafeRemoveAll(staging)
		return err
	}
	if err := RenameNoReplace(staging, dst); err != nil {
		_ = SafeRemoveAll(staging)
		return err
	}
	// The copy must be durably in place before the source goes; until then
	// src is whole, so a copy that can't be made durable is undone.
	if err := ops.syncDir(filepath.Dir(dst)); err != nil {
		if rerr := SafeRemoveAll(dst); rerr != nil {
			return fmt.Errorf("%w; and the copy at %s couldn't be deleted: %v", err, dst, rerr)
		}
		return err
	}
	if err := SafeRemoveAll(src); err != nil {
		return &MovedError{Src: src, Dst: dst, Err: fmt.Errorf("couldn't delete everything left at %s: %w", src, err)}
	}
	return nil
}

// copyTree copies the tree at src into the existing directory dst, keeping
// modes, modification times and symlinks. Sockets and named pipes, such as
// git's fsmonitor socket, belong to running processes and are left out.
func copyTree(src, dst string) error {
	// Directories get their own modes only once filled, so a read-only
	// directory can still be copied into.
	type dirMode struct {
		path string
		info fs.FileInfo
	}
	var dirs []dirMode
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch mode := info.Mode(); {
		case mode.IsDir():
			if rel != "." {
				if err := os.Mkdir(target, 0o700); err != nil {
					return err
				}
			}
			dirs = append(dirs, dirMode{target, info})
		case mode&fs.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		case mode.IsRegular():
			return copyFile(path, target, info)
		case mode&(fs.ModeSocket|fs.ModeNamedPipe) != 0:
			return nil
		default:
			return fmt.Errorf("%s: can't copy a %s", path, mode.Type())
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, d := range slices.Backward(dirs) {
		if err := os.Chmod(d.path, d.info.Mode().Perm()); err != nil {
			return err
		}
		if err := os.Chtimes(d.path, d.info.ModTime(), d.info.ModTime()); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(src, dst string, info fs.FileInfo) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chtimes(dst, info.ModTime(), info.ModTime())
}
