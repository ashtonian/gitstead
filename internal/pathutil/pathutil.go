// Package pathutil provides filesystem path-safety helpers.
package pathutil

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// SafeRemoveAll removes path (via os.RemoveAll) after refusing obviously
// dangerous targets: the filesystem root, $HOME, or any path with fewer than
// three components. It is the single guard shared by the repo-removal and
// workspace-teardown commands. A read-only directory in the tree, which
// os.RemoveAll can't empty, is made writable first, so the tree never ends
// up half deleted for want of a permission bit.
func SafeRemoveAll(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolving path: %w", err)
	}
	if abs == "/" || abs == os.Getenv("HOME") || strings.Count(abs, string(os.PathSeparator)) < 3 {
		return fmt.Errorf("refusing to delete unsafe path: %s", abs)
	}
	err = os.RemoveAll(abs)
	if !errors.Is(err, fs.ErrPermission) {
		return err
	}
	// WalkDir doesn't follow symlinks, so nothing outside the tree changes.
	_ = filepath.WalkDir(abs, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			os.Chmod(p, 0o700)
		}
		return nil
	})
	return os.RemoveAll(abs)
}
