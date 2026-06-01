package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ashtonian/git-mgr/internal/config"
	"github.com/ashtonian/git-mgr/internal/gitops"
	"github.com/ashtonian/git-mgr/internal/state"
)

// backupConfigFor builds a gitops.BackupConfig from resolved per-repo config,
// layering in push-branch (with its resolved SSH key) when enabled by config or
// the CLI flag. pushRemoteFlag overrides the configured remote when non-empty.
func backupConfigFor(cfg, repoCfg *config.Config, key, discoveredBy string, pushBranchFlag bool, pushRemoteFlag string) gitops.BackupConfig {
	opts := []gitops.BackupOption{
		gitops.WithStrategy(repoCfg.Backup.Strategy),
		gitops.WithIncludeUntracked(repoCfg.Backup.IncludeUntracked),
		gitops.WithMaxPerRepo(repoCfg.Backup.MaxPerRepo),
		gitops.WithPatchDir(repoCfg.Backup.PatchDir),
	}
	if pushBranchFlag || repoCfg.Backup.PushBranch {
		remote := pushRemoteFlag
		if remote == "" {
			remote = repoCfg.Backup.PushRemote
		}
		opts = append(opts, gitops.WithPushBranch(remote, cfg.ResolveSSHKey(key, discoveredBy)))
	}
	return gitops.NewBackupConfig(opts...)
}

func (c *BackupCmd) Run(ctx *Context) error {
	cfg, err := ctx.LoadConfig()
	if err != nil {
		return err
	}

	// --list and --restore are read-only; load without the lock so they don't
	// fail while a sync is running.
	if c.List || c.Restore != "" {
		st, err := ctx.LoadState()
		if err != nil {
			return err
		}
		if c.List {
			return c.runList(ctx, st)
		}
		return c.runRestore(ctx, cfg, st)
	}

	// The backup path writes per-repo backup metadata, so take the lock.
	st, unlock, err := lockedState(ctx)
	if err != nil {
		return err
	}
	defer unlock()

	pass, err := selection{c.Repos, c.Tag, c.Set}.predicate(cfg)
	if err != nil {
		return err
	}

	// Run backup on matching repos
	backed := 0
	for key, repo := range st.Repos {
		if !pass(key, repo) || !repo.Cloned {
			continue
		}

		repoCfg := cfg.ResolveForRepo(key)
		backupCfg := backupConfigFor(cfg, repoCfg, key, repo.DiscoveredBy, c.PushBranch, c.PushRemote)

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
				if br.PushedBranch != "" {
					fmt.Printf("  %s: backed up (pushed %s)\n", key, br.PushedBranch)
				} else {
					fmt.Printf("  %s: backed up\n", key)
				}
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
	cfg, err := ctx.LoadConfig()
	if err != nil {
		return err
	}
	pass, err := selection{c.Repos, c.Tag, c.Set}.predicate(cfg)
	if err != nil {
		return err
	}
	for key, repo := range st.Repos {
		if !pass(key, repo) || !repo.Cloned {
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

func (c *BackupCmd) runRestore(ctx *Context, cfg *config.Config, st *state.State) error {
	pass, err := selection{c.Repos, c.Tag, c.Set}.predicate(cfg)
	if err != nil {
		return err
	}
	id := c.Restore
	printed := 0
	for key, repo := range st.Repos {
		if !pass(key, repo) || !repo.Cloned {
			continue
		}

		// A backup may be a stash ref, patch files, or both -- print whichever
		// exist for this id, so the instructions match what was actually saved.
		ref := "refs/git-mgr/backups/" + id
		hasRef := gitops.GitExitCode(ctx.GoCtx(), repo.LocalPath, "rev-parse", "--verify", "--quiet", ref) == 0

		repoCfg := cfg.ResolveForRepo(key)
		patchDir := filepath.Join(repoCfg.Backup.PatchDir, filepath.Base(filepath.Dir(repo.LocalPath)), filepath.Base(repo.LocalPath), id)
		staged := filepath.Join(patchDir, "staged.patch")
		unstaged := filepath.Join(patchDir, "unstaged.patch")
		untracked := filepath.Join(patchDir, "untracked.tar.gz")

		if !hasRef && !fileExists(staged) && !fileExists(unstaged) && !fileExists(untracked) {
			continue // no backup with this id for this repo
		}
		printed++

		fmt.Printf("To restore backup %s for %s (apply onto a clean tree):\n", id, key)
		fmt.Printf("  cd %s\n", repo.LocalPath)
		if hasRef {
			fmt.Printf("  git stash apply %s\n", ref)
		}
		if fileExists(staged) {
			fmt.Printf("  git apply --cached %s\n", staged)
		}
		if fileExists(unstaged) {
			fmt.Printf("  git apply %s\n", unstaged)
		}
		if fileExists(untracked) {
			fmt.Printf("  tar -xzf %s   # restores untracked files (may overwrite)\n", untracked)
		}
		fmt.Println()
	}
	if printed == 0 {
		fmt.Printf("No backup %q found for the selected repos.\n", id)
	}
	return nil
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
