package workspace

import (
	"fmt"
	"net"
	"os"
	"slices"
	"strconv"
	"time"

	"github.com/ashtonian/gitten/internal/config"
	"github.com/ashtonian/gitten/internal/pathutil"
	"github.com/ashtonian/gitten/internal/state"
)

// AgentSettings are what config says of a workspace's agents' commands.
type AgentSettings struct {
	// Env are the env templates, the workspace's over workspace_defaults'.
	Env map[string]string
	// PortBase and PortCount say where agents' port blocks start and how big
	// each is; a count of 0 hands out none.
	PortBase, PortCount int
	// CacheDir is the directory the workspace's agents share for build
	// caches.
	CacheDir string
	// Names are the workspace's agents in order, which numbers them.
	Names []string
}

// maxAgents is how many agents a workspace can have (workspace agents' max).
const maxAgents = 64

// SettingsFor resolves workspace name's agent settings: its declaration over
// workspace_defaults, or the defaults for a workspace made from flags.
func SettingsFor(cfg *config.Config, name string) AgentSettings {
	decl := config.Workspace{Name: name}
	if d := cfg.FindWorkspace(name); d != nil {
		decl = *d
	} else {
		// Made from flags: its agents are named as defaults name them.
		decl.Agents = maxAgents
	}
	w := cfg.WithDefaults(decl)
	base, count := w.PortBlock()
	names, _ := w.AgentNames()
	return AgentSettings{Env: w.Env, PortBase: base, PortCount: count, CacheDir: cfg.WorkspaceCacheDir(name), Names: names}
}

// Update saves a change to the state under its lock, as state.Update and
// reconcile.Env.Update do.
type Update func(change func(*state.State) error) error

// AgentEnv returns the environment agent's commands in workspace ws run
// with: its GITTEN_* variables, then the workspace's env. An agent without
// ports is given its block first, through update, which keeps it for the
// replica's life; the shared cache directory is made.
func AgentEnv(st *state.State, update Update, s AgentSettings, ws, agent string) ([]string, error) {
	vars, err := agentVars(st, s, ws, agent)
	if err != nil {
		return nil, err
	}
	if vars.Port == 0 && s.PortCount > 0 {
		err := update(func(st *state.State) error {
			w := st.Workspaces[ws]
			if w == nil || w.Replica(agent) == nil {
				return fmt.Errorf("%s/%s is no longer recorded", ws, agent)
			}
			r := w.Replica(agent)
			if r.Port == 0 {
				port, err := AllocatePorts(st, s.PortBase, s.PortCount, portFree)
				if err != nil {
					return err
				}
				r.Port, r.Ports = port, s.PortCount
			}
			vars.Port, vars.Ports = r.Port, r.Ports
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("giving %s/%s its ports: %w", ws, agent, err)
		}
	}
	if err := os.MkdirAll(vars.Cache, 0o755); err != nil {
		return nil, fmt.Errorf("making the shared cache dir: %w", err)
	}
	env, err := config.RenderEnv(s.Env, vars)
	if err != nil {
		return nil, fmt.Errorf("workspace %s: %w", ws, err)
	}
	return append(vars.Environ(), env...), nil
}

// RemoveCache deletes a workspace's shared cache directory, if there is one.
func RemoveCache(dir string) error {
	if dir == "" {
		return nil
	}
	if err := pathutil.SafeRemoveAll(dir); err != nil {
		return fmt.Errorf("deleting the workspace's cache %s: %w", dir, err)
	}
	return nil
}

// agentVars is what st records of agent in workspace ws.
func agentVars(st *state.State, s AgentSettings, ws, agent string) (config.AgentVars, error) {
	w := st.Workspaces[ws]
	if w == nil {
		return config.AgentVars{}, fmt.Errorf("workspace %q is not created", ws)
	}
	r := w.Replica(agent)
	if r == nil {
		return config.AgentVars{}, fmt.Errorf("no agent %q in workspace %q", agent, ws)
	}
	return config.AgentVars{
		Workspace: ws, Agent: agent, N: agentNumber(s.Names, w, agent), Replica: r.Path,
		Port: r.Port, Ports: r.Ports, Cache: s.CacheDir,
	}, nil
}

// agentNumber numbers agent from 1: by its place among the names config
// gives, else among the workspace's agents in name order.
func agentNumber(names []string, w *state.WorkspaceState, agent string) int {
	if i := slices.Index(names, agent); i >= 0 {
		return i + 1
	}
	var recorded []string
	for _, r := range w.Replicas {
		recorded = append(recorded, r.Agent)
	}
	slices.Sort(recorded)
	return slices.Index(recorded, agent) + 1
}

// AllocatePorts finds count ports from base up that no agent's block in st
// overlaps and that free says are free, and returns the first.
func AllocatePorts(st *state.State, base, count int, free func(port int) bool) (int, error) {
	type block struct{ start, end int } // end exclusive
	var taken []block
	for _, ws := range st.Workspaces {
		for _, r := range ws.Replicas {
			if r.Port > 0 {
				taken = append(taken, block{r.Port, r.Port + max(r.Ports, 1)})
			}
		}
	}
	for start := base; start+count-1 <= 65535; {
		end := start + count
		if i := slices.IndexFunc(taken, func(b block) bool { return b.start < end && start < b.end }); i >= 0 {
			start = taken[i].end
			continue
		}
		busy := -1
		for p := start; p < end; p++ {
			if !free(p) {
				busy = p
				break
			}
		}
		if busy < 0 {
			return start, nil
		}
		start = busy + 1
	}
	return 0, fmt.Errorf("no %d free ports left from %d", count, base)
}

// portProbe bounds each connection attempt; a closed loopback port refuses
// at once.
const portProbe = 200 * time.Millisecond

// portFree reports whether nothing on this machine listens on port. It
// connects rather than binds: binding would make macOS's firewall ask
// whether gitten may accept incoming connections.
func portFree(port int) bool {
	for _, host := range []string{"127.0.0.1", "::1"} {
		c, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(port)), portProbe)
		if err == nil {
			_ = c.Close()
			return false
		}
	}
	return true
}
