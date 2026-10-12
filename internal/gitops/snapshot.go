package gitops

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// snapshotIdent authors snapshot commits. A fixed identity keeps snapshots
// working on machines with no user.email and marks them as git-mgr's.
var snapshotIdent = []string{
	"GIT_AUTHOR_NAME=git-mgr", "GIT_AUTHOR_EMAIL=git-mgr@localhost",
	"GIT_COMMITTER_NAME=git-mgr", "GIT_COMMITTER_EMAIL=git-mgr@localhost",
}

// SnapshotCommit records the working tree, untracked files included and
// ignored files excluded, as a commit whose parent is HEAD (no parent on an
// unborn branch), and returns its object ID.
//
// The tree is built in a temporary index, so the repository's own index and
// index.lock are never touched: it works while an agent's git command holds the
// lock, and it changes no file, index entry, stash or ref. The commit is never
// signed.
func SnapshotCommit(ctx context.Context, repoPath, message string) (string, error) {
	tmp, err := os.CreateTemp("", "git-mgr-index-")
	if err != nil {
		return "", fmt.Errorf("creating temporary index: %w", err)
	}
	tmpIndex := tmp.Name()
	tmp.Close()
	defer os.Remove(tmpIndex)

	// Starting from the real index saves rehashing unchanged files. Without one
	// (an unborn repo) git must create the temporary index itself, because an
	// empty file is not a valid index.
	realIndex := filepath.Join(ResolveGitDirs(ctx, repoPath).GitDir, "index")
	if err := copyFile(realIndex, tmpIndex); err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("copying index: %w", err)
		}
		os.Remove(tmpIndex)
	}

	env := append([]string{"GIT_INDEX_FILE=" + tmpIndex}, snapshotIdent...)
	if _, err := gitWithEnv(ctx, repoPath, env, "add", "-A"); err != nil {
		return "", err
	}
	tree, err := gitWithEnv(ctx, repoPath, env, "write-tree")
	if err != nil {
		return "", err
	}
	args := []string{"commit-tree", "--no-gpg-sign", tree, "-m", message}
	if head, err := Git(ctx, repoPath, "rev-parse", "--verify", "-q", "HEAD"); err == nil && head != "" {
		args = append(args, "-p", head)
	}
	return gitWithEnv(ctx, repoPath, env, args...)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
