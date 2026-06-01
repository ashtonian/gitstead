package cmd

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/ashtonian/git-mgr/internal/gitops"
	"github.com/ashtonian/git-mgr/internal/state"
)

func (c *BackupCmd) Run(ctx *Context) error {
	cfg, err := ctx.LoadConfig()
	if err != nil {
		return err
	}
	st, err := ctx.LoadState()
	if err != nil {
		return err
	}

	if c.List {
		return c.runList(ctx, st)
	}
	if c.Restore != "" {
		return c.runRestore(ctx, st)
	}

	// Run backup on matching repos
	backed := 0
	for key, repo := range st.Repos {
		if c.Repos != "" {
			matched, _ := filepath.Match(c.Repos, key)
			if !matched {
				continue
			}
		}
		if c.Tag != "" && !repo.HasTag(c.Tag) {
			continue
		}
		if !repo.Cloned {
			continue
		}

		repoCfg := cfg.ResolveForRepo(key)
		backupCfg := gitops.NewBackupConfig(
			gitops.WithStrategy(repoCfg.Backup.Strategy),
			gitops.WithIncludeUntracked(repoCfg.Backup.IncludeUntracked),
			gitops.WithMaxPerRepo(repoCfg.Backup.MaxPerRepo),
			gitops.WithPatchDir(repoCfg.Backup.PatchDir),
		)

		br, err := gitops.Backup(ctx.GoCtx(), repo.LocalPath, backupCfg)
		if err != nil {
			fmt.Printf("  %s: error: %v\n", key, err)
			continue
		}
		if br.IsDirty {
			backed++
			repo.LastBackup = time.Now()
			repo.LastBackupRef = br.RefName
			repo.LastBackupHasPatch = br.HasPatch
			repo.Status = "dirty"
			if !ctx.Quiet {
				fmt.Printf("  %s: backed up\n", key)
			}
		}
	}

	if err := st.Save(); err != nil {
		return fmt.Errorf("saving state: %w", err)
	}

	if !ctx.Quiet {
		fmt.Printf("Backed up %d dirty repos\n", backed)
	}
	return nil
}

func (c *BackupCmd) runList(ctx *Context, st *state.State) error {
	for key, repo := range st.Repos {
		if c.Repos != "" {
			matched, _ := filepath.Match(c.Repos, key)
			if !matched {
				continue
			}
		}
		if c.Tag != "" && !repo.HasTag(c.Tag) {
			continue
		}
		if !repo.Cloned {
			continue
		}

		refs, err := gitops.ListBackupRefs(ctx.GoCtx(), repo.LocalPath)
		if err != nil || len(refs) == 0 {
			continue
		}

		fmt.Printf("%s:\n", key)
		for _, ref := range refs {
			fmt.Printf("  %s\n", ref)
		}
	}
	return nil
}

func (c *BackupCmd) runRestore(ctx *Context, st *state.State) error {
	for key, repo := range st.Repos {
		if c.Repos != "" {
			matched, _ := filepath.Match(c.Repos, key)
			if !matched {
				continue
			}
		}

		fmt.Printf("To restore backup %s for %s:\n", c.Restore, key)
		fmt.Printf("  cd %s\n", repo.LocalPath)
		fmt.Printf("  git stash apply refs/git-mgr/backups/%s\n", c.Restore)
		fmt.Println()
	}
	return nil
}
