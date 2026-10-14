package gitops

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ashtonian/git-mgr/internal/pathutil"
)

// BackupConfig holds backup-related settings.
type BackupConfig struct {
	Strategy  string // "stash-ref", "patch" or "both"
	Limits    SnapshotLimits
	Retention Retention
	PatchDir  string

	Now func() time.Time
}

// BackupOption is a functional option for BackupConfig.
type BackupOption func(*BackupConfig)

// NewBackupConfig creates a BackupConfig with defaults and optional overrides.
func NewBackupConfig(opts ...BackupOption) BackupConfig {
	cfg := BackupConfig{
		Strategy:  "both",
		Limits:    SnapshotLimits{Untracked: true, MaxFileBytes: 50 << 20, MaxTotalBytes: 500 << 20},
		Retention: Retention{KeepAll: 24 * time.Hour, KeepDaily: 14 * 24 * time.Hour, Max: 100},
		Now:       time.Now,
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

// WithRetention sets which backups of a working copy are kept.
func WithRetention(r Retention) BackupOption { return func(c *BackupConfig) { c.Retention = r } }

// WithIncludeUntracked controls whether untracked files are backed up.
func WithIncludeUntracked(b bool) BackupOption {
	return func(c *BackupConfig) { c.Limits.Untracked = b }
}

// WithUntrackedLimits caps the size of one untracked file and of all of them
// together; 0 means no limit.
func WithUntrackedLimits(maxFile, maxTotal int64) BackupOption {
	return func(c *BackupConfig) {
		c.Limits.MaxFileBytes = maxFile
		c.Limits.MaxTotalBytes = maxTotal
	}
}

// WithClock sets the clock that names and ages backups.
func WithClock(now func() time.Time) BackupOption { return func(c *BackupConfig) { c.Now = now } }

// backupRefPrefix holds snapshot refs as <prefix><copy ID>/<backup ID>. It is
// outside refs/heads and refs/stash, so it stays invisible to git branch, git
// stash list, git log and IDEs. Linked worktrees share refs, which is why each
// copy gets its own namespace.
const backupRefPrefix = "refs/git-mgr/backups/"

// submoduleRefPrefix holds, as <prefix><copy ID>/<backup ID>, a blob listing
// the submodules' backups that go with a backup (SubmoduleBackups as JSON),
// for a backup kept only as a snapshot ref.
const submoduleRefPrefix = "refs/git-mgr/backup-submodules/"

// SubmoduleBackup names the backup of one submodule that goes with its
// parent's: the submodule's own copy ID and backup ID.
type SubmoduleBackup struct {
	CopyID string `json:"copy_id"`
	ID     string `json:"id"`
}

// BackupOutcome is what a backup run did.
type BackupOutcome int

const (
	BackupClean     BackupOutcome = iota // nothing uncommitted, nothing saved
	BackupUnchanged                      // identical to the latest backup, which still stands
	BackupSaved                          // a new backup was written
)

func (o BackupOutcome) String() string {
	switch o {
	case BackupUnchanged:
		return "unchanged"
	case BackupSaved:
		return "saved"
	default:
		return "clean"
	}
}

// BackupResult reports a backup run.
type BackupResult struct {
	Outcome       BackupOutcome
	ID            string // backup holding the current state; "" when clean
	CopyID        string
	Snapshot      string // commit of the backed-up state; "" when clean
	Ref           string // snapshot ref; "" if the strategy keeps none or it failed
	PatchDir      string // patch backup directory; "" if the strategy keeps none or it failed
	Skipped       []SkippedFile
	IndexUnmerged bool
	// Submodules holds, by path, the backups of the checked-out submodules
	// that had uncommitted work.
	Submodules map[string]*BackupResult
}

// Dirty reports whether the working copy had anything to back up.
func (r *BackupResult) Dirty() bool { return r.Outcome != BackupClean }

// Backup saves the uncommitted state of the working copy at repoPath: staged
// and unstaged changes and untracked files within cfg.Limits. It never touches
// the working tree, the index, index.lock, branches or the stash.
//
// Each checked-out submodule with uncommitted work is backed up the same way,
// as a working copy of its own -- its own copy ID, refs and patch folder --
// and the parent's backup records which of the submodules' backups go with
// it, so restoring it restores theirs.
//
// An unchanged state is not saved again. Old backups are pruned by
// cfg.Retention, and only after a new one is complete. Every requested part
// that fails is reported in the error; the result still describes what was
// saved.
func Backup(ctx context.Context, repoPath string, cfg BackupConfig) (*BackupResult, error) {
	paths, err := Submodules(ctx, repoPath)
	if err != nil {
		return &BackupResult{}, err
	}
	var errs []error
	subs := map[string]SubmoduleBackup{}
	results := map[string]*BackupResult{}
	for _, path := range paths {
		// A submodule's backups are kept as long as a backup of the parent
		// names one, not by their own retention; see pruneSubmoduleBackups.
		r, err := backupRepo(ctx, filepath.Join(repoPath, path), cfg, nil, false)
		if err != nil {
			errs = append(errs, fmt.Errorf("submodule %s: %w", path, err))
		}
		if r.Dirty() {
			results[path] = r
		}
		if r.ID != "" {
			subs[path] = SubmoduleBackup{CopyID: r.CopyID, ID: r.ID}
		}
	}
	res, err := backupRepo(ctx, repoPath, cfg, subs, true)
	if err == nil && len(paths) > 0 {
		pruneSubmoduleBackups(ctx, repoPath, cfg.PatchDir, paths)
	}
	if len(results) > 0 {
		res.Submodules = results
	}
	for _, path := range slices.Sorted(maps.Keys(results)) {
		for _, s := range results[path].Skipped {
			res.Skipped = append(res.Skipped, SkippedFile{Path: path + "/" + s.Path, Reason: s.Reason})
		}
	}
	return res, errors.Join(append([]error{err}, errs...)...)
}

// backupRepo backs up one working copy, not its submodules; subs are the
// submodules' backups that go with it, which make it dirty by themselves.
// With prune, old backups go by cfg.Retention once a new one is saved.
func backupRepo(ctx context.Context, repoPath string, cfg BackupConfig, subs map[string]SubmoduleBackup, prune bool) (*BackupResult, error) {
	res := &BackupResult{}
	untracked := "--untracked-files=no"
	if cfg.Limits.Untracked {
		untracked = "--untracked-files=all"
	}
	status, err := GitOutput(ctx, repoPath, "status", "--porcelain=v1", "-z", untracked)
	if err != nil {
		return res, fmt.Errorf("reading status of %s: %w", repoPath, err)
	}
	if len(status) == 0 && len(subs) == 0 {
		return res, nil
	}

	snap, err := TakeSnapshot(ctx, repoPath, cfg.Limits)
	if err != nil {
		return res, fmt.Errorf("snapshotting %s: %w", repoPath, err)
	}
	res.Snapshot, res.Skipped, res.IndexUnmerged = snap.Commit, snap.Skipped, snap.IndexUnmerged
	if res.CopyID, err = CopyID(ctx, repoPath); err != nil {
		return res, err
	}

	wantRef := cfg.Strategy == "stash-ref" || cfg.Strategy == "both"
	wantPatch := cfg.Strategy == "patch" || cfg.Strategy == "both"
	backups, err := listCopyBackups(ctx, repoPath, cfg.PatchDir, res.CopyID)
	if err != nil {
		return res, err
	}

	var errs []error
	if latest := lastBackup(backups); latest != nil && latest.Snapshot() == snap.Commit && maps.Equal(latest.Submodules, subs) &&
		(!wantRef || latest.Ref != "") && (!wantPatch || latest.PatchDir != "") {
		res.Outcome, res.ID, res.Ref, res.PatchDir = BackupUnchanged, latest.ID, latest.Ref, latest.PatchDir
	} else {
		id := newBackupID(cfg.Now(), latest)
		if wantRef {
			ref := backupRefPrefix + res.CopyID + "/" + id
			if err := saveSnapshotRef(ctx, repoPath, ref, submoduleRefPrefix+res.CopyID+"/"+id, snap.Commit, subs); err != nil {
				errs = append(errs, fmt.Errorf("saving snapshot ref: %w", err))
			} else {
				res.Ref = ref
			}
		}
		if wantPatch {
			if dir, err := writePatchBackup(ctx, repoPath, cfg.PatchDir, res.CopyID, id, snap, subs, cfg.Now()); err != nil {
				errs = append(errs, fmt.Errorf("saving patch backup: %w", err))
			} else {
				res.PatchDir = dir
			}
		}
		if res.Ref != "" || res.PatchDir != "" {
			res.Outcome, res.ID = BackupSaved, id
		}
		// Prune only the kinds this run added to, so a failed run never
		// pushes out the last good backup.
		if prune && res.Ref != "" {
			pruneBackupRefs(ctx, repoPath, res.CopyID, cfg.Retention, cfg.Now())
		}
		if prune && res.PatchDir != "" {
			prunePatchBackups(cfg.PatchDir, res.CopyID, cfg.Retention, cfg.Now())
		}
	}

	if len(errs) > 0 {
		return res, fmt.Errorf("backing up %s: %w", repoPath, errors.Join(errs...))
	}
	return res, nil
}

// saveSnapshotRef points ref at the snapshot commit and, when there are
// submodules' backups, subsRef at a blob listing them. The list is written
// first, so a ref is never left without the submodules that go with it.
func saveSnapshotRef(ctx context.Context, repoPath, ref, subsRef, commit string, subs map[string]SubmoduleBackup) error {
	if len(subs) > 0 {
		data, err := json.Marshal(subs)
		if err != nil {
			return err
		}
		blob, err := gitWithStdin(ctx, repoPath, nil, data, "hash-object", "-w", "--stdin")
		if err != nil {
			return err
		}
		if _, err := Git(ctx, repoPath, "update-ref", subsRef, blob, ""); err != nil {
			return err
		}
	}
	_, err := Git(ctx, repoPath, "update-ref", ref, commit, "")
	return err
}

// newBackupID names a backup taken at now, later than latest so IDs stay
// unique and ordered even within one millisecond.
func newBackupID(now time.Time, latest *BackupInfo) string {
	at := now.UTC().Truncate(time.Millisecond)
	if latest != nil {
		if prev, err := time.Parse(backupIDLayout, latest.ID); err == nil && !at.After(prev) {
			at = prev.Add(time.Millisecond)
		}
	}
	return at.Format(backupIDLayout)
}

// partialSuffix marks a patch backup still being written. Only complete
// backups carry a bare ID, so listing and retention never see a partial one.
const partialSuffix = ".partial"

// writePatchBackup exports snap as files under <patchRoot>/<copyID>/<id>: the
// staged and unstaged changes as patches, the untracked files as a tarball,
// and metadata.json. They outlive the repository. The directory is built under
// a partial name and published with one rename.
func writePatchBackup(ctx context.Context, repoPath, patchRoot, copyID, id string, snap Snapshot, subs map[string]SubmoduleBackup, now time.Time) (string, error) {
	dir := filepath.Join(patchRoot, copyID, id)
	partial := dir + partialSuffix
	if err := pathutil.MkdirAll(partial, 0o700); err != nil {
		return "", fmt.Errorf("creating patch dir: %w", err)
	}
	if err := writePatchFiles(ctx, repoPath, partial, copyID, id, snap, subs, now); err != nil {
		os.RemoveAll(partial)
		return "", err
	}
	// On disk before it is published: older backups are pruned against it.
	if err := pathutil.SyncTree(partial); err != nil {
		os.RemoveAll(partial)
		return "", err
	}
	if err := os.Rename(partial, dir); err != nil {
		os.RemoveAll(partial)
		return "", fmt.Errorf("publishing patch backup: %w", err)
	}
	if err := pathutil.SyncDir(filepath.Dir(dir)); err != nil {
		return "", err
	}
	if err := writeCopyRecord(ctx, repoPath, patchRoot, copyID); err != nil {
		slog.Warn("recording copy for patch backups", "repo", repoPath, "error", err)
	}
	return dir, nil
}

func writePatchFiles(ctx context.Context, repoPath, dir, copyID, id string, snap Snapshot, subs map[string]SubmoduleBackup, now time.Time) error {
	baseTree, err := treeOf(ctx, repoPath, snap.Base)
	if err != nil {
		return err
	}
	if err := writeTreeDiff(ctx, repoPath, filepath.Join(dir, stagedPatch), baseTree, snap.Index+"^{tree}"); err != nil {
		return fmt.Errorf("staged changes: %w", err)
	}
	if err := writeTreeDiff(ctx, repoPath, filepath.Join(dir, unstagedPatch), snap.Index+"^{tree}", snap.Commit+"^{tree}"); err != nil {
		return fmt.Errorf("unstaged changes: %w", err)
	}
	if snap.Untracked != "" {
		if err := writeTreeTarball(ctx, repoPath, snap.Untracked+"^{tree}", filepath.Join(dir, untrackedTarball)); err != nil {
			return fmt.Errorf("untracked files: %w", err)
		}
	}
	meta := BackupMeta{
		ID: id, CopyID: copyID, Path: repoPath, Created: now.UTC(),
		Snapshot: snap.Commit, Base: snap.Base, Branch: currentBranchLabel(ctx, repoPath),
		IndexUnmerged: snap.IndexUnmerged, Skipped: snap.Skipped,
	}
	if len(subs) > 0 {
		meta.Submodules = subs
	}
	return writeJSON(filepath.Join(dir, metadataFile), meta)
}

// writeTreeDiff writes the binary diff between two trees to path, or nothing
// when they are equal. Fixed options keep user diff settings out of the patch.
func writeTreeDiff(ctx context.Context, repoPath, path, from, to string) error {
	patch, err := GitOutput(ctx, repoPath, "diff", "--binary", "--no-color", "--no-ext-diff", "--no-textconv",
		"--no-renames", "--no-relative", "--src-prefix=a/", "--dst-prefix=b/", from, to)
	if err != nil {
		return err
	}
	if len(patch) == 0 {
		return nil
	}
	return os.WriteFile(path, patch, 0o600)
}

// writeTreeTarball writes every file of tree to a gzipped tar at path,
// symlinks as symlinks and executables as executable. It reads blobs straight
// from the object store, so paths need no quoting and attributes such as
// export-ignore don't apply.
func writeTreeTarball(ctx context.Context, repoPath, tree, path string) error {
	listing, err := GitOutput(ctx, repoPath, "ls-tree", "-r", "-z", "--full-tree", tree)
	if err != nil {
		return err
	}
	type entry struct{ mode, oid, path string }
	var entries []entry
	var oids strings.Builder
	for _, rec := range strings.Split(strings.TrimSuffix(string(listing), "\x00"), "\x00") {
		if rec == "" {
			continue
		}
		meta, name, ok := strings.Cut(rec, "\t")
		fields := strings.Fields(meta)
		if !ok || len(fields) != 3 || fields[1] != "blob" {
			return fmt.Errorf("unexpected ls-tree entry %q", rec)
		}
		entries = append(entries, entry{fields[0], fields[2], name})
		oids.WriteString(fields[2] + "\n")
	}

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)

	// Cancelling on a write error stops cat-file, which would otherwise block
	// on a full pipe and hang Wait.
	catCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := gitCommand(catCtx, repoPath, nil, "cat-file", "--batch")
	cmd.Stdin = strings.NewReader(oids.String())
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	batch := bufio.NewReader(stdout)
	writeErr := func() error {
		for _, e := range entries {
			header, err := batch.ReadString('\n')
			if err != nil {
				return fmt.Errorf("reading %s: %w", e.path, err)
			}
			fields := strings.Fields(header)
			if len(fields) != 3 || fields[0] != e.oid {
				return fmt.Errorf("reading %s: unexpected cat-file header %q", e.path, strings.TrimSpace(header))
			}
			size, err := strconv.ParseInt(fields[2], 10, 64)
			if err != nil {
				return fmt.Errorf("reading %s: %w", e.path, err)
			}
			hdr := &tar.Header{Name: e.path, Mode: 0o644, Size: size, Typeflag: tar.TypeReg, ModTime: time.Unix(0, 0)}
			switch e.mode {
			case "100755":
				hdr.Mode = 0o755
			case "120000":
				target := make([]byte, size)
				if _, err := io.ReadFull(batch, target); err != nil {
					return fmt.Errorf("reading %s: %w", e.path, err)
				}
				hdr.Typeflag, hdr.Linkname, hdr.Size, hdr.Mode = tar.TypeSymlink, string(target), 0, 0o777
			}
			if err := tw.WriteHeader(hdr); err != nil {
				return err
			}
			if hdr.Typeflag == tar.TypeReg {
				if _, err := io.CopyN(tw, batch, size); err != nil {
					return fmt.Errorf("archiving %s: %w", e.path, err)
				}
			}
			if _, err := batch.Discard(1); err != nil { // newline after each object
				return fmt.Errorf("reading %s: %w", e.path, err)
			}
		}
		return nil
	}()
	if writeErr != nil {
		cancel()
		_ = cmd.Wait() // killed: the write's error is the one to report
		return writeErr
	}
	waitErr := cmd.Wait()
	if waitErr != nil {
		return fmt.Errorf("git cat-file: %w: %s", waitErr, strings.TrimSpace(stderr.String()))
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}
	return f.Close()
}

// writeCopyRecord notes in copy.json which working copy a patch backup
// directory belongs to, so backups can be found after the copy is re-cloned.
func writeCopyRecord(ctx context.Context, repoPath, patchRoot, copyID string) error {
	return writeJSON(filepath.Join(patchRoot, copyID, copyRecordFile), copyRecord{
		CopyID: copyID, Path: repoPath, Remotes: remoteURLs(ctx, repoPath),
	})
}

// writeJSON writes v to path atomically.
func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return pathutil.WriteFileAtomic(path, append(data, '\n'), 0o600)
}

func pruneBackupRefs(ctx context.Context, repoPath, copyID string, r Retention, now time.Time) {
	prefix := backupRefPrefix + copyID + "/"
	out, err := Git(ctx, repoPath, "for-each-ref", "--format=%(refname)", prefix)
	if err != nil || out == "" {
		return
	}
	var ids []string
	for _, ref := range strings.Split(out, "\n") {
		ids = append(ids, strings.TrimPrefix(ref, prefix))
	}
	for _, id := range r.expired(ids, now) {
		if _, err := Git(ctx, repoPath, "update-ref", "-d", prefix+id); err != nil {
			slog.Warn("pruning backup ref", "repo", repoPath, "ref", prefix+id, "error", err)
			continue
		}
		deleteRefs(ctx, repoPath, submoduleRefPrefix+copyID+"/"+id)
	}
}

func prunePatchBackups(patchRoot, copyID string, r Retention, now time.Time) {
	dir := filepath.Join(patchRoot, copyID)
	for _, id := range r.expired(patchBackupIDs(dir), now) {
		if err := os.RemoveAll(filepath.Join(dir, id)); err != nil {
			slog.Warn("pruning patch backup", "dir", filepath.Join(dir, id), "error", err)
		}
	}
}

// pruneSubmoduleBackups drops the backups of the submodules at paths that no
// backup of the parent at repoPath still names. A parent backup restores the
// submodule backups it names, so they live exactly as long as it does; each
// submodule's latest is kept besides. It runs only after the parent's backup
// succeeded, so a failed run leaves every earlier backup restorable.
func pruneSubmoduleBackups(ctx context.Context, repoPath, patchRoot string, paths []string) {
	parents, err := listCopyBackups(ctx, repoPath, patchRoot, ReadCopyID(ctx, repoPath))
	if err != nil {
		return
	}
	named := map[string]bool{} // copy ID + "/" + backup ID
	for _, b := range parents {
		for _, sb := range b.Submodules {
			named[sb.CopyID+"/"+sb.ID] = true
		}
	}
	for _, path := range paths {
		subPath := filepath.Join(repoPath, path)
		copyID := ReadCopyID(ctx, subPath)
		if copyID == "" {
			continue
		}
		backups, err := listCopyBackups(ctx, subPath, patchRoot, copyID)
		if err != nil || len(backups) == 0 {
			continue
		}
		for _, b := range backups[:len(backups)-1] {
			if named[copyID+"/"+b.ID] {
				continue
			}
			if b.Ref != "" {
				if _, err := Git(ctx, subPath, "update-ref", "-d", b.Ref); err != nil {
					slog.Warn("pruning submodule backup ref", "repo", subPath, "ref", b.Ref, "error", err)
				}
			}
			if b.PatchDir != "" {
				if err := os.RemoveAll(b.PatchDir); err != nil {
					slog.Warn("pruning submodule patch backup", "dir", b.PatchDir, "error", err)
				}
			}
		}
	}
}

// deleteCopyBackupRefs drops every snapshot ref of a copy, for a linked
// worktree that is gone; its refs would otherwise live on in the shared repo.
func deleteCopyBackupRefs(ctx context.Context, repoPath, copyID string) {
	if copyID != "" {
		deleteRefs(ctx, repoPath, backupRefPrefix+copyID+"/")
		deleteRefs(ctx, repoPath, submoduleRefPrefix+copyID+"/")
	}
}
