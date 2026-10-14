package cmd

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/ashtonian/gitten/internal/config"
	"github.com/ashtonian/gitten/internal/ctxutil"
	"github.com/ashtonian/gitten/internal/discovery"
	"github.com/ashtonian/gitten/internal/gitops"
	"github.com/ashtonian/gitten/internal/hook"
	"github.com/ashtonian/gitten/internal/oplog"
	"github.com/ashtonian/gitten/internal/state"
	"github.com/ashtonian/gitten/internal/workspace"
)

// eventsPath is the operation-log file, kept next to the state file so it
// follows GITTEN_STATE / --state.
func eventsPath(ctx *Context) string {
	return filepath.Join(filepath.Dir(ctx.StatePath()), "events.jsonl")
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

// hookRunner runs hooks within hooks.timeout, logging each checkout's last
// run of each event under <state dir>/hooks; echo also gets the output.
func hookRunner(cfg *config.Config, st *state.State, echo io.Writer) hook.Runner {
	return hook.Runner{LogDir: filepath.Join(st.Dir(), "hooks"), Timeout: cfg.Hooks.Timeout, Echo: echo}
}

// agentEnv is the environment of agent's commands in workspace ws, giving
// the agent its ports first if it has none (see workspace.AgentEnv).
func agentEnv(cfg *config.Config, st *state.State, ws, agent string) ([]string, error) {
	return workspace.AgentEnv(st, stateUpdater(cfg, st), workspace.SettingsFor(cfg, ws), ws, agent)
}

// staleCleanup judges tracked repos against a discovery pass under the
// configured cleanup policy, deleting clones through archivingRemover or
// moving them into the archive through cloneArchiver.
func staleCleanup(ctx *Context, cfg *config.Config, st *state.State) discovery.Cleanup {
	take := repoTaker(st.Path(), cfg.Backup.ArchiveDir, cfg.LockTimeout)
	remove := archivingRemover(ctx.GoCtx(), ctx.Logger(), cfg.Backup.ArchiveDir, cfg.Sync.CloneTimeout, take)
	if cfg.Cleanup.Policy == "archive" {
		remove = cloneArchiver(ctx.GoCtx(), ctx.Logger(), cfg.Backup.ArchiveDir, cfg.Sync.CloneTimeout, take)
	}
	return discovery.Cleanup{
		Config: cfg.Cleanup,
		Remove: remove,
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

// takenRepo is a tracked repo's clone, locked for deleting or archiving it,
// and whether archives lean on it, as they were once the lock was held.
type takenRepo struct {
	release func()
	leansOn bool
}

// repoTaker returns what takes repo key's clone at path for deleting or
// archiving it: its lock (see gitops.LockRepo), waiting up to wait, then a
// check against state as it is now that the repo is still tracked at path
// and no workspace copy was made from it meanwhile, and whether archives in
// archiveDir lean on it. A clone not on disk needs no lock.
func repoTaker(statePath, archiveDir string, wait time.Duration) func(ctx context.Context, key, path string) (takenRepo, error) {
	return func(ctx context.Context, key, path string) (takenRepo, error) {
		taken := takenRepo{release: func() {}}
		if gitops.HasGitDir(path) {
			release, err := gitops.LockRepo(ctx, path, true, wait)
			if err != nil {
				return taken, err
			}
			taken.release = release
		}
		fresh, err := state.Load(statePath)
		if err == nil {
			err = fresh.CheckRemovable(key)
		}
		if err == nil {
			if r := fresh.Repos[key]; r == nil || r.LocalPath != path {
				err = errors.New("it was moved or untracked meanwhile")
			}
		}
		if err != nil {
			taken.release()
			return taken, err
		}
		taken.leansOn = leansOn(fresh, archiveDir, key, path)
		return taken, nil
	}
}

// leansOn reports whether an archive may need repo key's history, its clone
// at path, to be restored: a thin bundle -- of a copy made from the repo, or
// of one of its submodules -- that holds only what the repo lacked. Archives
// on record count, and so does any in archiveDir that a run interrupted
// before recording it left there. An archive whose description can't be
// read might lean on it, so it counts too.
func leansOn(st *state.State, archiveDir, key, path string) bool {
	if st.LeansOn(key) {
		return true
	}
	descriptions, _ := filepath.Glob(filepath.Join(archiveDir, "*.json"))
	for _, d := range descriptions {
		meta, _, err := gitops.ReadArchive(d)
		if err != nil {
			return true
		}
		if meta.Bundle == "" || meta.Complete {
			continue
		}
		base := filepath.Clean(meta.Base)
		if meta.Repo == key || base == filepath.Clean(path) || strings.HasPrefix(base, filepath.Clean(path)+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// archivingRemover deletes stale clones through gitops.RemoveClone: unique work
// is bundled into archiveDir first, and a clone that can't be inspected or
// archived is kept. A repo other archives lean on has its whole history
// bundled.
func archivingRemover(ctx context.Context, log *slog.Logger, archiveDir string, timeout time.Duration, take func(context.Context, string, string) (takenRepo, error)) discovery.RemoveFunc {
	return func(key string, repo *state.RepoState) (*state.ArchiveEntry, error) {
		taken, err := take(ctx, key, repo.LocalPath)
		if err != nil {
			log.Warn("stale repo kept", "repo", key, "path", repo.LocalPath, "error", err)
			return nil, err
		}
		defer taken.release()
		rctx, cancel := ctxutil.WithTimeout(ctx, timeout)
		defer cancel()
		archived, err := gitops.RemoveClone(rctx, repo.LocalPath, archiveDir, key, gitops.RemoveOptions{KeepHistory: taken.leansOn, Repo: key})
		err = ctxutil.TimedOut(rctx, err, "archiving and deleting "+repo.LocalPath, timeout, "sync.clone_timeout")
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

// cloneArchiver moves stale clones, whole, into archiveDir.
func cloneArchiver(ctx context.Context, log *slog.Logger, archiveDir string, timeout time.Duration, take func(context.Context, string, string) (takenRepo, error)) discovery.RemoveFunc {
	return func(key string, repo *state.RepoState) (*state.ArchiveEntry, error) {
		taken, err := take(ctx, key, repo.LocalPath)
		if err != nil {
			log.Warn("stale repo kept", "repo", key, "path", repo.LocalPath, "error", err)
			return nil, err
		}
		defer taken.release()
		rctx, cancel := ctxutil.WithTimeout(ctx, timeout)
		defer cancel()
		entry, err := archiveClone(rctx, log, archiveDir, key, repo.LocalPath)
		err = ctxutil.TimedOut(rctx, err, "archiving "+repo.LocalPath, timeout, "sync.clone_timeout")
		switch {
		case err != nil:
			log.Warn("stale repo kept", "repo", key, "path", repo.LocalPath, "error", err)
		case entry != nil:
			log.Info("moved stale repo into the archive", "repo", key, "archive", entry.Path)
		}
		return entry, err
	}
}

// archiveClone moves repo key's clone at path, whole, into archiveDir and
// returns its catalog entry, nil when the clone was already gone. A clone
// moved into the archive counts as archived even when what came after the
// move failed (see pathutil.MovedError); that is logged.
func archiveClone(ctx context.Context, log *slog.Logger, archiveDir, key, path string) (*state.ArchiveEntry, error) {
	archived, err := gitops.ArchiveClone(ctx, path, archiveDir, key)
	if archived == "" {
		return nil, err
	}
	if err != nil {
		log.Warn("repo archived, but not cleanly", "repo", key, "path", path, "archive", archived, "error", err)
	}
	entry := archiveEntry(archived, key, path)
	return &entry, nil
}
