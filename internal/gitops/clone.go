package gitops

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/ashtonian/git-mgr/internal/repourl"
)

// stagingPrefix names the directory a clone is made in, beside its
// destination, until it is complete.
const stagingPrefix = ".git-mgr-clone-"

// IsStaging reports whether a directory name is a clone in progress, which
// scans for repositories skip.
func IsStaging(name string) bool {
	return strings.HasPrefix(name, stagingPrefix)
}

// CloneOptions shape a clone, and the checkouts made from it.
type CloneOptions struct {
	// SSHKey is the key to clone with -- submodules too; "" uses the default
	// SSH configuration.
	SSHKey string
	// Filter is a partial clone filter, such as blob:none; "" clones every
	// object. Submodules are cloned with it too.
	Filter string
	// Depth limits history to that many commits on every branch; 0 is all.
	// Submodules are cloned with it too.
	Depth int
	// Shape is how much of the repository a checkout holds.
	Shape
}

// env is the environment git clones and checks out with.
func (o CloneOptions) env() []string {
	env := o.Shape.env()
	if o.SSHKey != "" {
		env = append(env, gitSSHCommandEnv(o.SSHKey))
	}
	return env
}

// Clone clones url into destPath, creating parent directories, and checks out
// what opts.Shape asks for. See staged for how destPath is protected. A clone
// whose submodules can't all be checked out stays at destPath, and a
// *SubmoduleError says so.
func Clone(ctx context.Context, url, destPath string, opts CloneOptions) error {
	env := opts.env()
	err := staged(destPath, func(staging string) error {
		args := append([]string{"clone", "--quiet"}, opts.cloneConfig()...)
		if opts.Filter != "" {
			args = append(args, "--filter="+opts.Filter)
		}
		if opts.Depth > 0 {
			// --depth alone would also limit the clone to one branch, and
			// agents' copies need the others.
			args = append(args, fmt.Sprintf("--depth=%d", opts.Depth), "--no-single-branch")
		}
		if opts.noCheckout() {
			args = append(args, "--no-checkout")
		}
		if err := runClone(ctx, env, url, append(args, url, staging)...); err != nil {
			return err
		}
		if err := opts.setSparse(ctx, staging, env); err != nil {
			return err
		}
		return opts.fill(ctx, staging, env)
	})
	if err != nil {
		return err
	}
	// Submodules are checked out in place: a clone that has everything but
	// some of them is kept rather than cloned again on every sync.
	return opts.checkoutSubmodules(ctx, destPath, env, opts.Filter, opts.Depth)
}

// staged makes a repository at destPath with make, which builds it in an
// empty staging directory beside destPath; it is renamed into place when
// complete, so destPath never holds a partial repository, a failed or killed
// one leaves nothing there, and whatever already is at destPath -- files, or
// another run's clone -- is never touched.
func staged(destPath string, make func(staging string) error) error {
	parent, base := filepath.Dir(destPath), filepath.Base(destPath)
	if err := checkFree(destPath); err != nil {
		return err
	}
	sweepStaging(parent, base)

	staging, err := mkdirStaging(parent, base)
	if err != nil {
		return fmt.Errorf("creating staging dir: %w", err)
	}
	defer os.RemoveAll(staging)
	// Holding a lock on the staging directory marks it as in use; a run that
	// died mid-clone leaves it unlocked for the next clone to sweep.
	held, err := os.Open(staging)
	if err != nil {
		return err
	}
	defer held.Close()
	if err := syscall.Flock(int(held.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return fmt.Errorf("locking staging dir: %w", err)
	}

	if err := make(staging); err != nil {
		return err
	}
	// os.Remove takes only an empty directory, and rename(2) won't replace a
	// non-empty one, so anything that appeared at destPath meanwhile -- say,
	// another run's clone -- is kept.
	if err := os.Remove(destPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%s appeared while cloning; leaving it as it is", destPath)
	}
	if err := os.Rename(staging, destPath); err != nil {
		return fmt.Errorf("moving the clone into %s: %w", destPath, err)
	}
	return nil
}

// runClone runs a git clone of url with args, outside any repository.
func runClone(ctx context.Context, env []string, url string, args ...string) error {
	cmd := newGit(ctx, env, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git clone %s: %w\n%s", repourl.Redact(url), err, repourl.Redact(strings.TrimSpace(stderr.String())))
	}
	return nil
}

// mkdirStaging creates an empty staging directory for base in parent,
// creating parent too. Unlike os.MkdirTemp it honours the umask, so the
// finished clone gets the permissions git itself would have given it.
func mkdirStaging(parent, base string) (string, error) {
	// A move tidying the directories it emptied can remove parent before the
	// staging directory is in it, so a vanished parent is made again.
	for retries := 3; ; {
		if err := os.MkdirAll(parent, 0o755); err != nil {
			return "", err
		}
		dir := filepath.Join(parent, fmt.Sprintf("%s%s-%08x", stagingPrefix, base, rand.Uint32()))
		err := os.Mkdir(dir, 0o777)
		switch {
		case err == nil:
			return dir, nil
		case errors.Is(err, fs.ErrExist):
		case errors.Is(err, fs.ErrNotExist) && retries > 0:
			retries--
		default:
			return "", err
		}
	}
}

// checkFree fails unless destPath is missing or an empty directory.
func checkFree(destPath string) error {
	info, err := os.Lstat(destPath)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.IsDir() {
		if entries, err := os.ReadDir(destPath); err == nil && len(entries) == 0 {
			return nil
		}
	}
	return fmt.Errorf("%s already exists and is not an empty directory", destPath)
}

// sweepStaging removes staging directories for base that no running clone
// holds: what a killed run left behind.
func sweepStaging(parent, base string) {
	dirs, _ := filepath.Glob(filepath.Join(parent, stagingPrefix+base+"-*"))
	for _, dir := range dirs {
		f, err := os.Open(dir)
		if err != nil {
			continue
		}
		if syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) == nil {
			_ = os.RemoveAll(dir)
		}
		f.Close()
	}
}
