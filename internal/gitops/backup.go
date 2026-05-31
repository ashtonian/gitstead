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

func WithStrategy(s string) BackupOption { return func(c *BackupConfig) { c.Strategy = s } }
func WithPatchDir(d string) BackupOption { return func(c *BackupConfig) { c.PatchDir = d } }
func WithMaxPerRepo(n int) BackupOption  { return func(c *BackupConfig) { c.MaxPerRepo = n } }
func WithIncludeUntracked(b bool) BackupOption {
	return func(c *BackupConfig) { c.IncludeUntracked = b }
}

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
	IsDirty   bool
	RefName   string
	HasPatch  bool
	Timestamp string
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
	if locked, lf := IsLocked(repoPath); locked {
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

	// Strategy: stash-ref
	if cfg.Strategy == "stash-ref" || cfg.Strategy == "both" {
		stashOID, err := Git(ctx, repoPath, "stash", "create")
		if err == nil && stashOID != "" {
			refName := "refs/git-mgr/backups/" + timestamp
			if _, err := Git(ctx, repoPath, "update-ref", refName, stashOID); err != nil {
				slog.Warn("failed to create backup ref", "repo", repoPath, "error", err)
			} else {
				result.RefName = refName
			}
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

	// Prune old backups
	if cfg.MaxPerRepo > 0 {
		pruneRefs(ctx, repoPath, cfg.MaxPerRepo)
		prunePatchDirs(repoPath, cfg)
	}

	return result, nil
}

func detectWorkingTreeState(ctx context.Context, repoPath string) workingTreeState {
	untrackedOut, err := Git(ctx, repoPath, "ls-files", "--others", "--exclude-standard")
	if err != nil {
		slog.Debug("failed to list untracked files", "repo", repoPath, "error", err)
	}

	return workingTreeState{
		HasStaged:    GitExitCode(ctx, repoPath, "diff", "--cached", "--quiet") != 0,
		HasUnstaged:  GitExitCode(ctx, repoPath, "diff", "--quiet") != 0,
		HasUntracked: untrackedOut != "",
		UntrackedOut: untrackedOut,
		InRebase: fileExists(filepath.Join(repoPath, ".git", "rebase-merge")) ||
			fileExists(filepath.Join(repoPath, ".git", "rebase-apply")),
		InMerge:      fileExists(filepath.Join(repoPath, ".git", "MERGE_HEAD")),
		InCherryPick: fileExists(filepath.Join(repoPath, ".git", "CHERRY_PICK_HEAD")),
	}
}

func createPatchBackup(ctx context.Context, repoPath string, cfg BackupConfig, timestamp string, wt *workingTreeState) error {
	repoName := filepath.Base(repoPath)
	parentName := filepath.Base(filepath.Dir(repoPath))
	patchDir := filepath.Join(cfg.PatchDir, parentName, repoName, timestamp)

	if err := os.MkdirAll(patchDir, 0700); err != nil {
		return fmt.Errorf("creating patch dir: %w", err)
	}

	if wt.HasStaged {
		patch, err := Git(ctx, repoPath, "diff", "--cached", "--binary", "--no-color")
		if err != nil {
			slog.Warn("failed to get staged diff", "repo", repoPath, "error", err)
		} else if patch != "" {
			if err := os.WriteFile(filepath.Join(patchDir, "staged.patch"), []byte(patch), 0600); err != nil {
				return fmt.Errorf("writing staged patch: %w", err)
			}
		}
	}

	if wt.HasUnstaged {
		patch, err := Git(ctx, repoPath, "diff", "--binary", "--no-color")
		if err != nil {
			slog.Warn("failed to get unstaged diff", "repo", repoPath, "error", err)
		} else if patch != "" {
			if err := os.WriteFile(filepath.Join(patchDir, "unstaged.patch"), []byte(patch), 0600); err != nil {
				return fmt.Errorf("writing unstaged patch: %w", err)
			}
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
			tarPath := filepath.Join(patchDir, "untracked.tar.gz")
			if err := createTar(ctx, repoPath, tarPath, filtered); err != nil {
				slog.Debug("tar failed, file list saved", "repo", repoPath, "error", err)
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
	out, err := Git(ctx, repoPath, "for-each-ref", "--format=%(refname)", "refs/git-mgr/backups/")
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
		if e.IsDir() {
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
	out, err := Git(ctx, repoPath, "for-each-ref", "--format=%(refname) %(objectname:short)", "refs/git-mgr/backups/")
	if err != nil || out == "" {
		return nil, err
	}
	return strings.Split(out, "\n"), nil
}
