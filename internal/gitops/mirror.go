package gitops

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"time"

	"github.com/ashtonian/git-mgr/internal/repourl"
)

// MirrorOptions are how a mirror is made and kept.
type MirrorOptions struct {
	// SSHKey is the key to fetch with over SSH; "" for ssh's own choice.
	SSHKey string
	// LFS also fetches all the repository's Git LFS content.
	LFS bool
	// KeepRewritten is how long the old tip of a ref the remote rewrote --
	// moved to a commit that doesn't descend from it, as a force-push does --
	// is kept, under refs/git-mgr-rewritten/<time>/; 0 keeps none.
	KeepRewritten time.Duration
	// Now is when an update is made; nil for time.Now.
	Now func() time.Time
}

// RewrittenPrefix holds the old tips of refs a mirror's remote rewrote. It
// is outside refs/git-mgr/, which a remote may hold refs of its own under.
const RewrittenPrefix = "refs/git-mgr-rewritten/"

const rewrittenStamp = "20060102T150405Z"

// ErrNotAMirror reports something at a mirror's path that isn't a mirror,
// which is left alone.
var ErrNotAMirror = errors.New("it isn't a mirror (remote.origin.mirror is unset)")

// Mirror makes or updates the mirror of url at path: a bare repository with
// every ref the remote has (git clone --mirror), made in a staging directory
// like any clone. An existing mirror gets url as its origin, then fetches
// every ref, never pruning, whatever git's config says: a branch deleted
// upstream stays in the mirror. One moved follows the remote, and when the
// move rewrote history its old tip is kept (see KeepRewritten). It reports
// whether it made the mirror.
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
		before, err := refTips(ctx, path)
		if err != nil {
			return false, err
		}
		if _, err := gitWithEnv(ctx, path, env, "fetch", "--quiet", "--no-prune", "--no-prune-tags", "origin"); err != nil {
			return false, fmt.Errorf("fetching %s: %w", repourl.Redact(url), err)
		}
		now := time.Now()
		if opts.Now != nil {
			now = opts.Now()
		}
		if err := keepRewritten(ctx, path, before, now, opts.KeepRewritten); err != nil {
			return false, fmt.Errorf("keeping rewritten history: %w", err)
		}
	}
	if opts.LFS {
		if _, err := gitWithEnv(ctx, path, env, "lfs", "fetch", "--all", "origin"); err != nil {
			return created, fmt.Errorf("fetching LFS content: %w", err)
		}
	}
	return created, nil
}

// refTips maps each of the repository's refs to the object it points at.
func refTips(ctx context.Context, path string) (map[string]string, error) {
	out, err := Git(ctx, path, "for-each-ref", "--format=%(objectname) %(refname)")
	if err != nil {
		return nil, err
	}
	tips := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if oid, ref, ok := strings.Cut(line, " "); ok {
			tips[ref] = oid
		}
	}
	return tips, nil
}

// keepRewritten keeps, under RewrittenPrefix<now>/, the old tip of each ref
// that moved since before to something not descended from it, then drops
// the tips kept longer than keep ago. keep 0 keeps nothing.
func keepRewritten(ctx context.Context, path string, before map[string]string, now time.Time, keep time.Duration) error {
	if keep <= 0 {
		return nil
	}
	after, err := refTips(ctx, path)
	if err != nil {
		return err
	}
	var updates []string
	stamp := now.UTC().Format(rewrittenStamp)
	for ref, old := range before {
		cur, ok := after[ref]
		if !ok || cur == old || strings.HasPrefix(ref, RewrittenPrefix) {
			continue
		}
		// Exit 1: not an ancestor. Anything else -- a ref to a tree or a blob
		// -- is kept too: whether it was rewritten can't be told.
		if GitExitCode(ctx, path, "merge-base", "--is-ancestor", old, cur) == 0 {
			continue
		}
		updates = append(updates, fmt.Sprintf("create %s%s/%s %s", RewrittenPrefix, stamp, strings.TrimPrefix(ref, "refs/"), old))
	}
	cutoff := now.Add(-keep)
	for ref := range after {
		rest, ok := strings.CutPrefix(ref, RewrittenPrefix)
		if !ok {
			continue
		}
		kept, _, _ := strings.Cut(rest, "/")
		if when, err := time.Parse(rewrittenStamp, kept); err == nil && when.Before(cutoff) {
			updates = append(updates, "delete "+ref)
		}
	}
	if len(updates) == 0 {
		return nil
	}
	_, err = gitWithStdin(ctx, path, nil, []byte(strings.Join(updates, "\n")+"\n"), "update-ref", "--stdin")
	return err
}
