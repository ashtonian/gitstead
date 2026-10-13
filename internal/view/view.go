// Package view builds `git-mgr top`'s picture of everything git-mgr manages:
// each repo's working tree, and each workspace copy with its agent's state
// and terminal activity. Collect reads it, live and without the network;
// Render lays it out.
package view

import (
	"context"
	"crypto/sha256"
	"maps"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

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
	AgentState string        `json:"agent_state"`
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
	// Activity reads agents' terminal activity, such as a Tracker's Read,
	// which a refreshing view keeps between looks; nil uses a new Tracker.
	Activity func(ctx context.Context, holders []agents.Holder) map[string]Activity
}

// Activity is what an agent's terminal shows.
type Activity struct {
	Text string
	Idle time.Duration // how long since it printed; 0 when unknown
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
	activity := opts.Activity
	if activity == nil {
		activity = NewTracker().Read
	}
	var acts map[string]Activity
	if len(holders) > 0 {
		acts = activity(ctx, holders)
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

func copyRow(ctx context.Context, st *state.State, spec *workspace.Spec, l workspace.Located, held map[string]agents.Holder, acts map[string]Activity) CopyRow {
	h := workspace.Health(ctx, spec, []workspace.Located{l})[0]
	row := CopyRow{
		Workspace: spec.Name, Agent: l.Agent, Label: l.Label(spec), Path: l.Path, Repo: l.Repo,
		Branch: h.Branch, State: h.State(), Ahead: h.Ahead, Behind: h.Behind, Issues: h.Issues,
	}
	ws := st.Workspaces[spec.Name]
	if r := ws.Replica(l.Agent); r != nil {
		row.AgentState = workspace.AgentState(st.Dir(), spec.Name, r)
		if c := r.CopyAt(l.Path); c != nil {
			row.LastBackup, row.PR = c.LastBackup, c.PR
		}
	}
	if hd, ok := held[spec.Name+"/"+l.Agent]; ok {
		a := acts[activityKey(hd)]
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

// activityKey identifies a run's terminal across launchers.
func activityKey(h agents.Holder) string {
	return h.Launcher + "\x00" + h.Socket + "\x00" + h.Session
}

// activityTimeout bounds each tmux or Muxy query.
const activityTimeout = 5 * time.Second

// Tracker reads agents' terminal activity, remembering what it saw between
// looks. tmux keeps the time of a window's last output, not a pane's, so an
// agent alone in its window gets that time, and one sharing a window gets
// the time its pane's screen was last seen to change: unknown on the first
// look, then exact to the interval between looks. Muxy reports each agent's
// own state.
type Tracker struct {
	mu   sync.Mutex
	seen map[string]paneSeen
	now  func() time.Time
	// tmux and muxy run the commands; nil runs them for real.
	tmux func(ctx context.Context, args ...string) ([]byte, error)
	muxy func(ctx context.Context) ([]byte, error)
}

type paneSeen struct {
	hash    [32]byte
	since   time.Time // when the screen last changed, or was first seen
	changed bool      // a change was seen, so since is when it printed
}

// NewTracker returns a Tracker with nothing seen yet.
func NewTracker() *Tracker {
	return &Tracker{seen: map[string]paneSeen{}, now: time.Now}
}

// Read returns each holder's activity, by activityKey.
func (tr *Tracker) Read(ctx context.Context, holders []agents.Holder) map[string]Activity {
	out := map[string]Activity{}
	panes := map[string][]string{} // socket -> the agents' panes on it
	muxy := false
	for _, h := range holders {
		switch h.Launcher {
		case "tmux":
			panes[h.Socket] = append(panes[h.Socket], h.Session)
		case "muxy":
			muxy = true
		}
	}
	now := tr.now()
	for socket, wanted := range panes {
		base := []string{}
		if socket != "" {
			base = []string{"-S", socket}
		}
		data, err := tr.runTmux(ctx, append(base, "list-panes", "-a", "-F", "#{pane_id}\t#{window_activity}\t#{window_panes}")...)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			f := strings.Split(line, "\t")
			if len(f) != 3 || !slices.Contains(wanted, f[0]) {
				continue
			}
			key := activityKey(agents.Holder{Launcher: "tmux", Socket: socket, Session: f[0]})
			if f[2] == "1" {
				if sec, err := strconv.ParseInt(f[1], 10, 64); err == nil && sec > 0 {
					idle := now.Sub(time.Unix(sec, 0)).Truncate(time.Second)
					out[key] = Activity{Text: "printed " + Ago(idle), Idle: idle}
				}
				continue
			}
			screen, err := tr.runTmux(ctx, append(base, "capture-pane", "-p", "-t", f[0])...)
			if err != nil {
				continue
			}
			out[key] = tr.observe(key, sha256.Sum256(screen), now)
		}
	}
	if muxy {
		run := tr.muxy
		if run == nil {
			run = muxyActivityList
		}
		if data, err := run(ctx); err == nil {
			for session, st := range ParseMuxyActivity(data) {
				out[activityKey(agents.Holder{Launcher: "muxy", Session: session})] = Activity{Text: st}
			}
		}
	}
	return out
}

// observe records a pane's screen and says how long it has been quiet.
func (tr *Tracker) observe(key string, hash [32]byte, now time.Time) Activity {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	prev, ok := tr.seen[key]
	switch {
	case !ok:
		tr.seen[key] = paneSeen{hash: hash, since: now}
		return Activity{}
	case prev.hash != hash:
		tr.seen[key] = paneSeen{hash: hash, since: now, changed: true}
		return Activity{Text: "printed " + Ago(0), Idle: 0}
	}
	idle := now.Sub(prev.since).Truncate(time.Second)
	if prev.changed {
		return Activity{Text: "printed " + Ago(idle), Idle: idle}
	}
	return Activity{Text: "quiet ≥ " + strings.TrimSuffix(Ago(idle), " ago"), Idle: idle}
}

func (tr *Tracker) runTmux(ctx context.Context, args ...string) ([]byte, error) {
	if tr.tmux != nil {
		return tr.tmux(ctx, args...)
	}
	ctx, cancel := context.WithTimeout(ctx, activityTimeout)
	defer cancel()
	return exec.CommandContext(ctx, "tmux", args...).Output()
}

// muxyActivityList runs `muxy activity list`.
func muxyActivityList(ctx context.Context) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, activityTimeout)
	defer cancel()
	bin := "muxy"
	if _, err := exec.LookPath(bin); err != nil {
		bin = "/Applications/Muxy.app/Contents/MacOS/muxy"
	}
	return exec.CommandContext(ctx, bin, "activity", "list").Output()
}

// ParseMuxyActivity reads `muxy activity list`'s text form: tab-separated
// session-id, project-id, provider, state, event-id, event.
func ParseMuxyActivity(data []byte) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Split(line, "\t")
		if len(f) < 4 || f[0] == "" {
			continue
		}
		text := f[3]
		if len(f) >= 6 && f[5] != "" {
			text += ": " + f[5]
		}
		out[f[0]] = text
	}
	return out
}

// Ago says how long ago something was, coarsely: 12s, 4m, 3h, 2d.
func Ago(d time.Duration) string {
	switch {
	case d < 0:
		return "now"
	case d < time.Minute:
		return strconv.Itoa(int(d.Seconds())) + "s ago"
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes())) + "m ago"
	case d < 48*time.Hour:
		return strconv.Itoa(int(d.Hours())) + "h ago"
	default:
		return strconv.Itoa(int(d.Hours()/24)) + "d ago"
	}
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return line
}
