package cmd

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ashtonian/gitten/internal/config"
	"github.com/ashtonian/gitten/internal/state"
)

func TestWaitingMessage(t *testing.T) {
	long := ""
	for range 30 {
		long += "word "
	}
	for payload, want := range map[string]string{
		`{"hook_event_name":"Notification","message":"Claude needs your permission to use Bash"}`: "Claude needs your permission to use Bash",
		`{"hook_event_name":"Stop","stop_hook_active":false}`:                                     "finished",
		`{"hook_event_name":"Stop","last_assistant_message":"Refactor done.\nDetails."}`:          "finished: Refactor done.",
		`{"type":"agent-turn-complete","last-assistant-message":"Done: tests pass.\nMore."}`:      "finished: Done: tests pass.",
		`{"type":"agent-turn-complete","last-assistant-message":"` + long + `"}`:                  "finished: " + long[:99] + "…",
		`not json`: "",
		``:         "",
	} {
		if got := waitingMessage([]byte(payload)); got != want {
			t.Errorf("waitingMessage(%s) = %q, want %q", payload, got, want)
		}
	}
}

func TestReplicaAt(t *testing.T) {
	root := t.TempDir()
	shared := filepath.Join(root, "proj")
	inner := filepath.Join(root, "ws", "app", "agent-1")
	for _, d := range []string{filepath.Join(inner, "src"), shared} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	st := &state.State{Workspaces: map[string]*state.WorkspaceState{
		"app":  {Name: "app", Replicas: []state.ReplicaState{{Agent: "agent-1", Path: inner}}},
		"wide": {Name: "wide", Replicas: []state.ReplicaState{{Agent: "a", Path: root}}},
	}}
	if ws, agent := replicaAt(st, filepath.Join(inner, "src")); ws != "app" || agent != "agent-1" {
		t.Errorf("the deepest folder wins: %s/%s", ws, agent)
	}
	if ws, _ := replicaAt(st, shared); ws != "wide" {
		t.Errorf("got %s", ws)
	}
	if ws, _ := replicaAt(st, t.TempDir()); ws != "" {
		t.Errorf("outside every folder: %s", ws)
	}
}

// A watched pane's report doesn't replace the agent's own; the agent's own
// replaces a pane's, and clearing ends the wait.
func TestNoteWaiting(t *testing.T) {
	st, err := state.Load(filepath.Join(t.TempDir(), "state.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	st.Workspaces = map[string]*state.WorkspaceState{"app": {Name: "app", Replicas: []state.ReplicaState{
		{Agent: "agent-1", Run: &state.RunState{Command: "claude", Started: time.Now().Add(-time.Hour)}},
	}}}
	if err := st.Save(); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{LockTimeout: time.Second}
	run := func() *state.RunState { return st.Workspaces["app"].Replica("agent-1").Run }

	if err := noteWaiting(cfg, st, "app", "agent-1", "its pane has been quiet for 30s", true); err != nil {
		t.Fatal(err)
	}
	first := run().Waiting
	if first.IsZero() {
		t.Fatal("a watched pane's wait wasn't recorded")
	}
	_ = noteWaiting(cfg, st, "app", "agent-1", "quiet again", true)
	if run().WaitingFor != "its pane has been quiet for 30s" {
		t.Errorf("a second watched report replaced the first: %q", run().WaitingFor)
	}
	run().Waiting = time.Now().Add(-time.Minute)
	if err := st.Save(); err != nil {
		t.Fatal(err)
	}
	_ = noteWaiting(cfg, st, "app", "agent-1", "Claude needs your permission to use Bash", false)
	if run().WaitingFor != "Claude needs your permission to use Bash" {
		t.Errorf("the agent's own report should replace the pane's: %q", run().WaitingFor)
	}
	if err := clearWaiting(cfg, st, "app", "agent-1"); err != nil || !run().Waiting.IsZero() {
		t.Errorf("clear: %v, waiting %v", err, run().Waiting)
	}
	run().Ended = time.Now()
	_ = st.Save()
	_ = noteWaiting(cfg, st, "app", "agent-1", "late", false)
	if !run().Waiting.IsZero() {
		t.Error("a run that ended can't be waiting")
	}
}
