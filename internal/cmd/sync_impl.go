package cmd

import (
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
	scope := reconcile.Scope{}
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
			for _, key := range slices.Sorted(maps.Keys(outcome.Kept)) {
				tried++
				failures = append(failures, syncError{key, fmt.Errorf("stale cleanup kept its files: %w", outcome.Kept[key])})
			}
		}
	}

	machine := ""
	if offsiteWanted(cfg) {
		if machine, err = ensureMachine(cfg, st); err != nil {
			return fmt.Errorf("naming this machine for offsite refs: %w", err)
		}
	}
	jobs, skippedRecent := c.jobs(cfg, st, pass, machine)
	// Repos first: workspace clones are refreshed from what their repo's
	// clone just fetched.
	repoJobs, copyJobs := splitJobs(jobs)
	outcomes := runCopies(ctx.GoCtx(), log, repoJobs, cfg.Concurrency)
	outcomes = append(outcomes, runCopies(ctx.GoCtx(), log, copyJobs, cfg.Concurrency)...)
	err = st.Update(cfg.LockTimeout, func(st *state.State) error {
		recordCopies(st, outcomes)
		st.LastSync = time.Now()
		return nil
	})
	if err != nil {
		return fmt.Errorf("recording the sync: %w", err)
	}

	// Mirrors after fetches, which a slow mirror mustn't hold up.
	mirrored := 0
	if !c.NoFetch {
		jobs, _ := mirrorJobs(cfg, st, pass, c.Force)
		mirrors := runMirrors(ctx.GoCtx(), jobs, cfg.Concurrency)
		if err := recordMirrors(cfg, st, mirrors); err != nil {
			return fmt.Errorf("recording mirrors: %w", err)
		}
		for _, o := range mirrors {
			tried++
			if o.err != nil {
				failures = append(failures, syncError{o.job.key, fmt.Errorf("mirror: %w", o.err)})
			} else {
				mirrored++
			}
		}
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
	logEvent(ctx, "sync", "", fmt.Sprintf("fetched=%d backed_up=%d mirrored=%d failed=%d skipped_recent=%d skipped_locked=%d",
		fetched, backedUp, mirrored, len(failures), skippedRecent, skippedLocked))
	if !ctx.Quiet {
		fmt.Printf("Synced: %d fetched, %d skipped (recent), %d skipped (locked), %d failed, %d backed up",
			fetched, skippedRecent, skippedLocked, len(failures), backedUp)
		if mirrored > 0 {
			fmt.Printf(", %d mirrored", mirrored)
		}
		fmt.Println()
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

// jobs lists the copies this run backs up, fetches and, under
// remote_sync.enabled, takes offsite: selected repos, and the workspace
// copies of selected repos (all of them when nothing is selected). Repos
// fetched within min_fetch_interval are not fetched again; stale ones are
// only backed up. Workspace clones are refreshed from their repo's clone
// instead of fetched; worktrees share their repo's fetch.
func (c *SyncCmd) jobs(cfg *config.Config, st *state.State, pass func(string, *state.RepoState) bool, machine string) (jobs []copyJob, skippedRecent int) {
	for _, key := range slices.Sorted(maps.Keys(st.Repos)) {
		repo := st.Repos[key]
		if !pass(key, repo) || !repo.Cloned {
			continue
		}
		// A stale repo's upstream is gone, so it isn't fetched or pushed;
		// its local work is still backed up.
		stale := repo.Status == "stale"
		repoCfg := cfg.ResolveForRepo(key)
		var backup *gitops.BackupConfig
		if !c.NoBackup && repoCfg.Backup.Enabled {
			b := backupConfigFor(repoCfg)
			backup = &b
		}
		job := repoJob(cfg, key, repo, backup)
		// sync.enabled: false keeps a repo tracked and backed up, never fetched.
		if !c.NoFetch && repoCfg.Sync.Enabled && !stale {
			if !c.Force && !repo.LastFetch.IsZero() && time.Since(repo.LastFetch) < repoCfg.Sync.MinFetchInterval {
				skippedRecent++
			} else {
				job.fetch = true
			}
		}
		if policy := cfg.RemoteSyncFor(key, repo.DiscoveredBy); policy.Enabled && machine != "" && !stale {
			job.offsite = offsiteFor(cfg, policy, machine, identityOf(repo), job.sshKey, repo.OffsiteStatus)
		}
		jobs = append(jobs, job)
	}

	// Agents' copies never show up as tracked repos; without this their
	// uncommitted work -- what workspaces exist to keep -- would be
	// unprotected by the scheduled run.
	jobs = addCopyJobs(jobs, st, selection{c.Repos, c.Tag, c.Set}, pass, func(ws *state.WorkspaceState, agent string, cs state.CopyState) *copyJob {
		job := copyJobFor(cfg, st, ws, agent, cs, !c.NoBackup)
		if policy := copyRemoteSync(cfg, st, ws.Name, cs); policy.Enabled && machine != "" {
			job.offsite = offsiteFor(cfg, policy, machine, copyIdentity(st, cs), job.sshKey, cs.OffsiteStatus)
		}
		if !c.NoFetch {
			job.refresh = refreshSource(cfg, st, ws, cs)
		}
		if job.backup == nil && job.offsite == nil && job.refresh == "" {
			return nil
		}
		return &job
	})
	return jobs, skippedRecent
}

// refreshSource is the clone a workspace copy is refreshed from: its repo's,
// for a clone of a repo sync fetches; "" otherwise.
func refreshSource(cfg *config.Config, st *state.State, ws *state.WorkspaceState, cs state.CopyState) string {
	if ws.Isolation != config.IsolationClone && ws.Isolation != config.IsolationReferenceClone {
		return ""
	}
	repo := st.Repos[cs.Repo]
	if repo == nil || !repo.Cloned || repo.Status == "stale" || !cfg.ResolveForRepo(cs.Repo).Sync.Enabled {
		return ""
	}
	return repo.LocalPath
}

// splitJobs separates tracked repos' jobs from workspace copies'.
func splitJobs(jobs []copyJob) (repos, copies []copyJob) {
	for _, j := range jobs {
		if j.repoKey != "" {
			repos = append(repos, j)
		} else {
			copies = append(copies, j)
		}
	}
	return repos, copies
}
