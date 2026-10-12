package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/ashtonian/git-mgr/internal/config"
	"github.com/ashtonian/git-mgr/internal/discovery"
	"github.com/ashtonian/git-mgr/internal/gitops"
	"github.com/ashtonian/git-mgr/internal/notify"
	"github.com/ashtonian/git-mgr/internal/reconcile"
	"golang.org/x/sync/errgroup"
)

type syncResult struct {
	fetched       int
	skippedRecent int
	skippedLocked int
	failed        int
	backedUp      int
	errors        []syncError
}

type syncError struct {
	repo string
	err  error
}

func (c *SyncCmd) Run(ctx *Context) error {
	cfg, err := ctx.LoadConfig()
	if err != nil {
		return err
	}
	st, err := ctx.LoadState()
	if err != nil {
		return err
	}

	// Acquire lock
	if err := st.AcquireLock(); err != nil {
		if !ctx.Quiet {
			fmt.Fprintln(os.Stderr, "Another git-mgr instance is running, skipping.")
		}
		return nil // exit 0, not an error
	}
	defer st.ReleaseLock()

	// Re-read state under the lock so this run mutates from the latest on-disk
	// state, not the snapshot loaded before the lock was held.
	if err := st.Reload(); err != nil {
		return fmt.Errorf("reloading state: %w", err)
	}

	log := ctx.Logger()
	var reconcileErr error

	// Discover, then bring repos in line with config as far as reconcile.auto
	// allows. Without discovery only hand-added repos can be judged.
	scope := reconcile.Scope{Repos: true}
	var results []discovery.DiscoverResult
	if cfg.Sync.AutoDiscover && !c.NoDiscover {
		if !ctx.Quiet {
			fmt.Println("Discovering repos (auto-discover enabled)...")
		}
		var err error
		results, scope.Discovered, err = discovery.RunDiscovery(ctx.GoCtx(), cfg, "", c.Tag)
		if err != nil {
			log.Warn("auto-discovery failed", "error", err)
		} else {
			scope.Rules = discovery.StaleScopeOf(results, "", c.Tag)
		}
	}
	plan := reconcile.Compute(ctx.GoCtx(), cfg, st, scope)
	if err := applyAuto(ctx, cfg, st, plan, reconcile.AutoFilter(cfg.Reconcile.Auto)); err != nil {
		reconcileErr = err
	}

	// Detect stale -- but only if every rule succeeded. On a partial failure the
	// discovered set is incomplete, and marking the missing repos stale (or
	// deleting them under remove_and_delete) would be destructive on a
	// transient provider outage.
	if scope.Discovered != nil {
		if discovery.AnyFailed(results) {
			log.Warn("skipping stale detection: one or more discovery rules failed")
		} else {
			discoveredKeys := make(map[string]bool)
			for k := range scope.Discovered {
				discoveredKeys[k] = true
			}
			outcome := discovery.DetectStale(st, discoveredKeys, scope.Rules, cfg.Cleanup, true,
				archivingRemover(ctx.GoCtx(), log, cfg.Backup.ArchiveDir))
			if outcome.Reaped > 0 {
				log.Info("cleaned up stale repos", "count", outcome.Reaped, "policy", cfg.Cleanup.Policy)
			}
			if outcome.Failed > 0 {
				log.Warn("some stale repos could not be deleted", "count", outcome.Failed)
			}
		}
	}

	// Sync repos
	result := &syncResult{}
	sem := make(chan struct{}, cfg.Concurrency)
	var mu sync.Mutex
	g, gctx := errgroup.WithContext(ctx.GoCtx())

	pass, err := selection{c.Repos, c.Tag, c.Set}.predicate(cfg)
	if err != nil {
		return err
	}

	for key, repo := range st.Repos {
		key, repo := key, repo

		if !pass(key, repo) || repo.Status == "stale" || !repo.Cloned {
			continue
		}

		repoCfg := cfg.ResolveForRepo(key)

		g.Go(func() error {
			sem <- struct{}{}
			defer func() { <-sem }()

			repoCtx, cancel := context.WithTimeout(gctx, repoCfg.Sync.TimeoutPerRepo)
			defer cancel()

			// Fetch (skipped for repos with sync.enabled: false -- tracked/backed
			// up but never auto-fetched).
			if !c.NoFetch && repoCfg.Sync.Enabled {
				if !c.Force && !repo.LastFetch.IsZero() && time.Since(repo.LastFetch) < repoCfg.Sync.MinFetchInterval {
					mu.Lock()
					result.skippedRecent++
					mu.Unlock()
				} else {
					sshKey := cfg.ResolveSSHKey(key, repo.DiscoveredBy)
					err := fetchWithRetry(repoCtx, repo.LocalPath, sshKey, repoCfg.Sync)
					mu.Lock()
					switch {
					case err == nil:
						result.fetched++
						repo.LastFetch = time.Now()
						repo.LastFetchError = ""
					case errors.Is(err, gitops.ErrLocked):
						// Transient: another git process holds a lock.
						// Skip this round without flagging an error.
						result.skippedLocked++
					default:
						result.failed++
						result.errors = append(result.errors, syncError{repo: key, err: err})
						repo.LastFetchError = err.Error()
						repo.Status = "error"
					}
					mu.Unlock()
				}
			}

			// Backup
			if !c.NoBackup && repoCfg.Backup.Enabled {
				backupCfg := backupConfigFor(cfg, repoCfg, key, repo.DiscoveredBy, c.PushBranch, "")
				br, err := gitops.Backup(repoCtx, repo.LocalPath, backupCfg)
				mu.Lock()
				recordBackup(repo, br, err, time.Now())
				if err != nil {
					result.failed++
					result.errors = append(result.errors, syncError{repo: key, err: err})
				}
				if br.Outcome == gitops.BackupSaved {
					result.backedUp++
				}
				if br.Dirty() {
					log.Debug("backup", "repo", key, "outcome", br.Outcome.String(), "id", br.ID, "pushed", br.PushedBranch)
				}
				if len(br.Skipped) > 0 {
					log.Warn("untracked paths left out of backup", "repo", key, "count", len(br.Skipped), "paths", skippedSummary(br.Skipped))
				}
				mu.Unlock()
			}

			// Update status
			if rs, err := gitops.GetStatus(repoCtx, repo.LocalPath); err == nil {
				mu.Lock()
				repo.HeadCommit = rs.HeadCommit
				repo.CurrentBranch = rs.CurrentBranch
				if repo.Status != "error" && repo.Status != "stale" {
					repo.Status = rs.StatusString()
				}
				mu.Unlock()
			}

			return nil
		})
	}

	_ = g.Wait()

	// Back up workgroup members too. The tracked-repo loop above never sees agent
	// worktrees, so without this their uncommitted work is unprotected by the
	// scheduled run -- exactly the data the workgroup subsystem exists to keep.
	if !c.NoBackup {
		repoCfg := cfg.ResolveForRepo("")
		bcfg := backupConfigFor(cfg, repoCfg, "", "", c.PushBranch, "")
		for wgName, ws := range st.Workgroups {
			for _, m := range ws.Members {
				if !gitops.HasGitDir(m.Path) {
					continue
				}
				br, berr := gitops.Backup(ctx.GoCtx(), m.Path, bcfg)
				if berr != nil {
					result.failed++
					result.errors = append(result.errors, syncError{repo: wgName + "/" + m.Name, err: berr})
				}
				if br.Outcome == gitops.BackupSaved {
					result.backedUp++
				}
			}
		}
	}

	// Notifications
	if cfg.Notify.Enabled && len(result.errors) > 0 && cfg.Notify.OnError {
		notify.Send("git-mgr sync errors",
			fmt.Sprintf("%d repos failed to sync", len(result.errors)))
	}

	st.LastSync = time.Now()
	if err := st.Save(); err != nil {
		return fmt.Errorf("saving state: %w", err)
	}
	logEvent(ctx, "sync", "", fmt.Sprintf("fetched=%d backed_up=%d failed=%d skipped_recent=%d skipped_locked=%d",
		result.fetched, result.backedUp, result.failed, result.skippedRecent, result.skippedLocked))

	if !ctx.Quiet {
		fmt.Printf("Synced: %d fetched, %d skipped (recent), %d skipped (locked), %d failed, %d backed up\n",
			result.fetched, result.skippedRecent, result.skippedLocked, result.failed, result.backedUp)
		for _, e := range result.errors {
			fmt.Printf("  %s: %v\n", e.repo, e.err)
		}
	}

	if len(result.errors) > 0 {
		return errors.Join(fmt.Errorf("%d repos failed", len(result.errors)), reconcileErr)
	}
	return reconcileErr
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
