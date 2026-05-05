package gitops

import (
	"context"
	"os"
	"path/filepath"
)

// Git lock files that indicate an operation is in progress.
var gitLockFiles = []string{
	".git/index.lock",
	".git/HEAD.lock",
	".git/config.lock",
	".git/shallow.lock",
	".git/refs/stash.lock",
	".git/packed-refs.lock",
}

// IsLocked checks if any git lock files exist in the repo.
func IsLocked(repoPath string) (bool, string) {
	for _, lf := range gitLockFiles {
		p := filepath.Join(repoPath, lf)
		if _, err := os.Stat(p); err == nil {
			return true, lf
		}
	}
	return false, ""
}

// IsBareRepo checks if the repo at repoPath is a bare repository.
func IsBareRepo(ctx context.Context, repoPath string) bool {
	out, err := Git(ctx, repoPath, "rev-parse", "--is-bare-repository")
	return err == nil && out == "true"
}

// HasGitDir checks if the path contains a .git directory or file.
func HasGitDir(path string) bool {
	_, err := os.Stat(filepath.Join(path, ".git"))
	return err == nil
}
