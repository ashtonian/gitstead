package gitops

import (
	"context"
	"fmt"
)

// Fetch fetches all remotes with pruning and tags using the given SSH key.
// It returns ErrLocked (without fetching) if the repo has an active git lock.
func Fetch(ctx context.Context, repoPath, sshKeyPath string) error {
	if locked, file := IsLocked(ctx, repoPath); locked {
		return fmt.Errorf("%w: %s", ErrLocked, file)
	}
	_, err := GitWithSSHKey(ctx, repoPath, sshKeyPath, "fetch", "--all", "--prune", "--tags")
	return err
}
