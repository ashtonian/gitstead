// Package view builds `git-mgr top`'s picture of everything git-mgr manages:
// each repo's working tree, and each workspace copy with its agent's state
// and terminal activity. Collect reads it, live and without the network;
// Render lays it out.
package view

import (
	"context"
	"maps"
	"slices"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/ashtonian/git-mgr/internal/activity"
	agents "github.com/ashtonian/git-mgr/internal/agent"
	"github.com/ashtonian/git-mgr/internal/config"
	"github.com/ashtonian/git-mgr/internal/gitops"
	"github.com/ashtonian/git-mgr/internal/state"
	"github.com/ashtonian/git-mgr/internal/workspace"
)

// Snapshot is everything at one moment.
type Snapshot struct {
	Taken  time.Time `json:"taken"`
	Copies []CopyRow `json:"copies"`
	Repos  []RepoRow `json:"repos"`
}

// RepoRow is a tracked repo's working tree, and what the last run found.
type RepoRow struct {
	Key        string    `json:"key"`
	Path       string    `json:"path"`
	State      string    `json:"state"` // clean, dirty, conflict, missing, error
	Branch     string    `json:"branch,omitempty"`
	Upstream   string    `json:"upstream,omitempty"`
	Ahead      int       `json:"ahead"`
	Behind     int       `json:"behind"`
	LastFetch  time.Time `json:"last_fetch,omitzero"`
	LastBackup time.Time `json:"last_backup,omitzero"`
	// Problem is the last run's fetch or backup error.
	Problem string `json:"problem,omitempty"`
}

// Attention reports whether the repo needs a look: not clean, ahead or
// behind, or with an error.
func (r RepoRow) Attention() bool {
	return r.State != "clean" || r.Ahead > 0 || r.Behind > 0 || r.Problem != ""
}

// CopyRow is a workspace copy, its agent, and its working tree.
type CopyRow struct {
	Workspace string `json:"workspace"`
	Agent     string `json:"agent,omitempty"`
	Label     string `json:"label"`
	Path      string `json:"path"`
	Repo      string `json:"repo,omitempty"`
	// AgentState is the agent's lifecycle state; Activity what its terminal
	// shows: when its pane last printed (tmux), or the agent's state (Muxy).
	AgentState string `json:"agent_state"`
	// Waiting is when the running agent began waiting for you, and
	// WaitingFor what for; zero when it isn't.
	Waiting    time.Time     `json:"waiting,omitzero"`
	WaitingFor string        `json:"waiting_for,omitempty"`
	Activity   string        `json:"activity,omitempty"`
	Idle       time.Duration `json:"idle,omitempty"`
	Branch     string        `json:"branch,omitempty"`
	State      string        `json:"state"`
	Compare    string        `json:"compare,omitempty"`
	Ahead      int           `json:"ahead"`
	Behind     int           `json:"behind"`
	LastBackup time.Time     `json:"last_backup,omitzero"`
	PR         string        `json:"pr,omitempty"`
	Issues     []string      `json:"issues,omitempty"`
}

// Options shape a collection.
type Options struct {
	// Repos selects the repos shown; nil shows all.
	Repos       func(key string, r *state.RepoState) bool
	Concurrency int
	Now         func() time.Time
	// Activity reads agents' terminal activity, such as an activity.Tracker's Read,
	// which a refreshing view keeps between looks; nil uses a new Tracker.
	Activity func(ctx context.Context, holders []agents.Holder) map[string]activity.Activity
}

// Collect reads the snapshot: every copy of every workspace and every
// selected repo, inspected at once up to opts.Concurrency.
func Collect(ctx context.Context, cfg *config.Config, st *state.State, opts Options) Snapshot {
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	snap := Snapshot{Taken: now()}

	type copyJob struct {
		spec *workspace.Spec
		loc  workspace.Located
	}
	var jobs []copyJob
	var holders []agents.Holder
	held := map[string]agents.Holder{} // workspace/agent -> its run
	for _, name := range slices.Sorted(maps.Keys(st.Workspaces)) {
		ws := st.Workspaces[name]
		spec := workspace.FromState(cfg, st, ws)
		if spec.Shared() {
			// Every agent works in the one folder: a row each, for their
			// states and activity.
			for _, r := range spec.Replicas {
				jobs = append(jobs, copyJob{spec, workspace.Located{Workspace: name, Agent: r.Agent, Copy: workspace.Copy{Path: r.Path}}})
			}
		} else {
			for _, t := range spec.Copies() {
				jobs = append(jobs, copyJob{spec, t})
			}
		}
		for _, r := range ws.Replicas {
			if h, ok := agents.Read(agents.LeasePath(st.Dir(), name, r.Agent)); ok {
				held[name+"/"+r.Agent] = h
				holders = append(holders, h)
			}
		}
	}
	read := opts.Activity
	if read == nil {
		read = activity.NewTracker().Read
	}
	var acts map[string]activity.Activity
	if len(holders) > 0 {
		acts = read(ctx, holders)
	}

	snap.Copies = make([]CopyRow, len(jobs))
	keys := slices.Sorted(maps.Keys(st.Repos))
	keys = slices.DeleteFunc(keys, func(k string) bool {
		return !st.Repos[k].Cloned || opts.Repos != nil && !opts.Repos(k, st.Repos[k])
	})
	snap.Repos = make([]RepoRow, len(keys))

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(max(opts.Concurrency, 1))
	for i, j := range jobs {
		g.Go(func() error {
			snap.Copies[i] = copyRow(gctx, st, j.spec, j.loc, held, acts)
			return nil
		})
	}
	for i, key := range keys {
		g.Go(func() error {
			snap.Repos[i] = repoRow(gctx, key, st.Repos[key])
			return nil
		})
	}
	_ = g.Wait()
	return snap
}

func copyRow(ctx context.Context, st *state.State, spec *workspace.Spec, l workspace.Located, held map[string]agents.Holder, acts map[string]activity.Activity) CopyRow {
	h := workspace.Health(ctx, spec, []workspace.Located{l})[0]
	row := CopyRow{
		Workspace: spec.Name, Agent: l.Agent, Label: l.Label(spec), Path: l.Path, Repo: l.Repo,
		Branch: h.Branch, State: h.State(), Ahead: h.Ahead, Behind: h.Behind, Issues: h.Issues,
	}
	ws := st.Workspaces[spec.Name]
	if r := ws.Replica(l.Agent); r != nil {
		row.AgentState = workspace.AgentState(st.Dir(), spec.Name, r)
		if row.AgentState == workspace.AgentRunning {
			row.Waiting, row.WaitingFor, _ = workspace.WaitingFor(r)
		}
		if c := r.CopyAt(l.Path); c != nil {
			row.LastBackup, row.PR = c.LastBackup, c.PR
		}
	}
	if hd, ok := held[spec.Name+"/"+l.Agent]; ok {
		a := acts[activity.Key(hd)]
		row.Activity, row.Idle = a.Text, a.Idle
	}
	return row
}

func repoRow(ctx context.Context, key string, r *state.RepoState) RepoRow {
	row := RepoRow{Key: key, Path: r.LocalPath, LastFetch: r.LastFetch, LastBackup: r.LastBackup}
	switch {
	case r.LastFetchError != "":
		row.Problem = "fetch: " + firstLine(r.LastFetchError)
	case r.LastBackupError != "":
		row.Problem = "backup: " + firstLine(r.LastBackupError)
	case r.LastMirrorError != "":
		row.Problem = "mirror: " + firstLine(r.LastMirrorError)
	}
	if !gitops.HasGitDir(r.LocalPath) {
		row.State = "missing"
		return row
	}
	rs, err := gitops.GetStatus(ctx, r.LocalPath)
	if err != nil {
		row.State = "error"
		return row
	}
	row.State, row.Branch = rs.StatusString(), rs.CurrentBranch
	if up := gitops.Upstream(ctx, r.LocalPath); up != "" {
		row.Upstream = up
		if a, b, err := gitops.AheadBehind(ctx, r.LocalPath, up); err == nil {
			row.Ahead, row.Behind = a, b
		}
	}
	return row
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return line
}
