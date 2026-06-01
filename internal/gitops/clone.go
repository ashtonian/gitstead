package gitops

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// Clone clones a repo from sshURL into destPath using the given SSH key,
// creating the parent directory if needed. An empty sshKeyPath uses the
// default SSH configuration.
func Clone(ctx context.Context, sshURL, destPath, sshKeyPath string) error {
	if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
		return fmt.Errorf("creating parent dir: %w", err)
	}
	_, err := GitClone(ctx, sshURL, destPath, sshKeyPath)
	return err
}
