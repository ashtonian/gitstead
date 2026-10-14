package pathutil_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ashtonian/gitstead/internal/pathutil"
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

// WriteFileAtomic replaces a file whole, with the mode asked for, leaving no
// temporary file behind.
func TestWriteFileAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.yaml")
	for _, data := range []string{"first\n", "second, longer than the first\n"} {
		if err := pathutil.WriteFileAtomic(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		if got, _ := os.ReadFile(path); string(got) != data {
			t.Errorf("file holds %q, want %q", got, data)
		}
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, %v", info.Mode(), err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("left in the directory: %v", entries)
	}
}
