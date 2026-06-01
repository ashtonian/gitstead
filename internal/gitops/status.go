package gitops

import (
	"context"
	"os"
	"path/filepath"
)

// RepoStatus describes the working tree state of a repo.
type RepoStatus struct {
	HeadCommit    string
	CurrentBranch string
	HasStaged     bool
	HasUnstaged   bool
	HasUntracked  bool
	InRebase      bool
	InMerge       bool
	InCherryPick  bool
}

// GetStatus returns the current status of a repo's working tree.
func GetStatus(ctx context.Context, repoPath string) (*RepoStatus, error) {
	rs := &RepoStatus{}

	head, err := Git(ctx, repoPath, "rev-parse", "HEAD")
	if err != nil {
		return nil, err
	}
	rs.HeadCommit = head

	branch, err := Git(ctx, repoPath, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return nil, err
	}
	rs.CurrentBranch = branch

	rs.HasStaged = GitExitCode(ctx, repoPath, "diff", "--cached", "--quiet") != 0
	rs.HasUnstaged = GitExitCode(ctx, repoPath, "diff", "--quiet") != 0

	untracked, _ := Git(ctx, repoPath, "ls-files", "--others", "--exclude-standard")
	rs.HasUntracked = untracked != ""

	gitDir := ResolveGitDirs(ctx, repoPath).GitDir
	rs.InRebase = fileExists(filepath.Join(gitDir, "rebase-merge")) ||
		fileExists(filepath.Join(gitDir, "rebase-apply"))
	rs.InMerge = fileExists(filepath.Join(gitDir, "MERGE_HEAD"))
	rs.InCherryPick = fileExists(filepath.Join(gitDir, "CHERRY_PICK_HEAD"))

	return rs, nil
}

// StatusString returns a summary status string for state tracking.
func (rs *RepoStatus) StatusString() string {
	if rs.InMerge || rs.InRebase || rs.InCherryPick {
		return "conflict"
	}
	if rs.HasStaged || rs.HasUnstaged || rs.HasUntracked {
		return "dirty"
	}
	return "clean"
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
