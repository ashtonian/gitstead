package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/ashtonian/git-mgr/internal/config"
	"github.com/ashtonian/git-mgr/internal/ctxutil"
	"github.com/ashtonian/git-mgr/internal/gitops"
	"github.com/ashtonian/git-mgr/internal/reconcile"
	"github.com/ashtonian/git-mgr/internal/state"
)

func recorded(st *state.State, name string) (*state.WorkspaceState, error) {
	if ws := st.Workspaces[name]; ws != nil {
		return ws, nil
	}
	return nil, fmt.Errorf("workspace %s is no longer recorded", name)
}

func recordedCopy(st *state.State, name, agent, path string) (*state.CopyState, error) {
	if c := st.CopyAt(name, agent, path); c != nil {
		return c, nil
	}
	return nil, fmt.Errorf("%s/%s's copy at %s is no longer recorded", name, agent, path)
}

// currentSource is where c's repo's clone is now: its repo may have moved
// since the plan was made.
func currentSource(st *state.State, c Copy) string {
	if repo := st.Repos[c.Repo]; repo != nil && repo.LocalPath != "" {
		return repo.LocalPath
	}
	return c.Source
}

func createWorkspace(spec *Spec) reconcile.RunFunc {
	return func(_ context.Context, env *reconcile.Env) (reconcile.Outcome, error) {
		ws := &state.WorkspaceState{
			Name: spec.Name, Dir: spec.Dir, Isolation: spec.Isolation, BaseRef: spec.BaseRef,
			Declared: spec.Declared, CreatedAt: env.Time(),
		}
		if spec.Folder != "" {
			if err := os.MkdirAll(spec.Folder, 0o755); err != nil {
				return reconcile.Outcome{}, err
			}
		}
		if spec.Shared() {
			for _, r := range spec.Replicas {
				ws.Replicas = append(ws.Replicas, state.ReplicaState{Agent: r.Agent, Path: r.Path})
			}
		}
		return reconcile.Outcome{Record: func(st *state.State) error {
			if st.Workspaces[spec.Name] != nil {
				return fmt.Errorf("workspace %s was recorded by another run meanwhile", spec.Name)
			}
			st.Workspaces[spec.Name] = ws
			return nil
		}}, nil
	}
}

func updateWorkspace(spec *Spec) reconcile.RunFunc {
	return func(context.Context, *reconcile.Env) (reconcile.Outcome, error) {
		return reconcile.Outcome{Record: func(st *state.State) error {
			ws, err := recorded(st, spec.Name)
			if err != nil {
				return err
			}
			ws.Dir, ws.BaseRef, ws.Declared = spec.Dir, spec.BaseRef, spec.Declared
			if spec.Shared() {
				ws.Replicas = nil
				for _, r := range spec.Replicas {
					ws.Replicas = append(ws.Replicas, state.ReplicaState{Agent: r.Agent, Path: r.Path})
				}
			}
			return nil
		}}, nil
	}
}

// createCopy makes copy c, outside the state lock, and records it; replaces
// is the path of the record it replaces, when the copy was recorded
// elsewhere.
func createCopy(spec *Spec, r Replica, c Copy, replaces string) reconcile.RunFunc {
	return func(ctx context.Context, env *reconcile.Env) (reconcile.Outcome, error) {
		if _, err := recorded(env.State, spec.Name); err != nil {
			return reconcile.Outcome{}, err
		}
		c.Source = currentSource(env.State, c)
		cctx, cancel := ctxutil.WithTimeout(ctx, env.CloneTimeout)
		err := CreateCopy(cctx, spec.Isolation, spec.BaseRef, c)
		err = ctxutil.TimedOut(cctx, err, "making the copy", env.CloneTimeout, "sync.clone_timeout")
		cancel()
		if err != nil {
			return reconcile.Outcome{}, err
		}
		cs, err := copyState(ctx, spec.Isolation, c)
		return reconcile.Outcome{Record: recordCopyState(spec.Name, r, cs, replaces)}, err
	}
}

// recordCopy records copy c, found already made.
func recordCopy(spec *Spec, r Replica, c Copy) reconcile.RunFunc {
	return func(ctx context.Context, _ *reconcile.Env) (reconcile.Outcome, error) {
		cs, err := copyState(ctx, spec.Isolation, c)
		return reconcile.Outcome{Record: recordCopyState(spec.Name, r, cs, "")}, err
	}
}

// copyState is what to record of copy c, on disk: its ID and, for a clone,
// the managed git config it wants that isn't set yet, written now.
func copyState(ctx context.Context, isolation string, c Copy) (state.CopyState, error) {
	cs := state.CopyState{Repo: c.Repo, Path: c.Path, Branch: c.Branch}
	id, err := gitops.CopyID(ctx, c.Path)
	if err != nil {
		return cs, err
	}
	cs.CopyID = id
	if isolation == config.IsolationWorktree {
		return cs, nil
	}
	cs.URL = c.URL
	if len(c.Config) > 0 {
		have, err := gitops.LocalConfig(ctx, c.Path)
		if err != nil {
			return cs, err
		}
		if cs.ManagedConfig, err = reconcile.WriteManagedConfig(ctx, c.Path, c.Config, have); err != nil {
			return cs, err
		}
	}
	return cs, nil
}

func recordCopyState(name string, r Replica, cs state.CopyState, replaces string) func(*state.State) error {
	return func(st *state.State) error {
		ws, err := recorded(st, name)
		if err != nil {
			return err
		}
		rr := ws.UpsertReplica(r.Agent, r.Path)
		rr.Path = r.Path
		if replaces != "" {
			rr.DropCopy(replaces)
		}
		rr.UpsertCopy(cs)
		return nil
	}
}

func recordCopyRepo(name, agent, path, repo string) reconcile.RunFunc {
	return func(context.Context, *reconcile.Env) (reconcile.Outcome, error) {
		return reconcile.Outcome{Record: func(st *state.State) error {
			c, err := recordedCopy(st, name, agent, path)
			if err == nil {
				c.Repo = repo
			}
			return err
		}}, nil
	}
}

// moveCopy moves a copy as it is recorded, under the state lock, as a repo's
// move is.
func moveCopy(spec *Spec, r Replica, rec state.CopyState, c Copy) reconcile.RunFunc {
	return func(ctx context.Context, env *reconcile.Env) (reconcile.Outcome, error) {
		from := Copy{Repo: rec.Repo, Path: rec.Path, Source: currentSource(env.State, c)}
		return reconcile.Outcome{
			Record: func(st *state.State) error {
				if _, err := recordedCopy(st, spec.Name, r.Agent, rec.Path); err != nil {
					return err
				}
				if err := MoveCopy(ctx, spec.Isolation, from, c.Path); err != nil {
					return err
				}
				return movedCopy(st, spec.Name, r, rec.Path, c.Path)
			},
			After: func(context.Context) (string, error) {
				reconcile.RemoveEmptyParents(env, rec.Path)
				return "", nil
			},
		}, nil
	}
}

func recordCopyMove(name string, r Replica, from, to string) reconcile.RunFunc {
	return func(context.Context, *reconcile.Env) (reconcile.Outcome, error) {
		return reconcile.Outcome{Record: func(st *state.State) error {
			return movedCopy(st, name, r, from, to)
		}}, nil
	}
}

func movedCopy(st *state.State, name string, r Replica, from, to string) error {
	c, err := recordedCopy(st, name, r.Agent, from)
	if err != nil {
		return err
	}
	c.Path = to
	st.Workspaces[name].Replica(r.Agent).Path = r.Path
	return nil
}

func setCopyURL(name, agent, path, url string) reconcile.RunFunc {
	return func(ctx context.Context, env *reconcile.Env) (reconcile.Outcome, error) {
		if have, err := gitops.Git(ctx, path, "remote", "get-url", "origin"); err != nil || have != url {
			if err := gitops.SetOriginURL(ctx, path, url); err != nil {
				return reconcile.Outcome{}, err
			}
		}
		return reconcile.Outcome{Record: func(st *state.State) error {
			c, err := recordedCopy(st, name, agent, path)
			if err == nil {
				c.URL = url
			}
			return err
		}}, nil
	}
}

func setCopyConfig(name, agent, path, key, value string, unset, recordOnly bool) reconcile.RunFunc {
	return func(ctx context.Context, _ *reconcile.Env) (reconcile.Outcome, error) {
		if !recordOnly {
			if err := reconcile.WriteConfig(ctx, path, key, value, unset); err != nil {
				return reconcile.Outcome{}, err
			}
		}
		return reconcile.Outcome{Record: func(st *state.State) error {
			c, err := recordedCopy(st, name, agent, path)
			if err == nil {
				reconcile.RecordConfig(&c.ManagedConfig, key, value, unset)
			}
			return err
		}}, nil
	}
}

func repointCopy(path, from, to string) reconcile.RunFunc {
	return func(ctx context.Context, _ *reconcile.Env) (reconcile.Outcome, error) {
		return reconcile.Outcome{}, gitops.RepointSource(ctx, path, from, to)
	}
}

func forgetCopy(name, agent, path string) reconcile.RunFunc {
	return func(context.Context, *reconcile.Env) (reconcile.Outcome, error) {
		return reconcile.Outcome{Record: dropCopy(name, agent, path)}, nil
	}
}

// dropCopy forgets a copy, and its replica once it has none left.
func dropCopy(name, agent string, paths ...string) func(*state.State) error {
	return func(st *state.State) error {
		ws, err := recorded(st, name)
		if err != nil {
			return err
		}
		DropCopies(ws, agent, paths...)
		return nil
	}
}

// DropCopies forgets the agent's copies at paths, and its replica once it
// has none left.
func DropCopies(ws *state.WorkspaceState, agent string, paths ...string) {
	r := ws.Replica(agent)
	if r == nil {
		return
	}
	for _, p := range paths {
		r.DropCopy(p)
	}
	if len(r.Copies) == 0 && ws.Isolation != config.IsolationShared {
		ws.DropReplica(agent)
	}
}

// removeCopy archives a copy's unique work and removes it, outside the state
// lock; on any failure the copy stays on disk and in state.
func removeCopy(name, agent, path string) reconcile.RunFunc {
	return func(ctx context.Context, env *reconcile.Env) (reconcile.Outcome, error) {
		ws, err := recorded(env.State, name)
		if err != nil {
			return reconcile.Outcome{}, err
		}
		spec := FromState(nil, env.State, ws)
		for _, l := range spec.Copies() {
			if l.Agent != agent || l.Path != path {
				continue
			}
			archived, err := RemoveCopy(ctx, spec.Isolation, l.Copy, env.ArchiveDir, ArchiveName(l), false)
			if err != nil {
				return reconcile.Outcome{}, err
			}
			return reconcile.Outcome{
				Info:   archivedInfo(archived),
				Record: dropCopy(name, agent, path),
				After: func(context.Context) (string, error) {
					reconcile.RemoveEmptyParents(env, path)
					return "", nil
				},
			}, nil
		}
		return reconcile.Outcome{}, nil
	}
}

func removeWorkspace(name string) reconcile.RunFunc {
	return func(ctx context.Context, env *reconcile.Env) (reconcile.Outcome, error) {
		ws, err := recorded(env.State, name)
		if err != nil {
			return reconcile.Outcome{}, err
		}
		spec := FromState(nil, env.State, ws)
		var infos []string
		var errs []error
		removed := map[string][]string{}
		for _, l := range spec.Copies() {
			archived, err := RemoveCopy(ctx, spec.Isolation, l.Copy, env.ArchiveDir, ArchiveName(l), false)
			if err != nil {
				errs = append(errs, fmt.Errorf("%s kept: %w", l.Label(spec), err))
				continue
			}
			removed[l.Agent] = append(removed[l.Agent], l.Path)
			if archived != "" {
				infos = append(infos, l.Label(spec)+": "+archivedInfo(archived))
			}
		}
		return reconcile.Outcome{
			Info: strings.Join(infos, "; "),
			Record: func(st *state.State) error {
				ws, err := recorded(st, name)
				if err != nil {
					return err
				}
				for agent, paths := range removed {
					DropCopies(ws, agent, paths...)
				}
				if ws.Isolation == config.IsolationShared || len(ws.Replicas) == 0 {
					delete(st.Workspaces, name)
				}
				return nil
			},
			After: func(context.Context) (string, error) {
				for _, paths := range removed {
					for _, p := range paths {
						reconcile.RemoveEmptyParents(env, p)
					}
				}
				return "", nil
			},
		}, errors.Join(errs...)
	}
}

// ArchiveName names the archive of a copy's unique work.
func ArchiveName(l Located) string {
	name := l.Workspace + "-" + l.Agent
	if l.Repo != "" {
		name += "-" + l.Repo[strings.LastIndex(l.Repo, "/")+1:]
	}
	return name
}

func archivedInfo(path string) string {
	if path == "" {
		return ""
	}
	return "unique work archived to " + path
}
