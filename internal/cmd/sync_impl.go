package cmd

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"time"

	"github.com/ashtonian/git-mgr/internal/config"
	"github.com/ashtonian/git-mgr/internal/discovery"
	"github.com/ashtonian/git-mgr/internal/gitops"
	"github.com/ashtonian/git-mgr/internal/notify"
	"github.com/ashtonian/git-mgr/internal/reconcile"
	"github.com/ashtonian/git-mgr/internal/state"
)

func (c *SyncCmd) Run(ctx *Context) error {
	cfg, err := ctx.LoadConfig()
	if err != nil {
		return err
	}
	st, err := ctx.LoadState()
	if err != nil {
		return err
	}
	pass, err := selection{c.Repos, c.Tag, c.Set}.predicate(cfg)
	if err != nil {
		return err
	}

	// Syncs don't overlap; a scheduled one that finds another running is
	// routine. Other commands only wait on the state lock, which this run
	// takes just long enough to record each change.
	release, err := st.RunLock("sync")
	if err != nil {
		if !errors.Is(err, state.ErrLocked) {
			return err
		}
		if !ctx.Quiet {
			fmt.Fprintln(os.Stderr, "Another sync is running, skipping.")
		}
		return nil
	}
	defer release()
	if err := st.Reload(); err != nil {
		return fmt.Errorf("reloading state: %w", err)
	}

	log := ctx.Logger()
	var failures []syncError
	tried := 0

	// Discover, then bring repos in line with config as far as reconcile.auto
	// allows. Without discovery only hand-added repos can be judged.
	scope := reconcile.Scope{Repos: true}
	var results []discovery.DiscoverResult
	if cfg.Sync.AutoDiscover && !c.NoDiscover {
		if !ctx.Quiet {
			fmt.Println("Discovering repos (auto-discover enabled)...")
		}
		results, scope.Discovered, err = discovery.RunDiscovery(ctx.GoCtx(), cfg, "", c.Tag)
		if err != nil {
			log.Warn("auto-discovery failed", "error", err)
		} else {
			scope.Rules = discovery.StaleScopeOf(results, "", c.Tag)
		}
		for _, r := range results {
			for _, why := range r.Incomplete {
				log.Warn("discovery incomplete; the rule's repos are not judged stale", "rule", r.Rule, "reason", why)
			}
		}
	}
	plan := reconcile.Compute(ctx.GoCtx(), cfg, st, scope)
	changes := applyAuto(ctx, cfg, st, plan, reconcile.AutoFilter(cfg.Reconcile.Auto))
	changesFailed, changesTried := failedChanges(changes)+len(plan.Blocked), len(changes)+len(plan.Blocked)

	// Judge staleness only if every rule succeeded. On a partial failure the
	// discovered set is incomplete, and marking the missing repos stale (or
	// deleting them under remove_and_delete) would be destructive on a
	// transient provider outage.
	if scope.Discovered != nil {
		if discovery.AnyFailed(results) {
			log.Warn("skipping stale detection: one or more discovery rules failed")
		} else {
			outcome, err := staleCleanup(ctx, cfg, st).Run(st, discoveredKeys(scope.Discovered), scope.Rules)
			tried++
			if err != nil {
				failures = append(failures, syncError{"stale cleanup", err})
			}
			if outcome.Reaped > 0 {
				log.Info("cleaned up stale repos", "count", outcome.Reaped, "policy", cfg.Cleanup.Policy)
			}
			if outcome.Failed > 0 {
				log.Warn("some stale repos could not be deleted", "count", outcome.Failed)
			}
		}
	}

	jobs, skippedRecent := c.jobs(cfg, st, pass)
	outcomes := runCopies(ctx.GoCtx(), log, jobs, cfg.Concurrency)
	err = st.Update(cfg.LockTimeout, func(st *state.State) error {
		recordCopies(st, outcomes)
		st.LastSync = time.Now()
		return nil
	})
	if err != nil {
		return fmt.Errorf("recording the sync: %w", err)
	}

	fetched, skippedLocked, backedUp := 0, 0, 0
	for _, o := range outcomes {
		if o.job.backup != nil {
			tried++
		}
		if o.fetched {
			tried++
		}
		switch {
		case o.fetched && o.fetchErr == nil:
			fetched++
		case errors.Is(o.fetchErr, gitops.ErrLocked):
			skippedLocked++
		}
		if o.backup != nil && o.backup.Outcome == gitops.BackupSaved {
			backedUp++
		}
		for _, err := range o.failed() {
			failures = append(failures, syncError{o.job.label, err})
		}
	}

	if cfg.Notify.Enabled && cfg.Notify.OnError && len(failures) > 0 {
		notify.Send("git-mgr sync errors", fmt.Sprintf("%d repos failed to sync", len(failures)))
	}
	logEvent(ctx, "sync", "", fmt.Sprintf("fetched=%d backed_up=%d failed=%d skipped_recent=%d skipped_locked=%d",
		fetched, backedUp, len(failures), skippedRecent, skippedLocked))
	if !ctx.Quiet {
		fmt.Printf("Synced: %d fetched, %d skipped (recent), %d skipped (locked), %d failed, %d backed up\n",
			fetched, skippedRecent, skippedLocked, len(failures), backedUp)
	}
	for _, e := range failures {
		fmt.Fprintf(os.Stderr, "  %s: %v\n", e.repo, e.err)
	}
	return outcome(len(failures)+changesFailed, tried+changesTried, "sync operations failed")
}

type syncError struct {
	repo string
	err  error
}

// jobs lists the copies this run backs up and fetches: selected repos, and
// the workgroup members of selected repos (all of them when nothing is
// selected). Repos fetched within min_fetch_interval are only backed up.
func (c *SyncCmd) jobs(cfg *config.Config, st *state.State, pass func(string, *state.RepoState) bool) (jobs []copyJob, skippedRecent int) {
	for _, key := range slices.Sorted(maps.Keys(st.Repos)) {
		repo := st.Repos[key]
		if !pass(key, repo) || repo.Status == "stale" || !repo.Cloned {
			continue
		}
		repoCfg := cfg.ResolveForRepo(key)
		var backup *gitops.BackupConfig
		if !c.NoBackup && repoCfg.Backup.Enabled {
			b := backupConfigFor(cfg, repoCfg, key, repo.DiscoveredBy, c.PushBranch, "")
			backup = &b
		}
		job := repoJob(cfg, key, repo, backup)
		// sync.enabled: false keeps a repo tracked and backed up, never fetched.
		if !c.NoFetch && repoCfg.Sync.Enabled {
			if !c.Force && !repo.LastFetch.IsZero() && time.Since(repo.LastFetch) < repoCfg.Sync.MinFetchInterval {
				skippedRecent++
			} else {
				job.fetch = true
			}
		}
		jobs = append(jobs, job)
	}

	// Agent worktrees and clones never show up as tracked repos; without
	// this their uncommitted work -- what workgroups exist to keep -- would
	// be unprotected by the scheduled run.
	memberCfg := cfg.ResolveForRepo("")
	if c.NoBackup || !memberCfg.Backup.Enabled {
		return jobs, skippedRecent
	}
	backup := backupConfigFor(cfg, memberCfg, "", "", c.PushBranch, "")
	all := (selection{c.Repos, c.Tag, c.Set}).empty()
	// A member adopted from a tracked repo's directory is already a job; two
	// backups of one working copy at once would race on its refs.
	queued := make(map[string]bool, len(jobs))
	for _, j := range jobs {
		queued[j.path] = true
	}
	for _, wgName := range slices.Sorted(maps.Keys(st.Workgroups)) {
		for _, m := range st.Workgroups[wgName].Members {
			if repo := st.Repos[m.RepoKey]; !all && (repo == nil || !pass(m.RepoKey, repo)) {
				continue
			}
			if queued[m.Path] || !gitops.HasGitDir(m.Path) {
				continue
			}
			queued[m.Path] = true
			jobs = append(jobs, copyJob{
				label: wgName + "/" + m.Name, workgroup: wgName, member: m.Name, path: m.Path,
				backup: &backup, backupTimeout: memberCfg.Sync.BackupTimeout,
			})
		}
	}
	return jobs, skippedRecent
}

func fetchWithRetry(ctx context.Context, repoPath, sshKey string, cfg config.SyncConfig) error {
	var lastErr error
	for attempt := 0; attempt <= cfg.Retries; attempt++ {
		if attempt > 0 {
			backoff := cfg.RetryBackoff * time.Duration(1<<uint(attempt-1))
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		lastErr = gitops.Fetch(ctx, repoPath, sshKey)
		if lastErr == nil {
			return nil
		}
		// A lock is transient; don't burn retries waiting on another process.
		if errors.Is(lastErr, gitops.ErrLocked) {
			return lastErr
		}
	}
	return lastErr
}
