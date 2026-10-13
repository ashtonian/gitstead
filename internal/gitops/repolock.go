package gitops

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// ErrRepoInUse reports a repository whose lock another git-mgr command
// holds: one deleting, archiving or moving it, or making a copy of it.
var ErrRepoInUse = errors.New("another git-mgr command is using the repo")

// ErrRepoGone reports a repository that isn't there to lock: never cloned,
// or deleted or moved away while its lock was waited for.
var ErrRepoGone = errors.New("the repo is gone")

const (
	repoLockFile = "git-mgr.lock"
	repoLockPoll = 50 * time.Millisecond
)

// LockRepo takes git-mgr's lock on the repository at repoPath, waiting up to
// wait: exclusive to delete, archive or move it, shared to make a copy of
// it. A copy holds the shared lock until it is recorded in state, so a
// command holding the exclusive lock sees every copy in state and no copy
// is begun from the repository while it goes. The lock is a file in the
// repository's git dir, so it moves with the repository and is deleted with
// it. Call release once done.
func LockRepo(ctx context.Context, repoPath string, exclusive bool, wait time.Duration) (release func(), err error) {
	if !HasGitDir(repoPath) {
		return nil, fmt.Errorf("%s: %w", repoPath, ErrRepoGone)
	}
	path := filepath.Join(ResolveGitDirs(ctx, repoPath).CommonDir, repoLockFile)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%s: %w", repoPath, ErrRepoGone)
	}
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}
	how := syscall.LOCK_SH
	if exclusive {
		how = syscall.LOCK_EX
	}
	deadline := time.Now().Add(wait)
	for {
		err := syscall.Flock(int(f.Fd()), how|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			f.Close()
			return nil, fmt.Errorf("locking %s: %w", path, err)
		}
		if !time.Now().Before(deadline) {
			f.Close()
			return nil, fmt.Errorf("%s: %w (waited %s)", repoPath, ErrRepoInUse, wait)
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-time.After(repoLockPoll):
		}
	}
	release = func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}
	// A lock taken on a file deleted or moved away meanwhile belongs to a
	// repository no longer at repoPath.
	held, herr := f.Stat()
	now, nerr := os.Stat(path)
	if herr != nil || nerr != nil || !os.SameFile(held, now) {
		release()
		return nil, fmt.Errorf("%s: %w (deleted or moved meanwhile)", repoPath, ErrRepoGone)
	}
	return release, nil
}
