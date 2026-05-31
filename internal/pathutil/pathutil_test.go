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
