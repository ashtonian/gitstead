package workspace

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ashtonian/git-mgr/internal/state"
)

func TestAllocatePorts(t *testing.T) {
	st := &state.State{Workspaces: map[string]*state.WorkspaceState{
		"a": {Name: "a", Replicas: []state.ReplicaState{{Agent: "agent-1", Port: 41000, Ports: 10}, {Agent: "agent-2", Port: 41010, Ports: 10}}},
		"b": {Name: "b", Replicas: []state.ReplicaState{{Agent: "agent-1", Port: 41025, Ports: 5}}},
	}}
	all := func(int) bool { return true }
	// 41020-41024 fits nothing of 10 before b's block at 41025; 41030 is next.
	if got, err := AllocatePorts(st, 41000, 10, all); err != nil || got != 41030 {
		t.Errorf("AllocatePorts = %d, %v; want 41030", got, err)
	}
	if got, _ := AllocatePorts(st, 41000, 5, all); got != 41020 {
		t.Errorf("a block of 5 fits at 41020, got %d", got)
	}
	// A port something else listens on is skipped, and the block after it used.
	busy := func(p int) bool { return p != 41033 }
	if got, _ := AllocatePorts(st, 41000, 10, busy); got != 41034 {
		t.Errorf("past a busy port: %d, want 41034", got)
	}
	if _, err := AllocatePorts(st, 65530, 10, all); err == nil {
		t.Error("no room past 65535 must be an error")
	}
}

func TestAgentEnv(t *testing.T) {
	dir := t.TempDir()
	st, err := state.Load(filepath.Join(dir, "state.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	st.Workspaces = map[string]*state.WorkspaceState{"app": {Name: "app", Replicas: []state.ReplicaState{
		{Agent: "agent-2", Path: "/w/app/agent-2"}, {Agent: "agent-1", Path: "/w/app/agent-1"},
	}}}
	if err := st.Save(); err != nil {
		t.Fatal(err)
	}
	update := func(change func(*state.State) error) error { return st.Update(time.Second, change) }
	s := AgentSettings{
		Env:      map[string]string{"PORT": "{{.Port}}", "DB": "app_{{.N}}"},
		PortBase: 41000, PortCount: 10,
		CacheDir: filepath.Join(dir, "cache", "app"),
		Names:    []string{"agent-1", "agent-2"},
	}
	env, err := AgentEnv(st, update, s, "app", "agent-2")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(env, "\n")
	for _, want := range []string{"GIT_MGR_AGENT=agent-2", "GIT_MGR_AGENT_N=2", "DB=app_2", "GIT_MGR_REPLICA=/w/app/agent-2", "GIT_MGR_CACHE=" + s.CacheDir} {
		if !slices.Contains(env, want) {
			t.Errorf("missing %s in\n%s", want, joined)
		}
	}
	port := st.Workspaces["app"].Replica("agent-2").Port
	if port < 41000 || !slices.Contains(env, "PORT="+strconv.Itoa(port)) || !slices.Contains(env, "GIT_MGR_PORT="+strconv.Itoa(port)) {
		t.Errorf("port %d not given or not told:\n%s", port, joined)
	}
	if _, err := os.Stat(s.CacheDir); err != nil {
		t.Errorf("the cache dir wasn't made: %v", err)
	}

	// The agent keeps its block; the next agent gets another.
	again, _ := AgentEnv(st, update, s, "app", "agent-2")
	if !slices.Contains(again, "GIT_MGR_PORT="+strconv.Itoa(port)) {
		t.Errorf("the port changed:\n%s", strings.Join(again, "\n"))
	}
	if _, err := AgentEnv(st, update, s, "app", "agent-1"); err != nil {
		t.Fatal(err)
	}
	if other := st.Workspaces["app"].Replica("agent-1").Port; other == port || other < port+10 && other > port-10 {
		t.Errorf("agent-1's block %d overlaps agent-2's %d", other, port)
	}
	reloaded, _ := state.Load(filepath.Join(dir, "state.yaml"))
	if reloaded.Workspaces["app"].Replica("agent-2").Port != port {
		t.Error("the port must be saved")
	}
}
