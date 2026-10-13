package gitops

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// snapshotIdent authors snapshot commits. A fixed identity keeps snapshots
// working on machines with no user.email and marks them as git-mgr's.
var snapshotIdent = []string{
	"GIT_AUTHOR_NAME=git-mgr", "GIT_AUTHOR_EMAIL=git-mgr@localhost",
	"GIT_COMMITTER_NAME=git-mgr", "GIT_COMMITTER_EMAIL=git-mgr@localhost",
}

// Snapshot is a working copy's state stored as commits in the layout `git
// stash` uses, so `git stash apply --index <Commit>` restores it exactly. Commit
// holds the tracked files of the working tree; its parents are the base commit
// (HEAD when taken), the index commit and, when untracked files were captured,
// a commit of those. On an unborn branch there is no base and the index commit
// is the first parent.
//
// Snapshot commits carry a fixed identity and the base commit's date, so the
// same state always produces the same Commit ID.
type Snapshot struct {
	Commit    string
	Base      string // "" on an unborn branch
	Index     string
	Untracked string // "" when no untracked files were captured

	// IndexUnmerged is set when the index held unresolved conflicts. The index
	// commit then records the base tree; the conflicted files' working copies
	// are still in Commit.
	IndexUnmerged bool
	// Skipped lists untracked paths left out of the snapshot.
	Skipped []SkippedFile
}

// SkippedFile is an untracked path a snapshot left out, and why.
type SkippedFile struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// SnapshotLimits bounds the untracked files a snapshot hashes into the
// repository's object store.
type SnapshotLimits struct {
	Untracked     bool  // capture untracked files at all
	MaxFileBytes  int64 // skip larger untracked files; 0 means no limit
	MaxTotalBytes int64 // stop adding untracked files past this total; 0 means no limit
}

// TakeSnapshot captures the index, the working tree and, within limits, the
// untracked files of the working copy at repoPath. Ignored files are never
// captured.
//
// Every tree is built in a temporary index, so the repository's own index and
// index.lock are never touched: it works while an agent's git command holds the
// lock, and it changes no file, index entry, stash or ref. Commits are never
// signed.
func TakeSnapshot(ctx context.Context, repoPath string, limits SnapshotLimits) (Snapshot, error) {
	var snap Snapshot
	tmpDir, err := os.MkdirTemp("", "git-mgr-snapshot-")
	if err != nil {
		return snap, fmt.Errorf("creating temporary index dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	if out, err := Git(ctx, repoPath, "rev-parse", "--verify", "-q", "HEAD^{commit}"); err == nil {
		snap.Base = out
	} else if GitExitCode(ctx, repoPath, "rev-parse", "--git-dir") != 0 {
		return snap, fmt.Errorf("%s is not a git working copy", repoPath)
	}
	baseTree, err := treeOf(ctx, repoPath, snap.Base)
	if err != nil {
		return snap, err
	}

	// Each tree gets its own copy of the real index: write-tree for the index
	// as it stands, add -u for the tracked files in the working tree.
	realIndex := filepath.Join(ResolveGitDirs(ctx, repoPath).GitDir, "index")
	indexEnv, err := tempIndex(realIndex, filepath.Join(tmpDir, "index"))
	if err != nil {
		return snap, err
	}
	indexTree, err := gitWithEnv(ctx, repoPath, indexEnv, "write-tree")
	if err != nil {
		unmerged, uerr := gitWithEnv(ctx, repoPath, indexEnv, "ls-files", "--unmerged")
		if uerr != nil || unmerged == "" {
			return snap, fmt.Errorf("recording the index: %w", err)
		}
		snap.IndexUnmerged = true
		indexTree = baseTree
	}

	workEnv, err := tempIndex(realIndex, filepath.Join(tmpDir, "work"))
	if err != nil {
		return snap, err
	}
	if _, err := gitWithEnv(ctx, repoPath, workEnv, "add", "-u"); err != nil {
		return snap, fmt.Errorf("recording the working tree: %w", err)
	}
	workTree, err := gitWithEnv(ctx, repoPath, workEnv, "write-tree")
	if err != nil {
		return snap, fmt.Errorf("recording the working tree: %w", err)
	}

	var untrackedTree string
	if limits.Untracked {
		untrackedTree, snap.Skipped, err = untrackedSnapshotTree(ctx, repoPath, workEnv, filepath.Join(tmpDir, "untracked"), limits)
		if err != nil {
			return snap, err
		}
	}

	env, err := snapshotEnv(ctx, repoPath, snap.Base)
	if err != nil {
		return snap, err
	}
	branch := currentBranchLabel(ctx, repoPath)
	commit := func(tree, message string, parents ...string) (string, error) {
		args := []string{"commit-tree", "--no-gpg-sign", tree, "-m", message}
		for _, p := range parents {
			if p != "" {
				args = append(args, "-p", p)
			}
		}
		return gitWithEnv(ctx, repoPath, env, args...)
	}

	if snap.Index, err = commit(indexTree, "index on "+branch, snap.Base); err != nil {
		return snap, err
	}
	if untrackedTree != "" {
		if snap.Untracked, err = commit(untrackedTree, "untracked files on "+branch); err != nil {
			return snap, err
		}
	}
	if snap.Commit, err = commit(workTree, "git-mgr snapshot on "+branch, snap.Base, snap.Index, snap.Untracked); err != nil {
		return snap, err
	}
	return snap, nil
}

// untrackedSnapshotTree hashes the untracked files that fit within limits into
// a tree, built in a fresh temporary index at indexPath. listEnv supplies the
// index used to tell untracked files from tracked ones.
func untrackedSnapshotTree(ctx context.Context, repoPath string, listEnv []string, indexPath string, limits SnapshotLimits) (string, []SkippedFile, error) {
	out, err := runGit(gitCommand(ctx, repoPath, listEnv, "ls-files", "-z", "--others", "--exclude-standard"), []string{"ls-files"})
	if err != nil {
		return "", nil, fmt.Errorf("listing untracked files: %w", err)
	}
	var kept bytes.Buffer
	var skipped []SkippedFile
	var total int64
	for _, path := range strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00") {
		if path == "" {
			continue
		}
		// ls-files lists an untracked nested repository as its directory; git
		// can't store its contents in a tree.
		if strings.HasSuffix(path, "/") {
			skipped = append(skipped, SkippedFile{Path: path, Reason: "nested git repository"})
			continue
		}
		info, err := os.Lstat(filepath.Join(repoPath, path))
		if err != nil {
			return "", nil, fmt.Errorf("reading untracked file %q: %w", path, err)
		}
		size := info.Size()
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
		case !info.Mode().IsRegular():
			skipped = append(skipped, SkippedFile{Path: path, Reason: "not a regular file or symlink"})
			continue
		case limits.MaxFileBytes > 0 && size > limits.MaxFileBytes:
			skipped = append(skipped, SkippedFile{Path: path, Reason: fmt.Sprintf("%s is over the %s file limit", formatBytes(size), formatBytes(limits.MaxFileBytes))})
			continue
		case limits.MaxTotalBytes > 0 && total+size > limits.MaxTotalBytes:
			skipped = append(skipped, SkippedFile{Path: path, Reason: fmt.Sprintf("untracked files are over the %s total limit", formatBytes(limits.MaxTotalBytes))})
			continue
		default:
			// One unreadable file must not fail the backup of everything else.
			if err := checkReadable(filepath.Join(repoPath, path)); err != nil {
				skipped = append(skipped, SkippedFile{Path: path, Reason: "unreadable"})
				continue
			}
		}
		total += size
		kept.WriteString(path)
		kept.WriteByte(0)
	}
	if kept.Len() == 0 {
		return "", skipped, nil
	}

	env := []string{"GIT_INDEX_FILE=" + indexPath}
	if _, err := gitWithStdin(ctx, repoPath, env, kept.Bytes(), "update-index", "-z", "--add", "--stdin"); err != nil {
		return "", nil, fmt.Errorf("recording untracked files: %w", err)
	}
	tree, err := gitWithEnv(ctx, repoPath, env, "write-tree")
	if err != nil {
		return "", nil, fmt.Errorf("recording untracked files: %w", err)
	}
	return tree, skipped, nil
}

// readSnapshot recovers a Snapshot's commits from its snapshot commit's shape.
func readSnapshot(ctx context.Context, repoPath, commit string) (Snapshot, error) {
	snap := Snapshot{Commit: commit}
	parents, err := commitParents(ctx, repoPath, commit)
	if err != nil {
		return snap, err
	}
	if len(parents) >= 2 {
		indexParents, err := commitParents(ctx, repoPath, parents[1])
		if err != nil {
			return snap, err
		}
		if len(indexParents) == 1 && indexParents[0] == parents[0] && len(parents) <= 3 {
			snap.Base, snap.Index = parents[0], parents[1]
			if len(parents) == 3 {
				snap.Untracked = parents[2]
			}
			return snap, nil
		}
	}
	// Unborn layout: index commit first, both parentless.
	if len(parents) == 0 || len(parents) > 2 {
		return snap, fmt.Errorf("%s is not a git-mgr snapshot", commit)
	}
	snap.Index = parents[0]
	if len(parents) == 2 {
		snap.Untracked = parents[1]
	}
	return snap, nil
}

// wipCommit returns a commit of the snapshot's tracked working tree whose only
// parent is the base: the form pushed offsite, which leaves out the index
// commit and untracked files.
func wipCommit(ctx context.Context, repoPath string, snap Snapshot) (string, error) {
	env, err := snapshotEnv(ctx, repoPath, snap.Base)
	if err != nil {
		return "", err
	}
	args := []string{"commit-tree", "--no-gpg-sign", snap.Commit + "^{tree}", "-m", "git-mgr WIP on " + currentBranchLabel(ctx, repoPath)}
	if snap.Base != "" {
		args = append(args, "-p", snap.Base)
	}
	return gitWithEnv(ctx, repoPath, env, args...)
}

// snapshotEnv fixes the identity and dates of snapshot commits. Dating them
// by the base commit makes a snapshot of an unchanged state reproduce the same
// commit ID, which is how unchanged backups are recognised.
func snapshotEnv(ctx context.Context, repoPath, base string) ([]string, error) {
	date := "@0 +0000"
	if base != "" {
		ts, err := Git(ctx, repoPath, "show", "-s", "--format=%ct", base)
		if err != nil {
			return nil, err
		}
		if _, err := strconv.ParseInt(ts, 10, 64); err != nil {
			return nil, fmt.Errorf("reading the date of %s: %q", base, ts)
		}
		date = "@" + ts + " +0000"
	}
	return append([]string{"GIT_AUTHOR_DATE=" + date, "GIT_COMMITTER_DATE=" + date}, snapshotIdent...), nil
}

// treeOf returns commit's tree, or the empty tree for "".
func treeOf(ctx context.Context, repoPath, commit string) (string, error) {
	if commit == "" {
		return gitWithStdin(ctx, repoPath, nil, nil, "mktree")
	}
	return Git(ctx, repoPath, "rev-parse", commit+"^{tree}")
}

func commitParents(ctx context.Context, repoPath, commit string) ([]string, error) {
	out, err := Git(ctx, repoPath, "rev-list", "--parents", "-n", "1", commit)
	if err != nil {
		return nil, err
	}
	fields := strings.Fields(out)
	if len(fields) == 0 {
		return nil, fmt.Errorf("reading parents of %s: no output", commit)
	}
	return fields[1:], nil
}

func currentBranchLabel(ctx context.Context, repoPath string) string {
	if b, err := Git(ctx, repoPath, "symbolic-ref", "-q", "--short", "HEAD"); err == nil && b != "" {
		return b
	}
	return "detached HEAD"
}

// tempIndex copies the real index to path and returns the environment that
// points git at the copy. Without a real index (an unborn repo) git creates
// the temporary one itself, because an empty file is not a valid index.
//
// The copy keeps the index's modification time: git trusts an entry's cached
// stat data only for a file older than the index, so a copy stamped now would
// hide an edit made in the second the index was last written.
func tempIndex(realIndex, path string) ([]string, error) {
	err := copyFile(realIndex, path)
	if errors.Is(err, fs.ErrNotExist) {
		return []string{"GIT_INDEX_FILE=" + path}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("copying index: %w", err)
	}
	info, err := os.Stat(realIndex)
	if err != nil {
		return nil, fmt.Errorf("copying index: %w", err)
	}
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		return nil, fmt.Errorf("copying index: %w", err)
	}
	return []string{"GIT_INDEX_FILE=" + path}, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func checkReadable(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	return f.Close()
}

func formatBytes(n int64) string {
	const mib = 1 << 20
	if n >= mib {
		return fmt.Sprintf("%.1f MiB", float64(n)/mib)
	}
	return fmt.Sprintf("%d bytes", n)
}
