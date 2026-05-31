package gitops

import "context"

// Commit stages changes and commits them. With addUntracked it stages
// untracked files too (git add -A); otherwise only tracked modifications
// (git add -u). It returns committed=false when there was nothing to commit.
func Commit(ctx context.Context, repoPath, message string, addUntracked bool) (committed bool, err error) {
	addArg := "-u"
	if addUntracked {
		addArg = "-A"
	}
	if _, err := Git(ctx, repoPath, "add", addArg); err != nil {
		return false, err
	}
	if GitExitCode(ctx, repoPath, "diff", "--cached", "--quiet") == 0 {
		return false, nil // nothing staged
	}
	if _, err := Git(ctx, repoPath, "commit", "-m", message); err != nil {
		return false, err
	}
	return true, nil
}

// Pull updates the current branch. rebase uses --rebase; otherwise ffOnly
// restricts to a fast-forward (the safe default for group operations).
func Pull(ctx context.Context, repoPath, sshKeyPath string, ffOnly, rebase bool) (string, error) {
	args := []string{"pull"}
	switch {
	case rebase:
		args = append(args, "--rebase")
	case ffOnly:
		args = append(args, "--ff-only")
	}
	return GitWithSSHKey(ctx, repoPath, sshKeyPath, args...)
}

// Push pushes the current branch. setUpstream publishes it as origin/<branch>
// and records the upstream.
func Push(ctx context.Context, repoPath, sshKeyPath string, setUpstream bool) (string, error) {
	args := []string{"push"}
	if setUpstream {
		branch, err := Git(ctx, repoPath, "rev-parse", "--abbrev-ref", "HEAD")
		if err != nil {
			return "", err
		}
		args = append(args, "-u", "origin", branch)
	}
	return GitWithSSHKey(ctx, repoPath, sshKeyPath, args...)
}
