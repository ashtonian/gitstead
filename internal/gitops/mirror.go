package gitops

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/ashtonian/git-mgr/internal/repourl"
)

// MirrorOptions are how a mirror is made and kept.
type MirrorOptions struct {
	// SSHKey is the key to fetch with over SSH; "" for ssh's own choice.
	SSHKey string
	// LFS also fetches all the repository's Git LFS content.
	LFS bool
}

// ErrNotAMirror reports something at a mirror's path that isn't a mirror,
// which is left alone.
var ErrNotAMirror = errors.New("it isn't a mirror (remote.origin.mirror is unset)")

// Mirror makes or updates the mirror of url at path: a bare repository with
// every ref the remote has (git clone --mirror), made in a staging directory
// like any clone. An existing mirror gets url as its origin, then fetches
// every ref, without pruning: a branch deleted upstream stays in the
// mirror, while one moved -- force-pushed included -- follows the remote.
// It reports whether it made the mirror.
func Mirror(ctx context.Context, url, path string, opts MirrorOptions) (created bool, err error) {
	var env []string
	if opts.SSHKey != "" && !repourl.IsHTTP(url) {
		env = []string{gitSSHCommandEnv(opts.SSHKey)}
	}
	if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
		err := staged(path, func(staging string) error {
			return runClone(ctx, env, url, "clone", "--quiet", "--mirror", url, staging)
		})
		if err != nil {
			return false, err
		}
		created = true
	} else {
		if v, _ := Git(ctx, path, "config", "--bool", "remote.origin.mirror"); v != "true" {
			return false, fmt.Errorf("%s: %w", path, ErrNotAMirror)
		}
		if have, _ := Git(ctx, path, "remote", "get-url", "origin"); have != url {
			if _, err := Git(ctx, path, "remote", "set-url", "origin", url); err != nil {
				return false, err
			}
		}
		if _, err := gitWithEnv(ctx, path, env, "fetch", "--quiet", "origin"); err != nil {
			return false, fmt.Errorf("fetching %s: %w", repourl.Redact(url), err)
		}
	}
	if opts.LFS {
		if _, err := gitWithEnv(ctx, path, env, "lfs", "fetch", "--all", "origin"); err != nil {
			return created, fmt.Errorf("fetching LFS content: %w", err)
		}
	}
	return created, nil
}
