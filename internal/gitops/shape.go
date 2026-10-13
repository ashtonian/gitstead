package gitops

import (
	"context"
	"fmt"
	"strings"
)

// Shape is how much of a repository a new checkout holds beyond its history:
// which directories, whether its submodules are checked out, and whether
// Git LFS content is downloaded.
type Shape struct {
	// Sparse lists the only directories checked out, in cone mode: the files
	// at the top level, and directly in each listed directory's parents,
	// come too. Empty checks out everything.
	Sparse []string
	// Submodules checks out the submodules within the checkout -- in its
	// sparse cone, when it has one -- recursively.
	Submodules bool
	// SkipLFS leaves Git LFS files as pointers: their content is never
	// downloaded, in this checkout or later ones.
	SkipLFS bool
}

// LFSFetchExclude is the git config key that, set to "*", keeps Git LFS from
// downloading any content.
const LFSFetchExclude = "lfs.fetchexclude"

// SubmoduleError reports a checkout made in full whose submodules couldn't
// all be checked out. The checkout stays where it is.
type SubmoduleError struct {
	Path string
	Err  error
}

func (e *SubmoduleError) Error() string {
	return fmt.Sprintf("%s is checked out, but not all of its submodules: %v; once they can be reached, run `git submodule update --init --recursive` there", e.Path, e.Err)
}

func (e *SubmoduleError) Unwrap() error { return e.Err }

// env is the environment git makes a checkout of this shape in. With SkipLFS,
// Git LFS's smudge filter leaves pointers even where the repo's config
// doesn't say so yet: in submodules, and in a worktree of a repo cloned
// before.
func (s Shape) env() []string {
	if s.SkipLFS {
		return []string{"GIT_LFS_SKIP_SMUDGE=1"}
	}
	return nil
}

// cloneConfig is the `git clone -c` argument that writes SkipLFS into the new
// repo's config before anything is checked out, so it holds for every later
// checkout too.
func (s Shape) cloneConfig() []string {
	if s.SkipLFS {
		return []string{"-c", LFSFetchExclude + "=*"}
	}
	return nil
}

// noCheckout reports whether a checkout of this shape must be made empty and
// filled only once its sparse patterns are set, so nothing outside them is
// ever checked out -- or, in a partial clone, downloaded.
func (s Shape) noCheckout() bool { return len(s.Sparse) > 0 }

// setSparse writes the sparse patterns of the checkout at path, if it has
// any; whatever checks it out next honours them.
func (s Shape) setSparse(ctx context.Context, path string, env []string) error {
	if len(s.Sparse) == 0 {
		return nil
	}
	args := append([]string{"sparse-checkout", "set", "--cone", "--"}, s.Sparse...)
	if _, err := gitWithEnv(ctx, path, env, args...); err != nil {
		return fmt.Errorf("setting the sparse checkout: %w", err)
	}
	return nil
}

// fill checks out the empty checkout at path, made with noCheckout, once its
// sparse patterns are set. An empty repository has nothing to check out.
func (s Shape) fill(ctx context.Context, path string, env []string) error {
	if !s.noCheckout() || GitExitCode(ctx, path, "rev-parse", "--verify", "-q", "HEAD") != 0 {
		return nil
	}
	_, err := gitWithEnv(ctx, path, env, "checkout", "--quiet")
	return err
}

// checkoutSubmodules checks out, recursively, the submodules in the checkout
// at path that its sparse cone holds, cloning them with filter and depth. A
// failure returns a *SubmoduleError.
func (s Shape) checkoutSubmodules(ctx context.Context, path string, env []string, filter string, depth int) error {
	if !s.Submodules {
		return nil
	}
	paths, err := checkedOutSubmodules(ctx, path)
	if err != nil {
		return &SubmoduleError{Path: path, Err: err}
	}
	if len(paths) == 0 {
		return nil
	}
	// git submodule update ignores the sparse cone, so it is given the paths
	// within it.
	args := []string{"submodule", "update", "--init", "--recursive"}
	if depth > 0 {
		args = append(args, fmt.Sprintf("--depth=%d", depth))
	}
	if filter != "" {
		args = append(args, "--filter="+filter)
	}
	args = append(append(args, "--"), paths...)
	if _, err := gitWithEnv(ctx, path, env, args...); err != nil {
		return &SubmoduleError{Path: path, Err: err}
	}
	if s.SkipLFS {
		// A submodule's config is its own; it says so too, for its later checkouts.
		_, err := gitWithEnv(ctx, path, env, "submodule", "foreach", "--quiet", "--recursive", "git config "+LFSFetchExclude+" '*'")
		if err != nil {
			return &SubmoduleError{Path: path, Err: err}
		}
	}
	return nil
}

// checkedOutSubmodules lists the submodules the checkout at path holds: the
// gitlinks in its index outside no sparse cone.
func checkedOutSubmodules(ctx context.Context, path string) ([]string, error) {
	out, err := GitOutput(ctx, path, "ls-files", "-z", "-t", "--stage")
	if err != nil {
		return nil, fmt.Errorf("listing submodules: %w", err)
	}
	var paths []string
	for _, entry := range strings.Split(string(out), "\x00") {
		// "<tag> <mode> <object> <stage>\t<path>"; tag S is outside the cone.
		meta, file, ok := strings.Cut(entry, "\t")
		fields := strings.Fields(meta)
		if !ok || len(fields) != 4 || fields[1] != "160000" || fields[0] == "S" {
			continue
		}
		paths = append(paths, file)
	}
	return paths, nil
}
