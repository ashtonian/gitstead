package gitops

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
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
	Restore string            `json:"restore"`
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
// it records the remotes and the restore command.
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
	id := safeName(name) + "-" + time.Now().UTC().Format("20060102T150405.000000000Z")
	bundle := filepath.Join(dir, id+".bundle")
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

	meta := ArchiveMeta{
		Source:  repoPath,
		Remotes: remoteURLs(ctx, repoPath),
		Work:    work.String(),
		Created: time.Now().UTC(),
		Restore: fmt.Sprintf("git clone <remote> restored && cd restored && git fetch %s '+refs/*:refs/restored/*'"+
			" && git stash apply --index refs/restored/%sworktree", bundle, strings.TrimPrefix(refs, "refs/")),
	}
	meta.Branch, _ = Git(ctx, repoPath, "symbolic-ref", "-q", "--short", "HEAD")
	var data bytes.Buffer
	enc := json.NewEncoder(&data)
	enc.SetEscapeHTML(false) // keep the restore command copy-pasteable
	enc.SetIndent("", "  ")
	if err := enc.Encode(meta); err != nil {
		return "", err
	}
	if err := os.WriteFile(strings.TrimSuffix(bundle, ".bundle")+".json", data.Bytes(), 0o600); err != nil {
		return "", fmt.Errorf("writing archive metadata: %w", err)
	}
	return bundle, nil
}

// RemoveClone deletes the clone at repoPath. Unless discard is set it first
// archives the clone's unique work into archiveDir; if that work cannot be
// inspected or archived the clone is left in place and an error is returned.
// A clone already gone from disk counts as removed. It returns the archive
// path, or "" when there was nothing to save.
func RemoveClone(ctx context.Context, repoPath, archiveDir, name string, discard bool) (string, error) {
	if !filepath.IsAbs(repoPath) {
		return "", fmt.Errorf("refusing to remove clone at non-absolute path %q", repoPath)
	}
	if _, err := os.Lstat(repoPath); errors.Is(err, fs.ErrNotExist) {
		return "", nil
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
