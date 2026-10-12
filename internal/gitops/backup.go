package gitops

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// BackupConfig holds backup-related settings.
type BackupConfig struct {
	Strategy         string
	IncludeUntracked bool
	MaxPerRepo       int
	PatchDir         string

	// PushBranch, when set, also force-pushes a snapshot of tracked changes to
	// a recoverable branch (refs/heads/git-mgr/wip/<branch>) on PushRemote,
	// using SSHKey for auth. Unlike the stash-ref backup (which lives in the
	// hidden refs/git-mgr namespace), the WIP branch is a normal branch visible
	// in the remote's UI, so dirty work is saved offsite and easy to recover.
	PushBranch bool
	PushRemote string
	SSHKey     string
}

// BackupOption is a functional option for BackupConfig.
type BackupOption func(*BackupConfig)

// NewBackupConfig creates a BackupConfig with defaults and optional overrides.
func NewBackupConfig(opts ...BackupOption) BackupConfig {
	cfg := BackupConfig{
		Strategy:         "both",
		IncludeUntracked: true,
		MaxPerRepo:       10,
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	return cfg
}

// WithStrategy sets the backup strategy ("stash-ref", "patch", or "both").
func WithStrategy(s string) BackupOption { return func(c *BackupConfig) { c.Strategy = s } }

// WithPatchDir sets the directory where patch-strategy backups are written.
func WithPatchDir(d string) BackupOption { return func(c *BackupConfig) { c.PatchDir = d } }

// WithMaxPerRepo caps how many backups are retained per repo (0 = unlimited).
func WithMaxPerRepo(n int) BackupOption { return func(c *BackupConfig) { c.MaxPerRepo = n } }

// WithIncludeUntracked controls whether untracked files are included in patch backups.
func WithIncludeUntracked(b bool) BackupOption {
	return func(c *BackupConfig) { c.IncludeUntracked = b }
}

// WithPushBranch enables pushing a snapshot of tracked changes to a recoverable
// branch (refs/heads/git-mgr/wip/<branch>) on the given remote, using sshKey for
// auth. An empty remote defaults to "origin".
func WithPushBranch(remote, sshKey string) BackupOption {
	return func(c *BackupConfig) {
		c.PushBranch = true
		if remote == "" {
			remote = "origin"
		}
		c.PushRemote = remote
		c.SSHKey = sshKey
	}
}

// backupRefPrefix is the ref namespace for transparent stash-ref backups. It is
// deliberately outside refs/heads and refs/stash so it stays invisible to
// git branch, git stash list, git log, and IDEs.
const backupRefPrefix = "refs/git-mgr/backups/"

// BackupMetadata stores context about a backup for restoration.
type BackupMetadata struct {
	HeadCommit     string `json:"head_commit"`
	CurrentBranch  string `json:"current_branch"`
	Timestamp      string `json:"timestamp"`
	InRebase       bool   `json:"in_rebase"`
	InMerge        bool   `json:"in_merge"`
	InCherryPick   bool   `json:"in_cherry_pick"`
	HasStaged      bool   `json:"has_staged"`
	HasUnstaged    bool   `json:"has_unstaged"`
	UntrackedCount int    `json:"untracked_count"`
}

// BackupResult contains information about what was backed up.
type BackupResult struct {
	IsDirty      bool
	RefName      string
	HasPatch     bool
	PushedBranch string // WIP ref pushed to the remote, empty if none
	Timestamp    string
}

// workingTreeState captures the state of a repo's working tree for backup.
type workingTreeState struct {
	HasStaged    bool
	HasUnstaged  bool
	HasUntracked bool
	UntrackedOut string
	InRebase     bool
	InMerge      bool
	InCherryPick bool
}

// Backup performs a transparent backup of uncommitted changes.
func Backup(ctx context.Context, repoPath string, cfg BackupConfig) (*BackupResult, error) {
	result := &BackupResult{}

	// Pre-flight checks
	if !HasGitDir(repoPath) {
		return result, nil
	}
	if locked, lf := IsLocked(ctx, repoPath); locked {
		slog.Warn("repo locked, skipping backup", "repo", repoPath, "lock", lf)
		return result, nil
	}

	// Detect working tree state (all read-only operations)
	wt := detectWorkingTreeState(ctx, repoPath)
	if !wt.HasStaged && !wt.HasUnstaged && !wt.HasUntracked {
		return result, nil
	}

	result.IsDirty = true
	timestamp := time.Now().UTC().Format("20060102T150405Z")
	result.Timestamp = timestamp

	// git stash create captures tracked (staged + unstaged) changes
	// only, never untracked files. Under stash-ref-only that means
	// untracked work is silently not backed up -- warn so it is not a
	// surprise (strategy "both"/"patch" tars untracked files).
	if cfg.Strategy == "stash-ref" && wt.HasUntracked {
		slog.Warn("stash-ref backup does not capture untracked files; use strategy 'both' or 'patch' to back them up", "repo", repoPath)
	}

	// Capture a snapshot commit of tracked (staged + unstaged) changes once.
	// Both the stash-ref strategy and push-branch reuse this single snapshot;
	// git stash create produces a real commit without touching the working tree.
	var snapshotOID string
	if cfg.Strategy == "stash-ref" || cfg.Strategy == "both" || cfg.PushBranch {
		if oid, err := Git(ctx, repoPath, "stash", "create"); err != nil {
			slog.Warn("failed to create stash snapshot", "repo", repoPath, "error", err)
		} else {
			snapshotOID = oid
		}
	}

	// Strategy: stash-ref
	if (cfg.Strategy == "stash-ref" || cfg.Strategy == "both") && snapshotOID != "" {
		refName := backupRefPrefix + timestamp
		if _, err := Git(ctx, repoPath, "update-ref", refName, snapshotOID); err != nil {
			slog.Warn("failed to create backup ref", "repo", repoPath, "error", err)
		} else {
			result.RefName = refName
		}
	}

	// Strategy: patch
	if cfg.Strategy == "patch" || cfg.Strategy == "both" {
		if err := createPatchBackup(ctx, repoPath, cfg, timestamp, &wt); err != nil {
			slog.Warn("patch backup failed", "repo", repoPath, "error", err)
		} else {
			result.HasPatch = true
		}
	}

	// Push WIP to a recoverable remote branch, independent of strategy.
	if cfg.PushBranch {
		if snapshotOID == "" {
			slog.Warn("push-branch: no tracked changes to push; untracked-only changes are not captured", "repo", repoPath)
		} else if ref, err := pushWIPBranch(ctx, repoPath, cfg.PushRemote, cfg.SSHKey, snapshotOID); err != nil {
			slog.Warn("push-branch failed", "repo", repoPath, "remote", cfg.PushRemote, "error", err)
		} else {
			result.PushedBranch = ref
		}
	}

	// A dirty repo for which no strategy produced a backup is a silent
	// data-loss risk: the caller would otherwise record it as backed up. A
	// pushed WIP branch also counts as the work being saved.
	if result.IsDirty && result.RefName == "" && !result.HasPatch && result.PushedBranch == "" {
		return result, fmt.Errorf("backup failed: no backup strategy succeeded for dirty repo %s", repoPath)
	}

	// Prune only the kinds this run just added to, so a failed run can never
	// push out the last good backup.
	if cfg.MaxPerRepo > 0 {
		if result.RefName != "" {
			pruneRefs(ctx, repoPath, cfg.MaxPerRepo)
		}
		if result.HasPatch {
			prunePatchDirs(repoPath, cfg)
		}
	}

	return result, nil
}

// pushWIPBranch force-pushes a snapshot commit to a recoverable branch on a
// remote so dirty/WIP changes are saved offsite in addition to local backups.
// The branch is namespaced under refs/heads/git-mgr/wip/<branch> and
// force-updated each run so it always reflects the latest snapshot. It returns
// the ref that was pushed.
func pushWIPBranch(ctx context.Context, repoPath, remote, sshKey, snapshotOID string) (string, error) {
	branch, err := Git(ctx, repoPath, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil || branch == "" || branch == "HEAD" {
		branch = "detached" // detached HEAD: no branch name to mirror
	}
	wipRef := "refs/heads/git-mgr/wip/" + branch
	refspec := snapshotOID + ":" + wipRef
	if _, err := GitWithSSHKey(ctx, repoPath, sshKey, "push", "--force", remote, refspec); err != nil {
		return "", err
	}
	return wipRef, nil
}

func detectWorkingTreeState(ctx context.Context, repoPath string) workingTreeState {
	untrackedOut, err := Git(ctx, repoPath, "ls-files", "--others", "--exclude-standard")
	if err != nil {
		slog.Debug("failed to list untracked files", "repo", repoPath, "error", err)
	}

	gitDir := ResolveGitDirs(ctx, repoPath).GitDir
	return workingTreeState{
		HasStaged:    GitExitCode(ctx, repoPath, "diff", "--cached", "--quiet") != 0,
		HasUnstaged:  GitExitCode(ctx, repoPath, "diff", "--quiet") != 0,
		HasUntracked: untrackedOut != "",
		UntrackedOut: untrackedOut,
		InRebase: fileExists(filepath.Join(gitDir, "rebase-merge")) ||
			fileExists(filepath.Join(gitDir, "rebase-apply")),
		InMerge:      fileExists(filepath.Join(gitDir, "MERGE_HEAD")),
		InCherryPick: fileExists(filepath.Join(gitDir, "CHERRY_PICK_HEAD")),
	}
}

// partialSuffix marks a patch backup still being written. Only complete
// backups carry a bare timestamp name, so retention never counts a partial one.
const partialSuffix = ".partial"

// createPatchBackup writes the staged and unstaged diffs, the untracked files
// and metadata into a new timestamped directory. It builds the directory under
// a partial name and publishes it with one rename, so a failure leaves nothing
// that looks like a backup.
func createPatchBackup(ctx context.Context, repoPath string, cfg BackupConfig, timestamp string, wt *workingTreeState) error {
	repoName := filepath.Base(repoPath)
	parentName := filepath.Base(filepath.Dir(repoPath))
	patchDir := filepath.Join(cfg.PatchDir, parentName, repoName, timestamp)
	partial := patchDir + partialSuffix

	if err := os.MkdirAll(partial, 0700); err != nil {
		return fmt.Errorf("creating patch dir: %w", err)
	}
	if err := writePatchFiles(ctx, repoPath, partial, cfg, timestamp, wt); err != nil {
		os.RemoveAll(partial)
		return err
	}
	if err := os.Rename(partial, patchDir); err != nil {
		os.RemoveAll(partial)
		return fmt.Errorf("publishing patch backup: %w", err)
	}
	return nil
}

func writePatchFiles(ctx context.Context, repoPath, patchDir string, cfg BackupConfig, timestamp string, wt *workingTreeState) error {
	if wt.HasStaged {
		if err := writeDiff(ctx, repoPath, filepath.Join(patchDir, "staged.patch"), "--cached"); err != nil {
			return fmt.Errorf("staged diff: %w", err)
		}
	}
	if wt.HasUnstaged {
		if err := writeDiff(ctx, repoPath, filepath.Join(patchDir, "unstaged.patch")); err != nil {
			return fmt.Errorf("unstaged diff: %w", err)
		}
	}

	untrackedCount := 0
	if wt.HasUntracked && cfg.IncludeUntracked {
		filtered := filterUntrackedFiles(repoPath, wt.UntrackedOut)
		untrackedCount = len(filtered)
		if len(filtered) > 0 {
			if err := os.WriteFile(filepath.Join(patchDir, "untracked.list"), []byte(strings.Join(filtered, "\n")), 0600); err != nil {
				return fmt.Errorf("writing untracked list: %w", err)
			}
			if err := createTar(ctx, repoPath, filepath.Join(patchDir, "untracked.tar.gz"), filtered); err != nil {
				return fmt.Errorf("archiving untracked files: %w", err)
			}
		}
	}

	// Metadata
	head, err := Git(ctx, repoPath, "rev-parse", "HEAD")
	if err != nil {
		slog.Debug("failed to get HEAD", "repo", repoPath, "error", err)
	}
	branch, err := Git(ctx, repoPath, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		slog.Debug("failed to get branch", "repo", repoPath, "error", err)
	}

	meta := BackupMetadata{
		HeadCommit:     head,
		CurrentBranch:  branch,
		Timestamp:      timestamp,
		InRebase:       wt.InRebase,
		InMerge:        wt.InMerge,
		InCherryPick:   wt.InCherryPick,
		HasStaged:      wt.HasStaged,
		HasUnstaged:    wt.HasUnstaged,
		UntrackedCount: untrackedCount,
	}
	metaJSON, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling metadata: %w", err)
	}
	if err := os.WriteFile(filepath.Join(patchDir, "metadata.json"), metaJSON, 0600); err != nil {
		return fmt.Errorf("writing metadata: %w", err)
	}

	return nil
}

// writeDiff writes `git diff --binary` output, with any extra args, to path
// byte for byte.
func writeDiff(ctx context.Context, repoPath, path string, args ...string) error {
	patch, err := GitOutput(ctx, repoPath, append([]string{"diff", "--binary", "--no-color"}, args...)...)
	if err != nil {
		return err
	}
	if len(patch) == 0 {
		return nil
	}
	return os.WriteFile(path, patch, 0600)
}

// filterUntrackedFiles splits ls-files output and filters out large files (>50MB).
func filterUntrackedFiles(repoPath, untrackedOut string) []string {
	var filtered []string
	for _, f := range strings.Split(untrackedOut, "\n") {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		info, err := os.Stat(filepath.Join(repoPath, f))
		if err != nil || info.Size() > 50*1024*1024 {
			continue
		}
		filtered = append(filtered, f)
	}
	return filtered
}

// createTar creates a gzipped tar of the given files relative to repoPath.
func createTar(ctx context.Context, repoPath, tarPath string, files []string) error {
	args := append([]string{"-czf", tarPath, "-C", repoPath}, files...)
	cmd := exec.CommandContext(ctx, "tar", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("tar: %w: %s", err, string(out))
	}
	return nil
}

// pruneRefs removes old backup refs, keeping only the most recent N.
func pruneRefs(ctx context.Context, repoPath string, keep int) {
	out, err := Git(ctx, repoPath, "for-each-ref", "--format=%(refname)", backupRefPrefix)
	if err != nil || out == "" {
		return
	}
	refs := strings.Split(out, "\n")
	if len(refs) <= keep {
		return
	}
	sort.Strings(refs)
	for _, ref := range refs[:len(refs)-keep] {
		if _, err := Git(ctx, repoPath, "update-ref", "-d", ref); err != nil {
			slog.Debug("failed to prune ref", "ref", ref, "error", err)
		}
	}
}

// prunePatchDirs removes old patch directories, keeping only the most recent N.
func prunePatchDirs(repoPath string, cfg BackupConfig) {
	repoName := filepath.Base(repoPath)
	parentName := filepath.Base(filepath.Dir(repoPath))
	patchBase := filepath.Join(cfg.PatchDir, parentName, repoName)

	entries, err := os.ReadDir(patchBase)
	if err != nil {
		return
	}

	var dirs []string
	for _, e := range entries {
		if e.IsDir() && !strings.HasSuffix(e.Name(), partialSuffix) {
			dirs = append(dirs, e.Name())
		}
	}
	if len(dirs) <= cfg.MaxPerRepo {
		return
	}
	sort.Strings(dirs)
	for _, d := range dirs[:len(dirs)-cfg.MaxPerRepo] {
		os.RemoveAll(filepath.Join(patchBase, d))
	}
}

// ListBackupRefs lists all backup refs for a repo.
func ListBackupRefs(ctx context.Context, repoPath string) ([]string, error) {
	out, err := Git(ctx, repoPath, "for-each-ref", "--format=%(refname) %(objectname:short)", backupRefPrefix)
	if err != nil || out == "" {
		return nil, err
	}
	return strings.Split(out, "\n"), nil
}
