package gitops

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// MaintainOptions shapes one repo's upkeep.
type MaintainOptions struct {
	// TempPackAge is how old a temporary pack must be to count as left
	// behind by an interrupted fetch or repack. A running one writes its
	// pack as it goes, so its file stays younger.
	TempPackAge time.Duration
	// DryRun only lists the temporary packs upkeep would remove.
	DryRun bool
	// Now is the clock temporary packs are aged by; nil for time.Now.
	Now func() time.Time
}

// MaintainResult is what upkeep did to one repo.
type MaintainResult struct {
	// Removed are the temporary packs removed, or that a dry run would
	// remove; Bytes their size.
	Removed []string
	Bytes   int64
	// Maintained reports that git's maintenance ran.
	Maintained bool
}

// Maintain removes the temporary packs an interrupted fetch or repack left
// in the repo at repoPath -- git keeps a fetch's until gc prunes, which a
// repo whose objects a copy borrows never does, and a dead repack's for
// good -- and then runs git's own
// maintenance, which repacks and writes the commit-graph when git judges
// it due. A repo git is busy in (ErrBusy) or another gitstead command is
// deleting, archiving or moving (ErrRepoInUse) is left alone.
func Maintain(ctx context.Context, repoPath string, opts MaintainOptions) (MaintainResult, error) {
	var res MaintainResult
	if !HasGitDir(repoPath) {
		return res, fmt.Errorf("%s: %w", repoPath, ErrRepoGone)
	}
	if !opts.DryRun {
		release, err := LockRepo(ctx, repoPath, false, 0)
		if err != nil {
			return res, err
		}
		defer release()
	}
	if err := CheckIdle(ctx, repoPath, false); err != nil {
		return res, err
	}
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	packDir := filepath.Join(ResolveGitDirs(ctx, repoPath).CommonDir, "objects", "pack")
	stale, err := tempPacks(packDir, now().Add(-opts.TempPackAge))
	if err != nil {
		return res, err
	}
	var errs []error
	for _, f := range stale {
		if !opts.DryRun {
			if err := os.Remove(f.path); err != nil && !errors.Is(err, os.ErrNotExist) {
				errs = append(errs, fmt.Errorf("removing temporary pack %s: %w", f.path, err))
				continue
			}
		}
		res.Removed = append(res.Removed, f.path)
		res.Bytes += f.size
	}
	if opts.DryRun || len(errs) > 0 {
		return res, errors.Join(errs...)
	}
	// Detaching would leave gc running past the deadline the caller set.
	if _, err := Git(ctx, repoPath, "-c", "maintenance.autoDetach=false", "-c", "gc.autoDetach=false", "maintenance", "run", "--auto", "--quiet"); err != nil {
		return res, fmt.Errorf("git maintenance: %w", err)
	}
	res.Maintained = true
	return res, nil
}

type tempPack struct {
	path string
	size int64
}

// tempPacks lists the temporary files in packDir, a repo's objects/pack,
// last written before cutoff: tmp_pack_*, tmp_idx_* and the like from
// fetches, .tmp-<pid>-pack-* from repacks.
func tempPacks(packDir string, cutoff time.Time) ([]tempPack, error) {
	entries, err := os.ReadDir(packDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("listing %s: %w", packDir, err)
	}
	var out []tempPack
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "tmp_") && !strings.HasPrefix(name, ".tmp-") {
			continue
		}
		if !e.Type().IsRegular() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue // gone since the listing
		}
		if info.ModTime().Before(cutoff) {
			out = append(out, tempPack{filepath.Join(packDir, name), info.Size()})
		}
	}
	return out, nil
}
