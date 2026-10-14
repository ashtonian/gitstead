package gitops

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// SourceRemote is a workspace clone's remote for the repo's own clone, its
// source, whose branches it fetches as gitstead/<branch>.
const SourceRemote = "gitstead"

// sourceRefspec maps a source's view of origin onto the copy's, so upstream
// objects cross the network once, into the source. It is given on the fetch
// command line, never as a remote's refspec: two remotes writing origin's
// refs would make git refuse to track origin's branches.
const sourceRefspec = "+refs/remotes/origin/*:refs/remotes/origin/*"

// ErrPartialSource reports a source made with a partial clone filter: a copy
// can't fetch through it the objects it lacks.
var ErrPartialSource = errors.New("the repo's clone is partial (clone.filter); only worktree copies can be made from it")

// CloneFromSource makes a clone at dest of the repo whose clone is at source,
// on branch, with origin set to upstream. Its objects come from source:
// hardlinked (copied across filesystems), or with shared borrowed through
// git's alternates, in which case git gc in source is stopped from pruning
// objects the copy may need. branch is checked out tracking origin's when
// origin has it, else made at base (see ResolveBase). The clone is staged,
// as Clone's are.
func CloneFromSource(ctx context.Context, source, dest, upstream, branch, base string, shared bool, opts CloneOptions) error {
	if IsPartial(ctx, source) {
		return ErrPartialSource
	}
	if shared {
		if err := SetLocalConfig(ctx, source, "gc.pruneExpire", "never"); err != nil {
			return err
		}
	}
	env := opts.env()
	err := staged(dest, func(staging string) error {
		mode := "--local"
		if shared {
			mode = "--shared"
		}
		args := append([]string{"clone", "--quiet", mode, "--no-checkout", "--origin", SourceRemote}, opts.cloneConfig()...)
		if err := runClone(ctx, nil, source, append(args, source, staging)...); err != nil {
			return err
		}
		if err := shareLFSObjects(ctx, source, staging, opts.Shape); err != nil {
			return err
		}
		if err := opts.setSparse(ctx, staging, env); err != nil {
			return err
		}
		cloned, err := Git(ctx, staging, "for-each-ref", "--format=%(refname)", "refs/heads")
		if err != nil {
			return err
		}
		steps := [][]string{
			{"remote", "add", "origin", upstream},
			{"fetch", "--quiet", "--no-tags", source, sourceRefspec},
		}
		for _, args := range steps {
			if _, err := Git(ctx, staging, args...); err != nil {
				return err
			}
		}
		if head, err := Git(ctx, source, "symbolic-ref", "-q", "refs/remotes/origin/HEAD"); err == nil && head != "" {
			if _, err := Git(ctx, staging, "symbolic-ref", "refs/remotes/origin/HEAD", head); err != nil {
				return err
			}
		}
		if err := checkoutBranch(ctx, staging, source, branch, base, env); err != nil {
			return err
		}
		// Drop the local branch the clone made of source's checkout: the copy
		// works on its own branch, against origin.
		var drop []string
		for _, ref := range strings.Fields(cloned) {
			if ref != "refs/heads/"+branch {
				drop = append(drop, "delete "+ref)
			}
		}
		if len(drop) > 0 {
			if _, err := gitWithStdin(ctx, staging, nil, []byte(strings.Join(drop, "\n")+"\n"), "update-ref", "--no-deref", "--stdin"); err != nil {
				return err
			}
		}
		_, err = CopyID(ctx, staging)
		return err
	})
	if err != nil {
		return err
	}
	return opts.checkoutSubmodules(ctx, dest, env, opts.Filter, opts.Depth)
}

// shareLFSObjects gives a clone made from source the Git LFS content source
// already has, hardlinked, so checking it out downloads none of it again.
func shareLFSObjects(ctx context.Context, source, clone string, s Shape) error {
	if s.SkipLFS {
		return nil
	}
	objects := filepath.Join(ResolveGitDirs(ctx, source).CommonDir, "lfs", "objects")
	if !hasFiles(objects) {
		return nil
	}
	if err := linkTree(objects, filepath.Join(clone, ".git", "lfs", "objects")); err != nil {
		return fmt.Errorf("sharing LFS objects: %w", err)
	}
	return nil
}

// checkoutBranch checks out branch in the copy at path: tracking origin's
// branch when there is one, else made at base, resolved in path and then in
// source.
func checkoutBranch(ctx context.Context, path, source, branch, base string, env []string) error {
	if remoteBranchExists(ctx, path, branch) {
		_, err := gitWithEnv(ctx, path, env, "checkout", "--quiet", "-b", branch, "--track", "origin/"+branch)
		return err
	}
	start, err := ResolveBase(ctx, path, base)
	if err != nil {
		if start, err = ResolveBase(ctx, source, base); err != nil {
			return err
		}
	}
	_, err = gitWithEnv(ctx, path, env, "checkout", "--quiet", "--no-track", "-b", branch, start)
	return err
}

// AddWorktree adds a linked worktree of the repo at source at dest, on
// branch: the existing local branch, origin's branch when only origin has it,
// else a new branch at base (see ResolveBase). It checks out what opts.Shape
// asks for; opts' key, filter and depth clone its submodules. A worktree
// whose submodules can't all be checked out stays, and a *SubmoduleError
// says so.
func AddWorktree(ctx context.Context, source, dest, branch, base string, opts CloneOptions) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	args := []string{"worktree", "add", "--quiet"}
	if opts.noCheckout() {
		args = append(args, "--no-checkout")
	}
	switch {
	case BranchExists(ctx, source, branch):
		args = append(args, dest, branch)
	case remoteBranchExists(ctx, source, branch):
		args = append(args, "--track", "-b", branch, dest, "origin/"+branch)
	default:
		start, err := ResolveBase(ctx, source, base)
		if err != nil {
			return err
		}
		args = append(args, "--no-track", "-b", branch, dest, start)
	}
	env := opts.env()
	if _, err := gitWithEnv(ctx, source, env, args...); err != nil {
		return err
	}
	err := opts.setSparse(ctx, dest, env)
	if err == nil {
		err = opts.fill(ctx, dest, env)
	}
	if err != nil {
		// Left empty, it would later pass for a finished copy; it holds no
		// work yet, so it goes.
		_ = WorktreeRemove(ctx, source, dest, true)
		return err
	}
	return opts.checkoutSubmodules(ctx, dest, env, opts.Filter, opts.Depth)
}

// ResolveBase returns the commit new branches in repo start from: base, or
// origin's branch of that name; with base empty, origin's default branch, or
// HEAD when origin has none.
func ResolveBase(ctx context.Context, repo, base string) (string, error) {
	var candidates []string
	switch base {
	case "":
		candidates = []string{"refs/remotes/origin/HEAD", "HEAD"}
	default:
		candidates = []string{base, "refs/remotes/origin/" + base}
	}
	for _, ref := range candidates {
		if oid, err := Git(ctx, repo, "rev-parse", "--verify", "--quiet", ref+"^{commit}"); err == nil && oid != "" {
			return oid, nil
		}
	}
	if base == "" {
		return "", fmt.Errorf("%s has no commits to start a branch from", repo)
	}
	return "", fmt.Errorf("base ref %q is neither a ref nor a branch of origin in %s", base, repo)
}

// DefaultBase names the branch a repo's work is compared with when no base
// ref is configured: origin's default branch, else the upstream of source's
// checked-out branch -- for a workspace copy, source is the repo it was made
// from. "" when neither is known.
func DefaultBase(ctx context.Context, source string) string {
	if GitExitCode(ctx, source, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/HEAD") == 0 {
		return "origin/HEAD"
	}
	upstream, err := Git(ctx, source, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "HEAD@{upstream}")
	if err != nil {
		return ""
	}
	return upstream
}

func remoteBranchExists(ctx context.Context, repo, branch string) bool {
	return GitExitCode(ctx, repo, "show-ref", "--verify", "--quiet", "refs/remotes/origin/"+branch) == 0
}

// RefreshFromSource updates the copy's view of origin from its source
// without the network: whatever the source last fetched.
func RefreshFromSource(ctx context.Context, copyPath, source string) error {
	_, err := Git(ctx, copyPath, "fetch", "--quiet", "--prune", "--no-tags", source, sourceRefspec)
	return err
}

// IsPartial reports whether the repo at path was made with a partial clone
// filter: it has a promisor remote, which lends it the objects it lacks.
func IsPartial(ctx context.Context, path string) bool {
	if v, err := Git(ctx, path, "config", "--get", "extensions.partialclone"); err == nil && v != "" {
		return true
	}
	out, err := Git(ctx, path, "config", "--type=bool", "--get-regexp", `^remote\..*\.promisor$`)
	return err == nil && strings.Contains(" "+strings.ReplaceAll(out, "\n", " ")+" ", " true ")
}

// RepointSource points the copy at copyPath at its source's new path to: its
// gitstead remote, and the alternates it borrows objects through.
func RepointSource(ctx context.Context, copyPath, from, to string) error {
	if url, err := Git(ctx, copyPath, "config", "--get", "remote."+SourceRemote+".url"); err == nil && url == from {
		if _, err := Git(ctx, copyPath, "remote", "set-url", SourceRemote, to); err != nil {
			return err
		}
	}
	alternates := filepath.Join(ResolveGitDirs(ctx, copyPath).CommonDir, "objects", "info", "alternates")
	data, err := os.ReadFile(alternates)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	oldObjects, newObjects := filepath.Join(from, ".git", "objects"), filepath.Join(to, ".git", "objects")
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	changed := false
	for i, line := range lines {
		if filepath.Clean(line) == oldObjects {
			lines[i], changed = newObjects, true
		}
	}
	if !changed {
		return nil
	}
	tmp := alternates + ".tmp"
	if err := os.WriteFile(tmp, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, alternates)
}

// Source returns the path of the repo the copy at path was made from: the
// main worktree of a linked worktree, the gitstead remote of a workspace
// clone; "" when it is neither.
func Source(ctx context.Context, path string) string {
	dirs := ResolveGitDirs(ctx, path)
	if dirs.CommonDir != dirs.GitDir {
		return filepath.Dir(dirs.CommonDir)
	}
	url, err := Git(ctx, path, "config", "--get", "remote."+SourceRemote+".url")
	if err != nil {
		return ""
	}
	return url
}
