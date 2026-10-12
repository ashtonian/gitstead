// Package pathutil provides filesystem path-safety helpers.
package pathutil

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// SafeRemoveAll removes path (via os.RemoveAll) after refusing obviously
// dangerous targets: the filesystem root, $HOME, or any path with fewer than
// three components. It is the single guard shared by the repo-removal and
// workspace-teardown commands.
func SafeRemoveAll(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolving path: %w", err)
	}
	if abs == "/" || abs == os.Getenv("HOME") || strings.Count(abs, string(os.PathSeparator)) < 3 {
		return fmt.Errorf("refusing to delete unsafe path: %s", abs)
	}
	return os.RemoveAll(abs)
}
