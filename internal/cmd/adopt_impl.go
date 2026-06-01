package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ashtonian/git-mgr/internal/gitops"
	"github.com/ashtonian/git-mgr/internal/state"
)

func (c *AdoptCmd) Run(ctx *Context) error {
	_, err := ctx.LoadConfig()
	if err != nil {
		return err
	}

	// The dry-run path is read-only; the real path mutates state, so take the
	// lock and re-read under it.
	var st *state.State
	if c.DryRun {
		st, err = ctx.LoadState()
	} else {
		var unlock func()
		st, unlock, err = lockedState(ctx)
		if err == nil {
			defer unlock()
		}
	}
	if err != nil {
		return err
	}

	if c.Scan {
		return c.scanAndAdopt(ctx, st)
	}
	return c.adoptSingle(ctx, st, c.Path)
}

func (c *AdoptCmd) adoptSingle(ctx *Context, st *state.State, repoPath string) error {
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

	key := identity.Host + "/" + identity.FullName

	if _, exists := st.Repos[key]; exists {
		fmt.Printf("Already tracked: %s\n", key)
		return nil
	}

	if c.DryRun {
		fmt.Printf("Would adopt: %s -> %s\n", absPath, key)
		return nil
	}

	var tags []string
	if c.Tag != "" {
		tags = strings.Split(c.Tag, ",")
	}

	now := time.Now()
	st.Repos[key] = &state.RepoState{
		CloneURL:       identity.SSHURL,
		Host:           identity.Host,
		Owner:          identity.Owner,
		Name:           identity.Name,
		FullName:       identity.FullName,
		Provider:       identity.Host,
		LocalPath:      absPath,
		Cloned:         true,
		Tags:           tags,
		Status:         "clean",
		ManuallyAdded:  true,
		FirstSeen:      now,
		LastDiscovered: now,
	}

	if err := st.Save(); err != nil {
		return fmt.Errorf("saving state: %w", err)
	}
	logEvent(ctx, "adopt", key, absPath)

	fmt.Printf("Adopted: %s -> %s\n", key, absPath)
	return nil
}

func (c *AdoptCmd) scanAndAdopt(ctx *Context, st *state.State) error {
	absPath, err := filepath.Abs(c.Path)
	if err != nil {
		return fmt.Errorf("resolving path: %w", err)
	}

	var found []string
	err = filepath.WalkDir(absPath, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip errors
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
	for _, repoPath := range found {
		if err := c.adoptSingle(ctx, st, repoPath); err != nil {
			fmt.Printf("  %s: %v\n", repoPath, err)
		}
	}
	return nil
}
