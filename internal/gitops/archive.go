package gitops

import (
	"bufio"
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
	"github.com/ashtonian/git-mgr/internal/repourl"
)

// archiveRefPrefix holds the temporary refs a bundle is built from. Each
// archive gets its own sub-namespace because linked worktrees share refs.
const archiveRefPrefix = "refs/git-mgr/archive/"

// ArchiveMeta describes an archive bundle and is written beside it as JSON.
type ArchiveMeta struct {
	Source  string            `json:"source"`
	Remotes map[string]string `json:"remotes,omitempty"`
	Branch  string            `json:"branch,omitempty"`
	// Head is the commit HEAD was at; "" on an unborn branch.
	Head    string    `json:"head,omitempty"`
	Work    string    `json:"work"`
	Created time.Time `json:"created"`
	// Bundle is the repository's own bundle; "" when only its submodules
	// had work to save.
	Bundle string `json:"bundle,omitempty"`
	// Complete marks a bundle holding all of the repository's history, so it
	// restores with nothing else. Otherwise the bundle holds only what Base
	// lacks, and restores into a clone of Base or of the repository's remote.
	Complete bool   `json:"complete,omitempty"`
	Base     string `json:"base,omitempty"`
	Restore  string `json:"restore,omitempty"`
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

// archiveOptions says what archive saves and what its bundle may lean on.
type archiveOptions struct {
	scope archiveScope
	// base keeps the history a clone shares with its remotes; see
	// RemoveOptions.Base. A worktree's base is its own repository.
	base string
	// keepHistory archives a clone even when it has no unique work.
	keepHistory bool
}

// archive saves the unique work of the repository at repoPath into one
// verified git bundle in dir and returns its path, or "" when there is nothing
// to save. The bundle holds the repository's refs -- branches, tags and other
// refs, every stash entry, HEAD -- and a snapshot of the index, working tree
// and untracked files that `git stash apply --index` restores. A JSON file
// beside it records the remotes and how to restore it. Each submodule with
// work of its own gets its own archive, and a clone's Git LFS objects are kept
// beside the bundle. When only submodules had work, the JSON file's path is
// returned.
func archive(ctx context.Context, repoPath, dir, name string, opts archiveOptions) (string, error) {
	work, err := InspectUniqueWork(ctx, repoPath)
	if err != nil {
		return "", err
	}
	lost := work.LostWithClone()
	if opts.scope == scopeWorktree {
		lost = work.LostWithWorktree()
	}
	if !lost && !opts.keepHistory {
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
	meta.Head, _ = Git(ctx, repoPath, "rev-parse", "--verify", "-q", "HEAD^{commit}")
	if !lost {
		meta.Work = "its whole history, which other archives lean on"
	}

	for _, sub := range slices.Sorted(maps.Keys(work.Submodules)) {
		subOpts := archiveOptions{scope: scopeClone}
		if opts.base != "" {
			subOpts.base = filepath.Join(opts.base, sub)
		}
		path, err := archive(ctx, filepath.Join(repoPath, sub), dir, name+"-"+pathutil.FileName(sub), subOpts)
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
	ownLost := own.LostWithClone() || opts.keepHistory
	if opts.scope == scopeWorktree {
		ownLost = own.LostWithWorktree()
	}
	if opts.scope == scopeClone {
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
	if meta.Head != "" {
		if err := setRef("head", meta.Head); err != nil {
			return "", err
		}
	}
	if opts.scope == scopeClone {
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

	switch {
	case opts.scope == scopeWorktree:
		// Branch commits survive in the shared repository.
		err = writeArchiveBundle(ctx, repoPath, opts.base, bundle, "--glob="+refs+"*", "--not", "--remotes", "--branches")
	case opts.base != "":
		err = writeArchiveBundle(ctx, repoPath, opts.base, bundle, thinBundleRevs...)
		if err != nil && ctx.Err() == nil {
			// The base lacks some of what the bundle would lean on: keep
			// all of it instead.
			err = writeArchiveBundle(ctx, repoPath, "", bundle, completeBundleRevs...)
			meta.Complete = true
		} else {
			meta.Base = opts.base
		}
	default:
		err = writeArchiveBundle(ctx, repoPath, "", bundle, completeBundleRevs...)
		meta.Complete = true
	}
	if err != nil {
		return "", err
	}

	meta.Bundle = bundle
	meta.Restore = restoreCommand(meta, bundle, strings.TrimPrefix(refs, "refs/"))
	if err := writeMeta(metaPath, meta); err != nil {
		os.Remove(bundle)
		return "", err
	}
	return bundle, nil
}

// The refs a clone's bundle holds: all of the repository's own, and with
// completeBundleRevs its remote-tracking refs too. refs/prefetch holds copies
// of remote branches.
var (
	thinBundleRevs     = []string{"--exclude=refs/remotes/*", "--exclude=refs/prefetch/*", "--all", "--not", "--remotes"}
	completeBundleRevs = []string{"--exclude=refs/prefetch/*", "--all"}
)

// writeArchiveBundle writes the bundle of revs from repoPath to path and verifies
// it: against base, which must hold everything it leans on, or, with no base,
// that it leans on nothing at all. A bundle that fails is deleted.
func writeArchiveBundle(ctx context.Context, repoPath, base, path string, revs ...string) error {
	if _, err := Git(ctx, repoPath, append([]string{"bundle", "create", "-q", path}, revs...)...); err != nil {
		os.Remove(path)
		return fmt.Errorf("writing bundle: %w", err)
	}
	verify := func() error {
		if base != "" {
			_, err := Git(ctx, base, "bundle", "verify", "-q", path)
			return err
		}
		prereqs, err := bundlePrerequisites(path)
		if err == nil && len(prereqs) > 0 {
			err = fmt.Errorf("it needs %d commits it doesn't hold, such as %s", len(prereqs), prereqs[0])
		}
		return err
	}
	if err := verify(); err != nil {
		os.Remove(path)
		return fmt.Errorf("verifying bundle: %w", err)
	}
	return nil
}

// bundlePrerequisites lists the commits the bundle at path needs a
// repository to hold before it can be fetched: the header lines that start
// with "-".
func bundlePrerequisites(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := bufio.NewReader(f)
	signature, err := r.ReadString('\n')
	if err != nil || !strings.HasPrefix(signature, "# v") || !strings.HasSuffix(signature, " git bundle\n") {
		return nil, fmt.Errorf("%s is not a git bundle", path)
	}
	var prereqs []string
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", path, err)
		}
		switch {
		case line == "\n":
			return prereqs, nil
		case strings.HasPrefix(line, "-"):
			oid, _, _ := strings.Cut(strings.TrimSpace(line[1:]), " ")
			prereqs = append(prereqs, oid)
		}
	}
}

// restoreCommand is how to restore a bundle archive by hand. refs is where
// its own refs are, below refs/.
func restoreCommand(meta ArchiveMeta, bundle, refs string) string {
	start := "git clone <remote> restored"
	switch {
	case meta.Complete:
		start = "git init restored"
	case meta.Base != "":
		start = "git clone " + meta.Base + " restored"
	}
	return fmt.Sprintf("%s && cd restored && git fetch %s '+refs/*:refs/restored/*'"+
		" && git stash apply --index refs/restored/%sworktree", start, bundle, refs)
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
// are deleted, whatever its description says, and a directory only when its
// description names it as the archived clone: a path recorded wrongly can't
// take someone's working copy with it.
func DeleteArchive(path string) error {
	base := archiveBase(path)
	meta, _, err := ReadArchive(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if path == base && meta.Clone != path {
		if _, serr := os.Lstat(path); serr == nil {
			return fmt.Errorf("%s isn't described as an archived clone (no %s.json naming it), so it is left; delete it by hand if it is one", path, base)
		}
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

// RemoveOptions says how RemoveClone saves a clone's work before deleting it.
type RemoveOptions struct {
	// Discard deletes the clone without saving anything.
	Discard bool
	// Base is a repository that keeps the history the clone shares with its
	// remotes: the repo a workspace copy was made from. The bundle then holds
	// only what the base lacks. Without one it holds the clone's whole
	// history, so it restores with nothing else: once the clone is gone, its
	// upstream may be the only other copy, and may be gone too.
	Base string
	// KeepHistory archives the clone's whole history even when it has no
	// unique work, for archives that lean on it.
	KeepHistory bool
}

// RemoveClone deletes the clone at repoPath. Unless opts.Discard is set it
// first archives the clone's unique work into archiveDir; if that work cannot
// be inspected or archived the clone is left in place and an error is
// returned. A clone with linked worktrees on disk, or that git is busy in, is
// always left in place, discard or not. A clone already gone from disk counts
// as removed. It returns the archive path, or "" when there was nothing to
// save.
func RemoveClone(ctx context.Context, repoPath, archiveDir, name string, opts RemoveOptions) (string, error) {
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
	if err := CheckIdle(ctx, repoPath, true); err != nil {
		return "", fmt.Errorf("keeping %s: %w", repoPath, err)
	}
	var archived string
	if !opts.Discard {
		var err error
		aopts := archiveOptions{scope: scopeClone, base: opts.Base, keepHistory: opts.KeepHistory}
		if archived, err = archive(ctx, repoPath, archiveDir, name, aopts); err != nil {
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
	info, err := os.Lstat(repoPath)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "", nil
	case err != nil:
		return "", fmt.Errorf("keeping %s: %w", repoPath, err)
	case info.Mode()&fs.ModeSymlink != 0:
		// Moving the link would leave the clone where it is.
		target, _ := os.Readlink(repoPath)
		return "", fmt.Errorf("keeping %s: it is a symlink to %s; archive that instead", repoPath, target)
	}
	if linked, err := LinkedWorktrees(ctx, repoPath); err != nil {
		return "", fmt.Errorf("keeping %s: listing its worktrees: %w", repoPath, err)
	} else if len(linked) > 0 {
		return "", fmt.Errorf("keeping %s: %w (%s); remove them first", repoPath, ErrHasWorktrees, strings.Join(linked, ", "))
	}
	// A rebase or merge under way moves with the clone; a running git
	// command could be cut off halfway through a copy.
	if err := CheckIdle(ctx, repoPath, false); err != nil {
		return "", fmt.Errorf("keeping %s: %w", repoPath, err)
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
// or archive failure, or while git is busy in it, the worktree is left in
// place. The worktree's backup refs go with it.
func RemoveWorktree(ctx context.Context, source, path, archiveDir, name string, discard bool) (string, error) {
	if err := CheckIdle(ctx, path, true); err != nil {
		return "", fmt.Errorf("keeping %s: %w", path, err)
	}
	copyID := ReadCopyID(ctx, path)
	var archived string
	if !discard {
		var err error
		if archived, err = archive(ctx, path, archiveDir, name, archiveOptions{scope: scopeWorktree, base: source}); err != nil {
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
		_, _ = Git(ctx, repoPath, "update-ref", "-d", ref) // temporary refs; a leftover is harmless
	}
}

// remoteURLs maps the repository's remotes to their URLs, without any
// credential a URL embeds: they are written where anyone reading git-mgr's
// output or files can see them.
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
		urls[strings.TrimSuffix(strings.TrimPrefix(key, "remote."), ".url")] = repourl.WithoutCredentials(url)
	}
	return urls
}

// newArchiveID names a new archive of name: unique, sortable by time, and
// usable as a file name.
func newArchiveID(name string) string {
	return pathutil.FileName(name) + "-" + time.Now().UTC().Format("20060102T150405.000000000Z")
}
