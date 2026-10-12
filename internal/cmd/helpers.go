package cmd

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/ashtonian/git-mgr/internal/config"
	"github.com/ashtonian/git-mgr/internal/discovery"
	"github.com/ashtonian/git-mgr/internal/gitops"
	"github.com/ashtonian/git-mgr/internal/oplog"
	"github.com/ashtonian/git-mgr/internal/state"
)

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// eventsPath is the operation-log file, kept next to the state file so it
// follows GIT_MGR_STATE / --state.
func eventsPath(ctx *Context) string {
	return filepath.Join(filepath.Dir(config.ExpandHome(ctx.State)), "events.jsonl")
}

// logEvent appends a best-effort entry to the operation log (never fails the
// caller). Honors log.events: false as an off-switch.
func logEvent(ctx *Context, action, repo, detail string) {
	if cfg, err := ctx.LoadConfig(); err == nil && !cfg.Log.Events {
		return
	}
	_ = oplog.Append(eventsPath(ctx), oplog.Event{Time: time.Now(), Action: action, Repo: repo, Detail: detail})
}

// stateUpdater returns a func that applies a change to st under the state
// lock: it waits up to lock_timeout for the lock, re-reads the file, applies
// change and saves. Commands that do slow work (clones, fetches, archives)
// do it unlocked and record the outcome through this, so they never hold
// up another command for longer than a write.
func stateUpdater(cfg *config.Config, st *state.State) func(change func(*state.State) error) error {
	return func(change func(*state.State) error) error {
		return st.Update(cfg.LockTimeout, change)
	}
}

// staleCleanup judges tracked repos against a discovery pass under the
// configured cleanup policy, deleting through archivingRemover.
func staleCleanup(ctx *Context, cfg *config.Config, st *state.State) discovery.Cleanup {
	return discovery.Cleanup{
		Config: cfg.Cleanup,
		Remove: archivingRemover(ctx.GoCtx(), ctx.Logger(), cfg.Backup.ArchiveDir),
		Update: stateUpdater(cfg, st),
	}
}

// discoveredKeys is the set of repo keys a discovery pass found.
func discoveredKeys[V any](discovered map[string]V) map[string]bool {
	keys := make(map[string]bool, len(discovered))
	for k := range discovered {
		keys[k] = true
	}
	return keys
}

// archivingRemover deletes stale clones through gitops.RemoveClone: unique work
// is bundled into archiveDir first, and a clone that can't be inspected or
// archived is kept.
func archivingRemover(ctx context.Context, log *slog.Logger, archiveDir string) discovery.RemoveFunc {
	return func(key string, repo *state.RepoState) (*state.ArchiveEntry, error) {
		archived, err := gitops.RemoveClone(ctx, repo.LocalPath, archiveDir, key, false)
		if err != nil {
			log.Warn("stale repo kept", "repo", key, "path", repo.LocalPath, "error", err)
			return nil, err
		}
		if archived == "" {
			return nil, nil
		}
		log.Info("archived stale repo's unique work before deleting it", "repo", key, "archive", archived)
		entry := archiveEntry(archived, key, repo.LocalPath)
		return &entry, nil
	}
}
