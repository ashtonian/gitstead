package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/ashtonian/git-mgr/internal/config"
	"github.com/ashtonian/git-mgr/internal/discovery"
	"github.com/ashtonian/git-mgr/internal/gitops"
	"github.com/ashtonian/git-mgr/internal/notify"
	"github.com/ashtonian/git-mgr/internal/pathtemplate"
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

	log := ctx.Logger()

	// Auto-discover if enabled
	if cfg.Sync.AutoDiscover && !c.NoDiscover {
		if !ctx.Quiet {
			fmt.Println("Discovering repos (auto-discover enabled)...")
		}
		_, discovered, err := discovery.RunDiscovery(context.Background(), cfg, "", c.Tag)
		if err != nil {
			log.Warn("auto-discovery failed", "error", err)
		} else {
			// Clone new repos
			for key, repo := range discovered {
				if _, exists := st.Repos[key]; !exists {
					if !ctx.Quiet {
						fmt.Printf("  New repo: %s\n", key)
					}
					repoCfg := cfg.ResolveForRepo(key)
					relPath, err := resolveRepoPath(repoCfg, repo)
					if err != nil {
						log.Warn("path template error", "repo", key, "error", err)
						continue
					}
					destPath := filepath.Join(cfg.BaseDir, relPath)
					sshKey := cfg.ResolveSSHKey(key, "")
					if err := gitops.Clone(context.Background(), repo.SSHURL, destPath, sshKey); err != nil {
						log.Warn("clone failed", "repo", key, "error", err)
						continue
					}
					addRepoToState(st, repo, destPath)
				}
			}

			// Detect stale
			discoveredKeys := make(map[string]bool)
			for k := range discovered {
				discoveredKeys[k] = true
			}
			discovery.DetectStale(st, discoveredKeys, cfg.Cleanup)
		}
	}

	// Sync repos
	result := &syncResult{}
	sem := make(chan struct{}, cfg.Concurrency)
	var mu sync.Mutex
	g, gctx := errgroup.WithContext(context.Background())

	for key, repo := range st.Repos {
		key, repo := key, repo

		if c.Repos != "" {
			matched, _ := filepath.Match(c.Repos, key)
			if !matched {
				continue
			}
		}
		if c.Tag != "" && !repo.HasTag(c.Tag) {
			continue
		}
		if repo.Status == "stale" {
			continue
		}
		if !repo.Cloned {
			continue
		}

		repoCfg := cfg.ResolveForRepo(key)

		g.Go(func() error {
			sem <- struct{}{}
			defer func() { <-sem }()

			repoCtx, cancel := context.WithTimeout(gctx, repoCfg.Sync.TimeoutPerRepo)
			defer cancel()

			// Fetch
			if !c.NoFetch {
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
				backupCfg := gitops.NewBackupConfig(
					gitops.WithStrategy(repoCfg.Backup.Strategy),
					gitops.WithIncludeUntracked(repoCfg.Backup.IncludeUntracked),
					gitops.WithMaxPerRepo(repoCfg.Backup.MaxPerRepo),
					gitops.WithPatchDir(repoCfg.Backup.PatchDir),
				)
				br, err := gitops.Backup(repoCtx, repo.LocalPath, backupCfg)
				mu.Lock()
				if err != nil {
					log.Warn("backup failed", "repo", key, "error", err)
				} else if br.IsDirty {
					result.backedUp++
					repo.Status = "dirty"
					repo.LastBackup = time.Now()
					repo.LastBackupRef = br.RefName
					repo.LastBackupHasPatch = br.HasPatch
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

	g.Wait()

	// Notifications
	if cfg.Notify.Enabled && len(result.errors) > 0 && cfg.Notify.OnError {
		notify.Send("git-mgr sync errors",
			fmt.Sprintf("%d repos failed to sync", len(result.errors)))
	}

	st.LastSync = time.Now()
	if err := st.Save(); err != nil {
		return fmt.Errorf("saving state: %w", err)
	}

	if !ctx.Quiet {
		fmt.Printf("Synced: %d fetched, %d skipped (recent), %d skipped (locked), %d failed, %d backed up\n",
			result.fetched, result.skippedRecent, result.skippedLocked, result.failed, result.backedUp)
		for _, e := range result.errors {
			fmt.Printf("  %s: %v\n", e.repo, e.err)
		}
	}

	if len(result.errors) > 0 {
		return fmt.Errorf("%d repos failed", len(result.errors))
	}
	return nil
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

func resolveRepoPath(cfg *config.Config, repo discovery.Repo) (string, error) {
	return pathtemplate.Resolve(cfg.PathTemplate, pathtemplate.RepoData{
		Host:     repo.Host,
		Owner:    repo.Owner,
		Name:     repo.Name,
		FullName: repo.FullName,
	})
}
