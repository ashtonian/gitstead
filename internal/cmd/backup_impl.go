package cmd

import (
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"

	"github.com/ashtonian/git-mgr/internal/config"
	"github.com/ashtonian/git-mgr/internal/gitops"
	"github.com/ashtonian/git-mgr/internal/state"
)

// backupConfigFor is the backup configuration a repo resolves to.
func backupConfigFor(repoCfg *config.Config) gitops.BackupConfig {
	b := repoCfg.Backup
	return gitops.NewBackupConfig(
		gitops.WithStrategy(b.Strategy),
		gitops.WithIncludeUntracked(b.IncludeUntracked),
		gitops.WithUntrackedLimits(int64(b.MaxUntrackedFileMB)<<20, int64(b.MaxUntrackedTotalMB)<<20),
		gitops.WithRetention(gitops.Retention{KeepAll: b.KeepAll, KeepDaily: b.KeepDaily, Max: b.MaxPerCopy}),
		gitops.WithPatchDir(b.PatchDir),
	)
}

// describeBackup summarises a backup run for one line of output.
func describeBackup(br *gitops.BackupResult) string {
	var parts []string
	switch br.Outcome {
	case gitops.BackupSaved:
		parts = append(parts, "backed up "+br.ID)
	case gitops.BackupUnchanged:
		parts = append(parts, "unchanged since "+br.ID)
	default:
		parts = append(parts, "clean")
	}
	if br.IndexUnmerged {
		parts = append(parts, "index had conflicts, kept as unstaged")
	}
	if n := len(br.Skipped); n > 0 {
		parts = append(parts, fmt.Sprintf("%d untracked path(s) left out: %s", n, skippedSummary(br.Skipped)))
	}
	return strings.Join(parts, "; ")
}

func skippedSummary(skipped []gitops.SkippedFile) string {
	const show = 3
	var names []string
	for i, s := range skipped {
		if i == show {
			names = append(names, fmt.Sprintf("and %d more", len(skipped)-show))
			break
		}
		names = append(names, s.Path+" ("+s.Reason+")")
	}
	return strings.Join(names, ", ")
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
			return c.runList(ctx, cfg, st)
		}
		return c.runRestore(ctx, cfg, st)
	}

	st, err := ctx.LoadState()
	if err != nil {
		return err
	}
	pass, err := selection{c.Repos, c.Tag, c.Set}.predicate(cfg)
	if err != nil {
		return err
	}

	// Back up without the state lock, then record every outcome in one write.
	var jobs []copyJob
	for _, key := range slices.Sorted(maps.Keys(st.Repos)) {
		repo := st.Repos[key]
		if !pass(key, repo) || !repo.Cloned {
			continue
		}
		b := backupConfigFor(cfg.ResolveForRepo(key))
		jobs = append(jobs, repoJob(cfg, key, repo, &b))
	}
	outcomes := runCopies(ctx.GoCtx(), ctx.Logger(), jobs, cfg.Concurrency)
	if err := st.Update(cfg.LockTimeout, func(st *state.State) error {
		recordCopies(st, outcomes)
		return nil
	}); err != nil {
		return fmt.Errorf("recording backups: %w", err)
	}

	saved, failed := 0, 0
	for _, o := range outcomes {
		if o.backupErr != nil {
			failed++
			fmt.Fprintf(os.Stderr, "  %s: %v\n", o.job.label, o.backupErr)
		}
		br := o.backup
		if br == nil || !br.Dirty() {
			continue
		}
		if br.Outcome == gitops.BackupSaved {
			saved++
			logEvent(ctx, "backup", o.job.label, br.ID)
		}
		if !ctx.Quiet || len(br.Skipped) > 0 {
			fmt.Printf("  %s: %s\n", o.job.label, describeBackup(br))
		}
	}
	if !ctx.Quiet {
		fmt.Printf("Saved %d new backup(s)\n", saved)
	}
	return ctx.finish(copiesJSON(outcomes), outcome(failed, len(jobs), "repos failed to back up"))
}

func (c *BackupCmd) runList(ctx *Context, cfg *config.Config, st *state.State) error {
	pass, err := selection{c.Repos, c.Tag, c.Set}.predicate(cfg)
	if err != nil {
		return err
	}
	doc := map[string][]backupJSON{}
	failed := 0
	for _, key := range slices.Sorted(maps.Keys(st.Repos)) {
		repo := st.Repos[key]
		if !pass(key, repo) || !repo.Cloned {
			continue
		}
		backups, err := gitops.ListBackups(ctx.GoCtx(), repo.LocalPath, cfg.ResolveForRepo(key).Backup.PatchDir)
		if err != nil {
			failed++
			fmt.Fprintf(os.Stderr, "  %s: %v\n", key, err)
			continue
		}
		if len(backups) == 0 {
			continue
		}
		fmt.Printf("%s:\n", key)
		for _, b := range backups {
			fmt.Printf("  %s  %s\n", b.ID, backupKinds(b))
			doc[key] = append(doc[key], backupInfoJSON(b))
		}
	}
	return ctx.finish(doc, outcome(failed, failed+len(doc), "repos' backups could not be read"))
}

// backupKinds says what a backup consists of.
func backupKinds(b gitops.BackupInfo) string {
	var kinds []string
	if b.Ref != "" {
		kinds = append(kinds, "snapshot")
	}
	if b.PatchDir != "" {
		kinds = append(kinds, "patch")
	}
	if b.Meta != nil && len(b.Meta.Skipped) > 0 {
		kinds = append(kinds, fmt.Sprintf("%d untracked path(s) left out", len(b.Meta.Skipped)))
	}
	return strings.Join(kinds, ", ")
}

func (c *BackupCmd) runRestore(ctx *Context, cfg *config.Config, st *state.State) error {
	pass, err := selection{c.Repos, c.Tag, c.Set}.predicate(cfg)
	if err != nil {
		return err
	}
	docs := []restoreJSON{}
	for _, key := range slices.Sorted(maps.Keys(st.Repos)) {
		repo := st.Repos[key]
		if !pass(key, repo) || !repo.Cloned {
			continue
		}
		b, err := gitops.FindBackup(ctx.GoCtx(), repo.LocalPath, cfg.ResolveForRepo(key).Backup.PatchDir, c.Restore)
		if err != nil {
			continue
		}
		docs = append(docs, restoreJSON{Target: key, Path: repo.LocalPath, Backup: backupInfoJSON(b)})
		printRestoreSteps(repo.LocalPath, key, b)
	}
	if len(docs) == 0 {
		fmt.Printf("No backup %q found for the selected repos.\n", c.Restore)
	} else {
		fmt.Println("Tip: `git-mgr restore <repo> [id]` can apply a backup, or --into a scratch dir.")
	}
	return ctx.finish(docs, nil)
}
