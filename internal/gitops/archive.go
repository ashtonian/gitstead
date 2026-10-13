package gitops

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ashtonian/git-mgr/internal/pathutil"
)

// archiveRefPrefix holds the temporary refs a bundle is built from. Each
// archive gets its own sub-namespace because linked worktrees share refs.
const archiveRefPrefix = "refs/git-mgr/archive/"

// ArchiveMeta describes an archive bundle and is written beside it as JSON.
type ArchiveMeta struct {
	Source  string            `json:"source"`
	Remotes map[string]string `json:"remotes,omitempty"`
	Branch  string            `json:"branch,omitempty"`
	Work    string            `json:"work"`
	Created time.Time         `json:"created"`
	// Bundle is the repository's own bundle; "" when only its submodules
	// had work to save.
	Bundle  string `json:"bundle,omitempty"`
	Restore string `json:"restore,omitempty"`
	// Submodules maps each submodule with work saved to its own archive.
	Submodules map[string]string `json:"submodules,omitempty"`
	// LFS holds the clone's Git LFS objects, which a bundle carries only
	// pointers to; copy them into the restored repo's .git/lfs/objects.
	LFS string `json:"lfs,omitempty"`
	// Clone is where the whole clone was moved, for an archive that kept
	// all of it rather than a bundle of its unique work.
	Clone string `json:"clone,omitempty"`
}

type archiveScope int

const (
	scopeClone    archiveScope = iota // everything deleting a whole clone would lose
	scopeWorktree                     // what removing a linked worktree would lose
)

// ArchiveUniqueWork saves the unique work of the clone at repoPath into one
// verified git bundle in dir and returns its path, or "" when there is nothing
// to save. The bundle holds local branches and HEAD minus what any remote
// already has, every stash entry, and a snapshot of the index, working tree and
// untracked files that `git stash apply --index` restores. A JSON file beside
// it records the remotes and the restore command. Each submodule with work
// of its own gets its own archive, and a clone's Git LFS objects are kept
// beside the bundle. When only submodules had work, the JSON file's path is
// returned.
func ArchiveUniqueWork(ctx context.Context, repoPath, dir, name string) (string, error) {
	return archive(ctx, repoPath, dir, name, scopeClone)
}

func archive(ctx context.Context, repoPath, dir, name string, scope archiveScope) (string, error) {
	work, err := InspectUniqueWork(ctx, repoPath)
	if err != nil {
		return "", err
	}
	lost := work.LostWithClone()
	if scope == scopeWorktree {
		lost = work.LostWithWorktree()
	}
	if !lost {
		return "", nil
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("creating archive dir: %w", err)
	}
	id := newArchiveID(name)
	bundle := filepath.Join(dir, id+".bundle")
	metaPath := filepath.Join(dir, id+".json")
	meta := ArchiveMeta{Source: repoPath, Remotes: remoteURLs(ctx, repoPath), Work: work.String(), Created: time.Now().UTC()}
	meta.Branch, _ = Git(ctx, repoPath, "symbolic-ref", "-q", "--short", "HEAD")

	for _, sub := range slices.Sorted(maps.Keys(work.Submodules)) {
		path, err := archive(ctx, filepath.Join(repoPath, sub), dir, name+"-"+safeName(sub), scopeClone)
		if err != nil {
			return "", fmt.Errorf("archiving submodule %s: %w", sub, err)
		}
		if meta.Submodules == nil {
			meta.Submodules = map[string]string{}
		}
		meta.Submodules[sub] = path
	}
	own := work
	own.Submodules = nil
	ownLost := own.LostWithClone()
	if scope == scopeWorktree {
		ownLost = own.LostWithWorktree()
	}
	if scope == scopeClone {
		lfs := filepath.Join(ResolveGitDirs(ctx, repoPath).CommonDir, "lfs", "objects")
		if hasFiles(lfs) {
			meta.LFS = filepath.Join(dir, id+".lfs")
			if err := linkTree(lfs, meta.LFS); err != nil {
				return "", fmt.Errorf("keeping LFS objects: %w", err)
			}
		}
	}
	if !ownLost {
		if err := writeMeta(metaPath, meta); err != nil {
			return "", err
		}
		return metaPath, nil
	}

	refs := archiveRefPrefix + id + "/"
	defer deleteRefs(ctx, repoPath, refs)

	setRef := func(ref, oid string) error {
		_, err := Git(ctx, repoPath, "update-ref", refs+ref, oid)
		return err
	}
	if work.inWorkingTree() {
		// No limits: whatever the snapshot leaves out would be deleted.
		snap, err := TakeSnapshot(ctx, repoPath, SnapshotLimits{Untracked: true})
		if err != nil {
			return "", fmt.Errorf("snapshotting working tree: %w", err)
		}
		if len(snap.Skipped) > 0 {
			return "", fmt.Errorf("cannot archive %s (%s); move it out first", snap.Skipped[0].Path, snap.Skipped[0].Reason)
		}
		if err := setRef("worktree", snap.Commit); err != nil {
			return "", err
		}
	}
	if GitExitCode(ctx, repoPath, "rev-parse", "--verify", "-q", "HEAD") == 0 {
		if err := setRef("head", "HEAD"); err != nil {
			return "", err
		}
	}
	if scope == scopeClone {
		for i := 0; i < work.Stashes; i++ {
			oid, err := Git(ctx, repoPath, "rev-parse", "stash@{"+strconv.Itoa(i)+"}")
			if err != nil {
				return "", fmt.Errorf("reading stash %d: %w", i, err)
			}
			if err := setRef("stash-"+strconv.Itoa(i), oid); err != nil {
				return "", err
			}
		}
	}

	args := []string{"bundle", "create", "-q", bundle}
	if scope == scopeClone {
		args = append(args, "--branches")
	}
	args = append(args, "--glob="+refs+"*", "--not", "--remotes")
	if scope == scopeWorktree {
		args = append(args, "--branches") // branch commits survive in the shared repo
	}
	if _, err := Git(ctx, repoPath, args...); err != nil {
		os.Remove(bundle)
		return "", fmt.Errorf("writing bundle: %w", err)
	}
	if _, err := Git(ctx, repoPath, "bundle", "verify", "-q", bundle); err != nil {
		os.Remove(bundle)
		return "", fmt.Errorf("verifying bundle: %w", err)
	}

	meta.Bundle = bundle
	meta.Restore = fmt.Sprintf("git clone <remote> restored && cd restored && git fetch %s '+refs/*:refs/restored/*'"+
		" && git stash apply --index refs/restored/%sworktree", bundle, strings.TrimPrefix(refs, "refs/"))
	if err := writeMeta(metaPath, meta); err != nil {
		return "", err
	}
	return bundle, nil
}

// ReadArchive returns the description of the archive at path -- a bundle,
// an archived clone or its description -- and the archive's ID: its file
// name without extension.
func ReadArchive(path string) (ArchiveMeta, string, error) {
	base := archiveBase(path)
	var meta ArchiveMeta
	data, err := os.ReadFile(base + ".json")
	if err != nil {
		return meta, "", err
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return meta, "", fmt.Errorf("reading %s.json: %w", base, err)
	}
	return meta, filepath.Base(base), nil
}

// DeleteArchive deletes the archive at path: its bundle or archived clone,
// its description, its LFS objects and its submodules' archives. What is
// already gone is skipped. Only files of the archive's own name, beside it,
// are deleted, whatever its description says.
func DeleteArchive(path string) error {
	base := archiveBase(path)
	meta, _, err := ReadArchive(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	for _, sub := range slices.Sorted(maps.Values(meta.Submodules)) {
		if filepath.Dir(sub) != filepath.Dir(base) {
			return fmt.Errorf("%s's submodule archive %s isn't beside it", path, sub)
		}
		if err := DeleteArchive(sub); err != nil {
			return err
		}
	}
	dirs := []string{base + ".lfs"}
	if path == base {
		dirs = append(dirs, base) // an archived clone
	}
	for _, dir := range dirs {
		if err := pathutil.SafeRemoveAll(dir); err != nil {
			return err
		}
	}
	for _, f := range []string{base + ".bundle", base + ".json"} {
		if err := os.Remove(f); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

// archiveBase is an archive's path without extension: its archived clone,
// or the name its bundle and description share.
func archiveBase(path string) string {
	return strings.TrimSuffix(strings.TrimSuffix(path, ".bundle"), ".json")
}

func writeMeta(path string, meta ArchiveMeta) error {
	var data bytes.Buffer
	enc := json.NewEncoder(&data)
	enc.SetEscapeHTML(false) // keep the restore command copy-pasteable
	enc.SetIndent("", "  ")
	if err := enc.Encode(meta); err != nil {
		return err
	}
	if err := os.WriteFile(path, data.Bytes(), 0o600); err != nil {
		return fmt.Errorf("writing archive metadata: %w", err)
	}
	return nil
}

// hasFiles reports whether dir holds any file, at any depth.
func hasFiles(dir string) bool {
	found := false
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

// linkTree makes dst a copy of the files under src: hardlinks where it can,
// which cost no space, copies across filesystems.
func linkTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		if os.Link(path, target) == nil {
			return nil
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			out.Close()
			return err
		}
		return out.Close()
	})
}

// ErrHasWorktrees reports a clone with linked worktrees: deleting it would
// delete their branches and history, and leave their work unreadable.
var ErrHasWorktrees = errors.New("it has linked worktrees")

// RemoveClone deletes the clone at repoPath. Unless discard is set it first
// archives the clone's unique work into archiveDir; if that work cannot be
// inspected or archived the clone is left in place and an error is returned.
// A clone with linked worktrees on disk is always left in place, discard or
// not. A clone already gone from disk counts as removed. It returns the
// archive path, or "" when there was nothing to save.
func RemoveClone(ctx context.Context, repoPath, archiveDir, name string, discard bool) (string, error) {
	if !filepath.IsAbs(repoPath) {
		return "", fmt.Errorf("refusing to remove clone at non-absolute path %q", repoPath)
	}
	if _, err := os.Lstat(repoPath); errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if linked, err := LinkedWorktrees(ctx, repoPath); err != nil {
		return "", fmt.Errorf("keeping %s: listing its worktrees: %w", repoPath, err)
	} else if len(linked) > 0 {
		return "", fmt.Errorf("keeping %s: %w (%s); remove them first", repoPath, ErrHasWorktrees, strings.Join(linked, ", "))
	}
	var archived string
	if !discard {
		var err error
		if archived, err = archive(ctx, repoPath, archiveDir, name, scopeClone); err != nil {
			return "", fmt.Errorf("keeping %s: %w", repoPath, err)
		}
	}
	if err := pathutil.SafeRemoveAll(repoPath); err != nil {
		return archived, err
	}
	return archived, nil
}

// ArchiveClone moves the whole clone at repoPath into dir, as dir/<id>, with
// a JSON description beside it at dir/<id>.json, and returns its new path, or
// "" when it is already gone from disk. Moving loses nothing, so a clone
// whose work can't be inspected -- or that holds a nested repository a
// bundle can't -- is moved all the same. A clone with linked worktrees on
// disk is left in place: moving it would cut them off from their history.
// With a *pathutil.LeftoverError the clone is archived at the returned path,
// and only deleting what was left at repoPath failed.
func ArchiveClone(ctx context.Context, repoPath, dir, name string) (string, error) {
	if !filepath.IsAbs(repoPath) {
		return "", fmt.Errorf("refusing to archive clone at non-absolute path %q", repoPath)
	}
	if _, err := os.Lstat(repoPath); errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if linked, err := LinkedWorktrees(ctx, repoPath); err != nil {
		return "", fmt.Errorf("keeping %s: listing its worktrees: %w", repoPath, err)
	} else if len(linked) > 0 {
		return "", fmt.Errorf("keeping %s: %w (%s); remove them first", repoPath, ErrHasWorktrees, strings.Join(linked, ", "))
	}

	id := newArchiveID(name)
	dest := filepath.Join(dir, id)
	meta := ArchiveMeta{
		Source:  repoPath,
		Remotes: remoteURLs(ctx, repoPath),
		Work:    "the whole clone",
		Created: time.Now().UTC(),
		Clone:   dest,
		Restore: "git-mgr archives restore " + id,
	}
	meta.Branch, _ = Git(ctx, repoPath, "symbolic-ref", "-q", "--short", "HEAD")
	switch work, err := InspectUniqueWork(ctx, repoPath); {
	case err != nil:
		meta.Work += fmt.Sprintf(" (its work couldn't be inspected: %v)", err)
	case work.LostWithClone():
		meta.Work += ", with " + work.String()
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("creating archive dir: %w", err)
	}
	metaPath := dest + ".json"
	if err := writeMeta(metaPath, meta); err != nil {
		return "", err
	}
	if err := pathutil.Move(repoPath, dest); err != nil {
		if leftover := (*pathutil.LeftoverError)(nil); errors.As(err, &leftover) {
			return dest, err
		}
		os.Remove(metaPath)
		return "", fmt.Errorf("keeping %s: %w", repoPath, err)
	}
	return dest, nil
}

// RemoveWorktree removes the linked worktree at path from source. Unless
// discard is set it first archives what removal would lose (the working tree,
// index and detached-HEAD commits); branches stay in source. On an inspection
// or archive failure the worktree is left in place. The worktree's backup refs
// go with it.
func RemoveWorktree(ctx context.Context, source, path, archiveDir, name string, discard bool) (string, error) {
	copyID := ReadCopyID(ctx, path)
	var archived string
	if !discard {
		var err error
		if archived, err = archive(ctx, path, archiveDir, name, scopeWorktree); err != nil {
			return "", fmt.Errorf("keeping %s: %w", path, err)
		}
	}
	if err := WorktreeRemove(ctx, source, path, true); err != nil {
		return archived, err
	}
	deleteCopyBackupRefs(ctx, source, copyID)
	return archived, nil
}

func deleteRefs(ctx context.Context, repoPath, prefix string) {
	out, err := Git(ctx, repoPath, "for-each-ref", "--format=%(refname)", prefix)
	if err != nil || out == "" {
		return
	}
	for _, ref := range strings.Split(out, "\n") {
		Git(ctx, repoPath, "update-ref", "-d", ref)
	}
}

func remoteURLs(ctx context.Context, repoPath string) map[string]string {
	out, err := Git(ctx, repoPath, "config", "--get-regexp", `^remote\..*\.url$`)
	if err != nil || out == "" {
		return nil
	}
	urls := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		key, url, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		urls[strings.TrimSuffix(strings.TrimPrefix(key, "remote."), ".url")] = url
	}
	return urls
}

// newArchiveID names a new archive of name: unique, sortable by time, and
// usable as a file name.
func newArchiveID(name string) string {
	return safeName(name) + "-" + time.Now().UTC().Format("20060102T150405.000000000Z")
}

// safeName turns a repo key or member name into a single path component.
func safeName(name string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			return r
		}
		return '_'
	}, name)
}
