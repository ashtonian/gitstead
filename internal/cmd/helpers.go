package cmd

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/ashtonian/git-mgr/internal/config"
	"github.com/ashtonian/git-mgr/internal/discovery"
	"github.com/ashtonian/git-mgr/internal/gitops"
	"github.com/ashtonian/git-mgr/internal/hook"
	"github.com/ashtonian/git-mgr/internal/oplog"
	"github.com/ashtonian/git-mgr/internal/state"
	"github.com/ashtonian/git-mgr/internal/workspace"
)

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
	take := repoTaker(st.Path(), cfg.LockTimeout)
	remove := archivingRemover(ctx.GoCtx(), ctx.Logger(), cfg.Backup.ArchiveDir, take)
	if cfg.Cleanup.Policy == "archive" {
		remove = cloneArchiver(ctx.GoCtx(), ctx.Logger(), cfg.Backup.ArchiveDir, take)
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
// and state as it was once the lock was held.
type takenRepo struct {
	release func()
	state   *state.State
}

// repoTaker returns what takes repo key's clone at path for deleting or
// archiving it: its lock (see gitops.LockRepo), waiting up to wait, then a
// check against state as it is now that the repo is still tracked at path
// and no workspace copy was made from it meanwhile. A clone not on disk
// needs no lock.
func repoTaker(statePath string, wait time.Duration) func(ctx context.Context, key, path string) (takenRepo, error) {
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
		taken.state = fresh
		return taken, nil
	}
}

// archivingRemover deletes stale clones through gitops.RemoveClone: unique work
// is bundled into archiveDir first, and a clone that can't be inspected or
// archived is kept. A repo other archives lean on has its whole history
// bundled.
func archivingRemover(ctx context.Context, log *slog.Logger, archiveDir string, take func(context.Context, string, string) (takenRepo, error)) discovery.RemoveFunc {
	return func(key string, repo *state.RepoState) (*state.ArchiveEntry, error) {
		taken, err := take(ctx, key, repo.LocalPath)
		if err != nil {
			log.Warn("stale repo kept", "repo", key, "path", repo.LocalPath, "error", err)
			return nil, err
		}
		defer taken.release()
		archived, err := gitops.RemoveClone(ctx, repo.LocalPath, archiveDir, key, gitops.RemoveOptions{KeepHistory: taken.state.LeansOn(key)})
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
func cloneArchiver(ctx context.Context, log *slog.Logger, archiveDir string, take func(context.Context, string, string) (takenRepo, error)) discovery.RemoveFunc {
	return func(key string, repo *state.RepoState) (*state.ArchiveEntry, error) {
		taken, err := take(ctx, key, repo.LocalPath)
		if err != nil {
			log.Warn("stale repo kept", "repo", key, "path", repo.LocalPath, "error", err)
			return nil, err
		}
		defer taken.release()
		entry, err := archiveClone(ctx, log, archiveDir, key, repo.LocalPath)
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
// archived whole but whose old folder couldn't all be deleted counts as
// archived; the leftovers are logged.
func archiveClone(ctx context.Context, log *slog.Logger, archiveDir, key, path string) (*state.ArchiveEntry, error) {
	archived, err := gitops.ArchiveClone(ctx, path, archiveDir, key)
	if archived == "" {
		return nil, err
	}
	if err != nil {
		log.Warn("repo archived, but some of its old folder is left; delete it by hand", "repo", key, "path", path, "error", err)
	}
	entry := archiveEntry(archived, key, path)
	return &entry, nil
}
