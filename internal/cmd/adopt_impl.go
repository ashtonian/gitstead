package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/ashtonian/gitten/internal/config"
	"github.com/ashtonian/gitten/internal/gitops"
	"github.com/ashtonian/gitten/internal/reconcile"
	"github.com/ashtonian/gitten/internal/repourl"
	"github.com/ashtonian/gitten/internal/state"
)

func (c *AdoptCmd) Run(ctx *Context) error {
	cfg, err := ctx.LoadConfig()
	if err != nil {
		return err
	}

	// Adopting locks the state per change, so this reads it unlocked.
	st, err := ctx.LoadState()
	if err != nil {
		return err
	}

	if c.Scan {
		return ctx.finishChanges(c.scanAndAdopt(ctx, cfg, st))
	}
	return ctx.finishChanges(c.adoptSingle(ctx, cfg, st, c.Path))
}

func (c *AdoptCmd) adoptSingle(ctx *Context, cfg *config.Config, st *state.State, repoPath string) error {
	absPath, err := filepath.Abs(repoPath)
	if err != nil {
		return fmt.Errorf("resolving path: %w", err)
	}
	if !gitops.HasGitDir(absPath) {
		return fmt.Errorf("%s is not a git repository", absPath)
	}
	identity, err := gitops.ParseRemoteURL(ctx.GoCtx(), absPath)
	if err != nil {
		return fmt.Errorf("parsing remote URL: %w", err)
	}
	if err := repourl.CheckNoCredentials(identity.URL); err != nil {
		return fmt.Errorf("origin of %s: %w", absPath, err)
	}
	if _, exists := st.Repos[identity.Key()]; exists {
		fmt.Printf("Already tracked: %s\n", identity.Key())
		return nil
	}
	return addByHand(ctx, cfg, st, reconcile.HandAdded(cfg, identity, absPath, splitTags(c.Tag)), c.DryRun)
}

func (c *AdoptCmd) scanAndAdopt(ctx *Context, cfg *config.Config, st *state.State) error {
	absPath, err := filepath.Abs(c.Path)
	if err != nil {
		return fmt.Errorf("resolving path: %w", err)
	}

	var found []string
	err = filepath.WalkDir(absPath, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip errors
		}
		if d.IsDir() && gitops.IsStaging(d.Name()) {
			return filepath.SkipDir // a clone in progress
		}
		if d.IsDir() && path == cfg.Backup.ArchiveDir {
			return filepath.SkipDir // clones archived on purpose
		}
		if d.IsDir() && d.Name() == ".git" {
			found = append(found, filepath.Dir(path))
			return filepath.SkipDir
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("scanning directory: %w", err)
	}

	if len(found) == 0 {
		fmt.Println("No git repos found.")
		return nil
	}

	fmt.Printf("Found %d git repos\n", len(found))
	failed := 0
	for _, repoPath := range found {
		if err := c.adoptSingle(ctx, cfg, st, repoPath); err != nil {
			fmt.Fprintf(os.Stderr, "  %s: %v\n", repoPath, err)
			ctx.noteError(repoPath, err)
			failed++
		}
	}
	return outcome(failed, len(found), "repos could not be adopted")
}
