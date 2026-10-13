package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/ashtonian/git-mgr/internal/activity"
	agents "github.com/ashtonian/git-mgr/internal/agent"
	"github.com/ashtonian/git-mgr/internal/config"
	"github.com/ashtonian/git-mgr/internal/notify"
	"github.com/ashtonian/git-mgr/internal/shellutil"
	"github.com/ashtonian/git-mgr/internal/state"
)

// WSEnvCmd prints an agent's environment -- its GIT_MGR_* variables, ports
// included, and its workspace's env -- for a shell or editor of your own in
// its replica: eval "$(git-mgr ws env)".
type WSEnvCmd struct {
	Name  string `arg:"" optional:"" help:"Workspace name; default the one this directory is in." predictor:"workspace"`
	Agent string `arg:"" optional:"" help:"The agent; default the one whose folder this directory is in."`
}

func (c *WSEnvCmd) Run(ctx *Context) error {
	cfg, err := ctx.LoadConfig()
	if err != nil {
		return err
	}
	st, err := ctx.LoadState()
	if err != nil {
		return err
	}
	name, agent := c.Name, c.Agent
	if agent == "" {
		here, err := os.Getwd()
		if err != nil {
			return err
		}
		ws, a := replicaAt(st, here)
		switch {
		case a == "":
			return errors.New("this directory isn't in an agent's folder; name the workspace and agent")
		case name != "" && ws != name:
			return fmt.Errorf("this directory is in workspace %q, not %q; name the agent", ws, name)
		}
		name, agent = ws, a
	}
	env, err := agentEnv(cfg, st, name, agent)
	if err != nil {
		return err
	}
	vars := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		vars[k] = v
	}
	if ctx.JSON {
		return ctx.Emit(vars)
	}
	for _, k := range slices.Sorted(maps.Keys(vars)) {
		fmt.Printf("export %s=%s\n", k, shellutil.Quote(vars[k]))
	}
	return nil
}

// replicaAt finds the agent whose folder holds dir: its workspace and name,
// or "" for none. The deepest folder wins, so a shared folder inside
// another's doesn't hide it.
func replicaAt(st *state.State, dir string) (ws, agent string) {
	dir = resolvedPath(dir)
	best := -1
	for _, w := range st.Workspaces {
		for _, r := range w.Replicas {
			p := resolvedPath(r.Path)
			if (dir == p || strings.HasPrefix(dir, p+string(filepath.Separator))) && len(p) > best {
				ws, agent, best = w.Name, r.Agent, len(p)
			}
		}
	}
	return ws, agent
}

func resolvedPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

// WSWaitingCmd tells git-mgr an agent is waiting for you: what an agent's own
// notification hook runs. It records it, for `git-mgr top`, and notifies
// under notify.on_waiting. It never fails the agent's hook: problems are
// only printed.
type WSWaitingCmd struct {
	Workspace string `help:"The agent's workspace; default $GIT_MGR_WORKSPACE, which 'ws run' sets." predictor:"workspace"`
	Agent     string `help:"The agent; default $GIT_MGR_AGENT."`
	Message   string `help:"What it is waiting for."`
	Clear     bool   `help:"It is working again: for a hook run when you answer it."`
	Payload   string `arg:"" optional:"" help:"The agent's notification as JSON, as Codex passes it; Claude Code's comes on stdin."`
}

func (c *WSWaitingCmd) Run(ctx *Context) error {
	ws, agent := orDefault(c.Workspace, os.Getenv("GIT_MGR_WORKSPACE")), orDefault(c.Agent, os.Getenv("GIT_MGR_AGENT"))
	doc := map[string]any{"workspace": ws, "agent": agent, "recorded": false}
	if ws == "" || agent == "" {
		return ctx.finish(doc, nil) // not an agent git-mgr runs
	}
	if err := c.note(ctx, ws, agent); err != nil {
		fmt.Fprintf(os.Stderr, "git-mgr: %s/%s waiting: %v\n", ws, agent, err)
		return ctx.finish(doc, nil)
	}
	doc["recorded"] = true
	return ctx.finish(doc, nil)
}

func (c *WSWaitingCmd) note(ctx *Context, ws, agent string) error {
	cfg, err := ctx.LoadConfig()
	if err != nil {
		return err
	}
	st, err := ctx.LoadState()
	if err != nil {
		return err
	}
	if c.Clear {
		return clearWaiting(cfg, st, ws, agent)
	}
	what := c.Message
	if what == "" {
		payload := []byte(c.Payload)
		if len(payload) == 0 && !term.IsTerminal(int(os.Stdin.Fd())) {
			payload = readStdin(os.Stdin, 1<<20, 2*time.Second)
		}
		what = waitingMessage(payload)
	}
	return noteWaiting(cfg, st, ws, agent, orDefault(what, "waiting for you"), false)
}

// readStdin reads up to limit bytes of r, giving up after wait: a hook's
// input is small and closed at once, but nothing must hang an agent.
func readStdin(r io.Reader, limit int64, wait time.Duration) []byte {
	got := make(chan []byte, 1)
	go func() {
		data, _ := io.ReadAll(io.LimitReader(r, limit))
		got <- data
	}()
	select {
	case data := <-got:
		return data
	case <-time.After(wait):
		return nil
	}
}

// waitingMessage is what an agent's notification says it waits for: Claude
// Code's hook input ({"message": ...} from a Notification hook;
// {"hook_event_name": "Stop", "last_assistant_message": ...} when it has
// finished) or Codex's notify payload ({"type": "agent-turn-complete",
// "last-assistant-message": ...}).
func waitingMessage(payload []byte) string {
	var n struct {
		Message    string `json:"message"`
		Event      string `json:"hook_event_name"`
		Type       string `json:"type"`
		ClaudeLast string `json:"last_assistant_message"`
		CodexLast  string `json:"last-assistant-message"`
	}
	if len(payload) == 0 || json.Unmarshal(payload, &n) != nil {
		return ""
	}
	switch {
	case n.Message != "":
		return n.Message
	case n.Event == "Stop" || n.Type == "agent-turn-complete":
		line, _, _ := strings.Cut(strings.TrimSpace(n.ClaudeLast+n.CodexLast), "\n")
		if r := []rune(line); len(r) > 100 {
			line = string(r[:99]) + "…"
		}
		if line != "" {
			return "finished: " + line
		}
		return "finished"
	}
	return ""
}

// waitingBurst is how soon an agent's second report of a wait is taken as
// the same wait: Claude Code can report one through two hooks.
const waitingBurst = 10 * time.Second

// noteWaiting records that agent's running run in workspace ws is waiting
// for you, for what, and notifies under notify.on_waiting. A watched pane's
// report is dropped while an earlier one stands; an agent's own replaces it,
// unless it repeats one within waitingBurst.
func noteWaiting(cfg *config.Config, st *state.State, ws, agent, what string, watched bool) error {
	notifyNow := false
	err := st.Update(hookLockTimeout(cfg), func(st *state.State) error {
		run := runningRun(st, ws, agent)
		if run == nil {
			return nil
		}
		if !run.Waiting.IsZero() && run.Waiting.After(run.Started) && (watched || time.Since(run.Waiting) < waitingBurst) {
			return nil
		}
		run.Waiting, run.WaitingFor = time.Now(), what
		notifyNow = true
		return nil
	})
	if err == nil && notifyNow && cfg.Notify.Enabled && cfg.Notify.OnWaiting {
		notify.Send(context.Background(), cfg.Notify.Timeout, "git-mgr", fmt.Sprintf("%s/%s is waiting: %s", ws, agent, what))
	}
	return err
}

// clearWaiting records that agent's run is working again.
func clearWaiting(cfg *config.Config, st *state.State, ws, agent string) error {
	return st.Update(hookLockTimeout(cfg), func(st *state.State) error {
		if run := runningRun(st, ws, agent); run != nil {
			run.Waiting, run.WaitingFor = time.Time{}, ""
		}
		return nil
	})
}

// runningRun is the record of agent's run in workspace ws, if it hasn't
// ended.
func runningRun(st *state.State, ws, agent string) *state.RunState {
	w := st.Workspaces[ws]
	if w == nil {
		return nil
	}
	r := w.Replica(agent)
	if r == nil || r.Run == nil || !r.Run.Ended.IsZero() {
		return nil
	}
	return r.Run
}

// hookLockTimeout bounds how long recording a wait waits for the state lock:
// an agent's hook must not stall it.
func hookLockTimeout(cfg *config.Config) time.Duration {
	return min(cfg.LockTimeout, 5*time.Second)
}

// watchPane watches the tmux pane an agent runs in, until ctx ends, and
// records when it goes quiet after working -- it is likely waiting for you
// -- and when it works again. It keeps a state handle of its own, so its
// writes never race the run's.
func watchPane(ctx context.Context, cfg *config.Config, statePath, ws, agent string, h agents.Holder) {
	if h.Launcher != "tmux" || h.Session == "" || cfg.Notify.WaitingAfter <= 0 {
		return
	}
	st, err := state.Load(statePath)
	if err != nil {
		return
	}
	w := &activity.Watch{
		Tracker: activity.NewTracker(), Holder: h,
		Every: 2 * time.Second, Quiet: cfg.Notify.WaitingAfter, Busy: 5 * time.Second,
		Waiting: func(quiet time.Duration) {
			_ = noteWaiting(cfg, st, ws, agent, "its pane has been quiet for "+strings.TrimSuffix(activity.Ago(quiet), " ago"), true)
		},
		Working: func() { _ = clearWaiting(cfg, st, ws, agent) },
	}
	w.Run(ctx)
}
