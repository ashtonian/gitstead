package gitops

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// Clone clones a repo via SSH to the destination path.
func Clone(ctx context.Context, sshURL, destPath, sshKeyPath string) error {
	if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
		return fmt.Errorf("creating parent dir: %w", err)
	}
	_, err := GitClone(ctx, sshURL, destPath, sshKeyPath)
	return err
}
