package gitops

import (
	"context"
	"fmt"
)

// Fetch fetches all remotes with pruning. Skips if the repo is locked.
func Fetch(ctx context.Context, repoPath, sshKeyPath string) error {
	if locked, file := IsLocked(repoPath); locked {
		return fmt.Errorf("repo locked: %s", file)
	}
	_, err := GitWithSSHKey(ctx, repoPath, sshKeyPath, "fetch", "--all", "--prune", "--tags")
	return err
}
