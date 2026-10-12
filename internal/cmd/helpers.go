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

// archivingRemover deletes stale clones through gitops.RemoveClone: unique work
// is bundled into archiveDir first, and a clone that can't be inspected or
// archived is kept.
func archivingRemover(ctx context.Context, log *slog.Logger, archiveDir string) discovery.RemoveFunc {
	return func(key string, repo *state.RepoState) error {
		archived, err := gitops.RemoveClone(ctx, repo.LocalPath, archiveDir, key, false)
		if err != nil {
			log.Warn("stale repo kept", "repo", key, "path", repo.LocalPath, "error", err)
			return err
		}
		if archived != "" {
			log.Info("archived stale repo's unique work before deleting it", "repo", key, "archive", archived)
		}
		return nil
	}
}
