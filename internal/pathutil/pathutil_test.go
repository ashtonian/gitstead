package pathutil_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ashtonian/git-mgr/internal/pathutil"
)

func TestSafeRemoveAll(t *testing.T) {
	for _, bad := range []string{"/", "/tmp", os.Getenv("HOME")} {
		if bad == "" {
			continue
		}
		if err := pathutil.SafeRemoveAll(bad); err == nil {
			t.Errorf("expected refusal for unsafe path %q", bad)
		}
	}

	dir := filepath.Join(t.TempDir(), "sub")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := pathutil.SafeRemoveAll(dir); err != nil {
		t.Fatalf("expected a deep temp dir to be removable: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Error("dir should be gone after SafeRemoveAll")
	}
}

// A read-only directory doesn't leave the tree half deleted.
func TestSafeRemoveAll_ReadOnlyDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "repo")
	locked := filepath.Join(dir, "vendor", "locked")
	if err := os.MkdirAll(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(locked, "f"), []byte("f"), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o555); err != nil {
		t.Fatal(err)
	}
	if err := pathutil.SafeRemoveAll(dir); err != nil {
		t.Fatalf("SafeRemoveAll: %v", err)
	}
	if _, err := os.Lstat(dir); !os.IsNotExist(err) {
		t.Errorf("%s should be gone: %v", dir, err)
	}
}
