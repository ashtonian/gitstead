package gitops

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ashtonian/git-mgr/internal/shellutil"
)

// Files of a patch backup directory, and the record of the copy it belongs to.
const (
	stagedPatch      = "staged.patch"
	unstagedPatch    = "unstaged.patch"
	untrackedTarball = "untracked.tar.gz"
	metadataFile     = "metadata.json"
	copyRecordFile   = "copy.json"
)

// ErrNoBackup reports that a working copy has no backup, or none by that ID.
var ErrNoBackup = errors.New("no backup found")

// BackupMeta is a patch backup's metadata.json.
type BackupMeta struct {
	ID            string        `json:"id"`
	CopyID        string        `json:"copy_id"`
	Path          string        `json:"path"`
	Created       time.Time     `json:"created"`
	Snapshot      string        `json:"snapshot"`
	Base          string        `json:"base,omitempty"`
	Branch        string        `json:"branch"`
	IndexUnmerged bool          `json:"index_unmerged,omitempty"`
	Skipped       []SkippedFile `json:"skipped,omitempty"`
}

type copyRecord struct {
	CopyID  string            `json:"copy_id"`
	Path    string            `json:"path"`
	Remotes map[string]string `json:"remotes,omitempty"`
}

// BackupInfo is one backup of a working copy: a snapshot ref in the repo, a
// patch backup directory, or both.
type BackupInfo struct {
	ID       string
	CopyID   string
	Ref      string // "" if there is no snapshot ref
	Commit   string // the snapshot ref's commit
	PatchDir string // "" if there is no patch backup
	Meta     *BackupMeta
}

func (b BackupInfo) snapshot() string {
	if b.Commit != "" {
		return b.Commit
	}
	if b.Meta != nil {
		return b.Meta.Snapshot
	}
	return ""
}

// RestoreCommands returns the shell commands that restore b by hand from
// inside the working copy, onto a clean tree.
func (b BackupInfo) RestoreCommands() []string {
	if b.Ref != "" {
		return []string{"git stash apply --index " + b.Ref}
	}
	var cmds []string
	if p := filepath.Join(b.PatchDir, stagedPatch); b.PatchDir != "" && fileExists(p) {
		cmds = append(cmds, "git apply --index "+shellutil.Quote(p))
	}
	if p := filepath.Join(b.PatchDir, unstagedPatch); b.PatchDir != "" && fileExists(p) {
		cmds = append(cmds, "git apply "+shellutil.Quote(p))
	}
	if p := filepath.Join(b.PatchDir, untrackedTarball); b.PatchDir != "" && fileExists(p) {
		cmds = append(cmds, "tar -xzkf "+shellutil.Quote(p)+"   # untracked files; -k keeps existing ones")
	}
	return cmds
}

// ListBackups returns the backups of the working copy at repoPath, oldest
// first: its own, plus patch backups left by earlier copies at the same path,
// such as a clone that was deleted and cloned again.
func ListBackups(ctx context.Context, repoPath, patchRoot string) ([]BackupInfo, error) {
	var all []BackupInfo
	copyID := existingCopyID(ctx, repoPath)
	if copyID != "" {
		own, err := listCopyBackups(ctx, repoPath, patchRoot, copyID)
		if err != nil {
			return nil, err
		}
		all = append(all, own...)
	}
	for _, earlier := range copiesAtPath(patchRoot, repoPath) {
		if earlier == copyID {
			continue
		}
		for _, id := range patchBackupIDs(filepath.Join(patchRoot, earlier)) {
			all = append(all, patchBackup(patchRoot, earlier, id))
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].ID < all[j].ID })
	return all, nil
}

// FindBackup returns the backup of the copy at repoPath with the given ID, or
// its newest backup when id is "" or "latest".
func FindBackup(ctx context.Context, repoPath, patchRoot, id string) (BackupInfo, error) {
	backups, err := ListBackups(ctx, repoPath, patchRoot)
	if err != nil {
		return BackupInfo{}, err
	}
	if id == "" || id == "latest" {
		if b := lastBackup(backups); b != nil {
			return *b, nil
		}
		return BackupInfo{}, fmt.Errorf("%w for %s", ErrNoBackup, repoPath)
	}
	for _, b := range backups {
		if b.ID == id {
			return b, nil
		}
	}
	return BackupInfo{}, fmt.Errorf("%w with ID %s for %s", ErrNoBackup, id, repoPath)
}

// listCopyBackups merges one copy's snapshot refs and patch backups by ID,
// oldest first.
func listCopyBackups(ctx context.Context, repoPath, patchRoot, copyID string) ([]BackupInfo, error) {
	byID := map[string]*BackupInfo{}
	prefix := backupRefPrefix + copyID + "/"
	out, err := Git(ctx, repoPath, "for-each-ref", "--format=%(refname) %(objectname)", prefix)
	if err != nil {
		return nil, fmt.Errorf("listing backups of %s: %w", repoPath, err)
	}
	for _, line := range strings.Split(out, "\n") {
		ref, oid, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		id := strings.TrimPrefix(ref, prefix)
		byID[id] = &BackupInfo{ID: id, CopyID: copyID, Ref: ref, Commit: oid}
	}
	if patchRoot != "" {
		for _, id := range patchBackupIDs(filepath.Join(patchRoot, copyID)) {
			p := patchBackup(patchRoot, copyID, id)
			if b, ok := byID[id]; ok {
				b.PatchDir, b.Meta = p.PatchDir, p.Meta
			} else {
				byID[id] = &p
			}
		}
	}
	backups := make([]BackupInfo, 0, len(byID))
	for _, b := range byID {
		backups = append(backups, *b)
	}
	sort.Slice(backups, func(i, j int) bool { return backups[i].ID < backups[j].ID })
	return backups, nil
}

func patchBackup(patchRoot, copyID, id string) BackupInfo {
	dir := filepath.Join(patchRoot, copyID, id)
	b := BackupInfo{ID: id, CopyID: copyID, PatchDir: dir}
	if data, err := os.ReadFile(filepath.Join(dir, metadataFile)); err == nil {
		var meta BackupMeta
		if json.Unmarshal(data, &meta) == nil {
			b.Meta = &meta
		}
	}
	return b
}

// patchBackupIDs lists the complete backups in one copy's patch directory.
func patchBackupIDs(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var ids []string
	for _, e := range entries {
		if e.IsDir() && !strings.HasSuffix(e.Name(), partialSuffix) {
			ids = append(ids, e.Name())
		}
	}
	return ids
}

// copiesAtPath returns the IDs of copies whose patch backups were taken at path.
func copiesAtPath(patchRoot, path string) []string {
	if patchRoot == "" {
		return nil
	}
	entries, err := os.ReadDir(patchRoot)
	if err != nil {
		return nil
	}
	var ids []string
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(patchRoot, e.Name(), copyRecordFile))
		if err != nil {
			continue
		}
		var rec copyRecord
		if json.Unmarshal(data, &rec) == nil && rec.Path == path && rec.CopyID == e.Name() {
			ids = append(ids, rec.CopyID)
		}
	}
	return ids
}

func lastBackup(backups []BackupInfo) *BackupInfo {
	if len(backups) == 0 {
		return nil
	}
	return &backups[len(backups)-1]
}

// ApplyBackup restores backup b onto the working copy at repoPath, which the
// caller has checked is safe to change. A snapshot ref is applied with `git
// stash apply --index`, which restores the staged and unstaged changes and the
// untracked files and stops rather than overwrite an existing untracked file;
// if HEAD has moved since the backup, git merges and reports any conflict.
// Without a ref the patch backup is applied instead.
func ApplyBackup(ctx context.Context, repoPath string, b BackupInfo) error {
	if b.Commit != "" && GitExitCode(ctx, repoPath, "cat-file", "-e", b.Commit+"^{commit}") == 0 {
		return applySnapshot(ctx, repoPath, b.Commit)
	}
	if b.PatchDir != "" {
		return applyPatchBackup(ctx, repoPath, b.PatchDir)
	}
	return fmt.Errorf("backup %s has neither a snapshot in %s nor a patch backup", b.ID, repoPath)
}

// RestoreBackupInto clones the copy at repoPath into the empty or missing
// directory into, checks out the backup's base commit there and restores the
// backup on top, leaving the original working copy untouched.
func RestoreBackupInto(ctx context.Context, repoPath string, b BackupInfo, into string) error {
	if entries, err := os.ReadDir(into); err == nil && len(entries) > 0 {
		return fmt.Errorf("%s exists and is not empty", into)
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(into), 0o755); err != nil {
		return err
	}
	if _, err := Git(ctx, filepath.Dir(into), "clone", "-q", "--no-checkout", repoPath, into); err != nil {
		return fmt.Errorf("cloning %s: %w", repoPath, err)
	}

	if b.Ref != "" && b.Commit != "" {
		if _, err := Git(ctx, into, "fetch", "-q", repoPath, b.Ref); err != nil {
			return fmt.Errorf("fetching snapshot %s: %w", b.Ref, err)
		}
		snap, err := readSnapshot(ctx, into, b.Commit)
		if err != nil {
			return err
		}
		if err := checkoutBase(ctx, into, snap.Base); err != nil {
			return err
		}
		return applySnapshot(ctx, into, b.Commit)
	}

	base := ""
	if b.Meta != nil {
		base = b.Meta.Base
	}
	if base != "" && GitExitCode(ctx, into, "cat-file", "-e", base+"^{commit}") != 0 {
		return fmt.Errorf("the backup's base commit %s is not in %s; apply the patches in %s by hand", base, repoPath, b.PatchDir)
	}
	if err := checkoutBase(ctx, into, base); err != nil {
		return err
	}
	return applyPatchBackup(ctx, into, b.PatchDir)
}

// checkoutBase puts a fresh clone on the backup's base commit, or on an empty
// orphan branch for a backup taken before the first commit.
func checkoutBase(ctx context.Context, repoPath, base string) error {
	args := []string{"checkout", "-q", "--detach", base}
	if base == "" {
		args = []string{"switch", "-q", "--orphan", "git-mgr-restore"}
	}
	if _, err := Git(ctx, repoPath, args...); err != nil {
		return fmt.Errorf("checking out the backup's base: %w", err)
	}
	return nil
}

func applySnapshot(ctx context.Context, repoPath, commit string) error {
	snap, err := readSnapshot(ctx, repoPath, commit)
	if err != nil {
		return err
	}
	if snap.Base != "" {
		if _, err := Git(ctx, repoPath, "stash", "apply", "-q", "--index", commit); err != nil {
			return fmt.Errorf("applying snapshot %s: %w", commit, err)
		}
		return nil
	}
	// git stash refuses to run before the first commit. Write the tracked and
	// untracked trees into the working tree, then set the index to the
	// recorded one without touching the files.
	steps := [][]string{
		{"read-tree", commit + "^{tree}"}, {"checkout-index", "-a"},
	}
	if snap.Untracked != "" {
		steps = append(steps, []string{"read-tree", snap.Untracked + "^{tree}"}, []string{"checkout-index", "-a"})
	}
	steps = append(steps, []string{"read-tree", snap.Index + "^{tree}"})
	for _, args := range steps {
		if _, err := Git(ctx, repoPath, args...); err != nil {
			return fmt.Errorf("restoring snapshot %s: %w", commit, err)
		}
	}
	return nil
}

// applyPatchBackup applies a patch backup: the staged patch to the index and
// working tree, the unstaged patch to the working tree, then the untracked
// files, refusing to overwrite any that exist.
func applyPatchBackup(ctx context.Context, repoPath, dir string) error {
	if p := filepath.Join(dir, stagedPatch); fileExists(p) {
		if _, err := Git(ctx, repoPath, "apply", "--index", p); err != nil {
			return fmt.Errorf("applying staged changes: %w", err)
		}
	}
	if p := filepath.Join(dir, unstagedPatch); fileExists(p) {
		if _, err := Git(ctx, repoPath, "apply", p); err != nil {
			return fmt.Errorf("applying unstaged changes: %w", err)
		}
	}
	if p := filepath.Join(dir, untrackedTarball); fileExists(p) {
		if err := extractTarball(p, repoPath); err != nil {
			return fmt.Errorf("restoring untracked files: %w", err)
		}
	}
	return nil
}

// extractTarball unpacks an untracked-files tarball into dir. It refuses
// entries that would land outside dir or replace an existing file.
func extractTarball(path, dir string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	root, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		name := filepath.FromSlash(hdr.Name)
		if !filepath.IsLocal(name) {
			return fmt.Errorf("refusing tar entry %q outside the working tree", hdr.Name)
		}
		target := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if resolved, err := filepath.EvalSymlinks(filepath.Dir(target)); err != nil || !withinDir(root, resolved) {
			return fmt.Errorf("refusing tar entry %q: its directory leaves the working tree", hdr.Name)
		}
		switch hdr.Typeflag {
		case tar.TypeSymlink:
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return err
			}
		case tar.TypeReg:
			out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fs.FileMode(hdr.Mode)&0o777)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return err
			}
			if err := out.Close(); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unexpected tar entry type %q for %s", hdr.Typeflag, hdr.Name)
		}
	}
}

func withinDir(root, path string) bool {
	rootResolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(rootResolved, path)
	return err == nil && filepath.IsLocal(rel)
}
