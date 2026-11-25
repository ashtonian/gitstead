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
	if !onBranch(ctx, repoPath) {
		return false, "", nil
	}
	upstream, err := Git(ctx, repoPath, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "HEAD@{upstream}")
	if err != nil {
		return false, "", nil
	}
	return fastForward(ctx, repoPath, upstream, true)
}

// FastForwardTo brings the checked-out branch of the repo at repoPath up to
// ref, as git merge --ff-only does, when it is behind ref and not ahead and
// git is not busy in it. Uncommitted changes carry across, as git pull
// --ff-only carries them; git refuses, and leaves everything as it was, when
// a change, an untracked file or an ignored one is in the way. A branch it
// leaves behind ref, or a detached HEAD, gets a reason in skipped; a branch
// up to date with ref has nothing to do.
func FastForwardTo(ctx context.Context, repoPath, ref string) (updated bool, skipped string, err error) {
	if !onBranch(ctx, repoPath) {
		return false, "it is not on a branch", nil
	}
	return fastForward(ctx, repoPath, ref, false)
}

// fastForward fast-forwards the checked-out branch to ref; with
// requireClean, only when no tracked file has changes.
func fastForward(ctx context.Context, repoPath, ref string, requireClean bool) (updated bool, skipped string, err error) {
	ahead, behind, err := AheadBehind(ctx, repoPath, ref)
	switch {
	case err != nil:
		return false, "", err
	case behind == 0:
		return false, "", nil
	case ahead > 0:
		return false, fmt.Sprintf("it has diverged from %s: %d ahead, %d behind", ref, ahead, behind), nil
	}
	if reason, err := busy(ctx, repoPath); reason != "" || err != nil {
		return false, reason, err
	}
	if requireClean {
		changes, err := GitOutput(ctx, repoPath, "status", "--porcelain=v1", "-z", "--untracked-files=no")
		if err != nil {
			return false, "", fmt.Errorf("reading status of %s: %w", repoPath, err)
		}
		if len(changes) > 0 {
			return false, "it has uncommitted changes", nil
		}
	}
	if _, err := Git(ctx, repoPath, "merge", "--ff-only", "--no-overwrite-ignore", "--quiet", ref); err != nil {
		// Such as a change, an untracked file or an ignored one in the way:
		// git kept everything.
		return false, fmt.Sprintf("git left it as it was: %v", err), nil
	}
	return true, "", nil
}

// Rebase replays the commits the checked-out branch of the repo at repoPath
// has and ref lacks onto ref, as git rebase does, when the branch is behind
// ref and git is not busy in it. A branch git refuses to rebase, over
// uncommitted changes without rebase.autoStash say, is left as it was with
// the reason in skipped; so is one whose rebase stops, on a conflict say,
// which is aborted. A detached HEAD gets a reason too; a branch up to date
// with ref has nothing to do.
func Rebase(ctx context.Context, repoPath, ref string) (updated bool, skipped string, err error) {
	if !onBranch(ctx, repoPath) {
		return false, "it is not on a branch", nil
	}
	_, behind, err := AheadBehind(ctx, repoPath, ref)
	if err != nil || behind == 0 {
		return false, "", err
	}
	if reason, err := busy(ctx, repoPath); reason != "" || err != nil {
		return false, reason, err
	}
	_, rebaseErr := Git(ctx, repoPath, "rebase", "--quiet", ref)
	if rebaseErr == nil {
		return true, "", nil
	}
	// busy found no operation under way, so one now is the rebase just
	// started, and aborting it can't touch anyone else's.
	if operationInProgress(ResolveGitDirs(ctx, repoPath).GitDir) == "" {
		return false, fmt.Sprintf("git left it as it was: %v", rebaseErr), nil
	}
	if _, err := Git(ctx, repoPath, "rebase", "--abort"); err != nil {
		return false, "", fmt.Errorf("aborting the rebase onto %s that stopped (%v): %w", ref, rebaseErr, err)
	}
	return false, fmt.Sprintf("rebasing onto %s stopped, so it was aborted and left as it was: %v", ref, rebaseErr), nil
}

// busy returns what git is doing in the repo at repoPath, as a reason to
// leave it alone; "" when git is idle there.
func busy(ctx context.Context, repoPath string) (reason string, err error) {
	err = CheckIdle(ctx, repoPath, true)
	if errors.Is(err, ErrBusy) {
		return err.Error(), nil
	}
	return "", err
}

// onBranch reports whether the repo at repoPath has a branch checked out.
func onBranch(ctx context.Context, repoPath string) bool {
	_, err := Git(ctx, repoPath, "symbolic-ref", "-q", "HEAD")
	return err == nil
}
