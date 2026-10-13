package gitops

import (
	"context"
	"errors"
	"fmt"
)

// FastForward brings the checked-out branch of the clone at repoPath up to
// its upstream, as git merge --ff-only does, when nothing can be lost: the
// branch is behind and not ahead, no tracked file has changes, and git is
// not busy in it. Untracked and ignored files stay where they are: git
// refuses rather than overwrite one (--no-overwrite-ignore, as by default it
// would overwrite an ignored file the upstream starts tracking). A clone it
// leaves behind its upstream gets a reason in skipped; one that is up to
// date, detached or without an upstream has nothing to do.
func FastForward(ctx context.Context, repoPath string) (updated bool, skipped string, err error) {
	if _, err := Git(ctx, repoPath, "symbolic-ref", "-q", "HEAD"); err != nil {
		return false, "", nil
	}
	upstream, err := Git(ctx, repoPath, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "HEAD@{upstream}")
	if err != nil {
		return false, "", nil
	}
	ahead, behind, err := AheadBehind(ctx, repoPath, upstream)
	switch {
	case err != nil:
		return false, "", err
	case behind == 0:
		return false, "", nil
	case ahead > 0:
		return false, fmt.Sprintf("it has diverged from %s: %d ahead, %d behind", upstream, ahead, behind), nil
	}
	if err := CheckIdle(ctx, repoPath, true); err != nil {
		if errors.Is(err, ErrBusy) {
			return false, err.Error(), nil
		}
		return false, "", err
	}
	changes, err := GitOutput(ctx, repoPath, "status", "--porcelain=v1", "-z", "--untracked-files=no")
	if err != nil {
		return false, "", fmt.Errorf("reading status of %s: %w", repoPath, err)
	}
	if len(changes) > 0 {
		return false, "it has uncommitted changes", nil
	}
	if _, err := Git(ctx, repoPath, "merge", "--ff-only", "--no-overwrite-ignore", "--quiet", upstream); err != nil {
		// Such as an untracked or ignored file in the way: git kept everything.
		return false, fmt.Sprintf("git left it as it was: %v", err), nil
	}
	return true, "", nil
}
