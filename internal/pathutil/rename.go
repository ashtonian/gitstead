package pathutil

import (
	"errors"
	"io/fs"
	"os"
	"syscall"
)

// RenameNoReplace renames oldpath to newpath, failing with an error that
// matches fs.ErrExist when anything is at newpath, even an empty directory.
// Where the filesystem has an exclusive rename (renameat2 RENAME_NOREPLACE,
// renamex_np RENAME_EXCL) it never replaces what another process put there
// meanwhile. Elsewhere it checks first, and an empty directory made between
// the check and the rename can be replaced -- nothing else can be.
func RenameNoReplace(oldpath, newpath string) error {
	err := renameNoReplace(oldpath, newpath)
	if errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.ENOTSUP) || errors.Is(err, syscall.ENOSYS) {
		// A filesystem without an exclusive rename. rename(2) still refuses to
		// put a directory over a file or a non-empty directory, so only an
		// empty directory appearing after the check could be replaced.
		if _, lerr := os.Lstat(newpath); lerr == nil {
			err = syscall.EEXIST
		} else if !errors.Is(lerr, fs.ErrNotExist) {
			err = lerr
		} else {
			err = syscall.Rename(oldpath, newpath)
		}
	}
	if err != nil {
		return &os.LinkError{Op: "rename", Old: oldpath, New: newpath, Err: err}
	}
	return nil
}
