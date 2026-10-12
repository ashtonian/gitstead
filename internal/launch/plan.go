// Package launch opens agents' terminals: it turns a fleet -- workspaces
// opened side by side -- into a launch plan, and a launcher (tmux, iTerm,
// Muxy, or a git-mgr-launch-<name> plugin) turns the plan into terminals,
// each running `git-mgr ws run` for one agent.
package launch

import (
	"fmt"
	"strings"

	agents "github.com/ashtonian/git-mgr/internal/agent"
	"github.com/ashtonian/git-mgr/internal/config"
	"github.com/ashtonian/git-mgr/internal/shellutil"
	"github.com/ashtonian/git-mgr/internal/state"
	"github.com/ashtonian/git-mgr/internal/workspace"
)

// Plan is what a launcher opens. It is also what a plugin receives, as
// JSON on stdin, so its shape is stable.
type Plan struct {
	Fleet string `json:"fleet"`
	// Host is the SSH destination the workspaces live on; "" for this
	// machine. Paths and commands are the host's.
	Host       string      `json:"host,omitempty"`
	Workspaces []Workspace `json:"workspaces"`
	// Options are the plugin's own, from launchers.plugins.<name>.
	Options map[string]any `json:"options,omitempty"`
}

// Workspace is one workspace of a plan.
type Workspace struct {
	Name string `json:"name"`
	// Dir is the workspace's directory: the folder its agents share, or the
	// one their replicas are in.
	Dir    string  `json:"dir"`
	Agents []Agent `json:"agents"`
}

// Agent is one terminal to open.
type Agent struct {
	Name    string `json:"name"`
	Dir     string `json:"dir"` // its replica
	Title   string `json:"title"`
	Profile string `json:"profile,omitempty"`
	// Command is the shell command line the terminal runs: `git-mgr ws run`
	// for the agent, which checks its copies, runs it and leaves a shell.
	Command string `json:"command"`
	// State is the agent's lifecycle state when the plan was made; a
	// running agent's terminal shows that it is already running elsewhere.
	State string `json:"state"`
}

// Invocation is how a terminal runs this git-mgr: its binary and the global
// flags that pick its config and state.
type Invocation struct {
	Bin    string
	Config string
	State  string
}

// Command is the shell command line for git-mgr with args.
func (inv Invocation) Command(args ...string) string {
	parts := []string{shellutil.Word(inv.Bin)}
	if inv.Config != "" {
		parts = append(parts, "--config", shellutil.Word(inv.Config))
	}
	if inv.State != "" {
		parts = append(parts, "--state", shellutil.Word(inv.State))
	}
	for _, a := range args {
		parts = append(parts, shellutil.Word(a))
	}
	return strings.Join(parts, " ")
}

// Request is what to plan.
type Request struct {
	Fleet      string
	Workspaces []string
	// Agent is the profile every agent runs; "" for each workspace's.
	Agent string
	// Command, when set, runs instead of an agent profile.
	Command string
	Fresh   bool
	Self    Invocation
}

// Build plans the request against the recorded workspaces.
func Build(cfg *config.Config, st *state.State, req Request) (Plan, error) {
	p := Plan{Fleet: req.Fleet}
	for _, name := range req.Workspaces {
		ws := st.Workspaces[name]
		if ws == nil {
			return Plan{}, fmt.Errorf("workspace %q is not created; 'git-mgr ws ensure %s' or 'git-mgr apply' makes it", name, name)
		}
		profile := req.Agent
		if profile == "" {
			profile = cfg.WorkspaceDefaults.Agent
			if w := cfg.FindWorkspace(name); w != nil {
				profile = cfg.WithDefaults(*w).Agent
			}
		}
		if req.Command == "" {
			p, err := agents.Profile(cfg, profile)
			if err != nil {
				return Plan{}, fmt.Errorf("workspace %q: %w", name, err)
			}
			profile = p.Name
		} else {
			profile = ""
		}
		pw := Workspace{Name: name, Dir: ws.Dir}
		for i := range ws.Replicas {
			r := &ws.Replicas[i]
			title, err := config.RenderLaunch(cfg.Launchers.Title, config.LaunchNames{Fleet: req.Fleet, Workspace: name, Agent: r.Agent, Profile: profile})
			if err != nil {
				return Plan{}, err
			}
			pw.Agents = append(pw.Agents, Agent{
				Name: r.Agent, Dir: r.Path, Title: title, Profile: profile,
				Command: req.Self.Command(runArgs(cfg, name, r.Agent, req, profile)...),
				State:   workspace.AgentState(st.Dir(), name, r),
			})
		}
		p.Workspaces = append(p.Workspaces, pw)
	}
	return p, nil
}

func runArgs(cfg *config.Config, ws, agent string, req Request, profile string) []string {
	args := []string{"ws", "run", ws, agent}
	switch {
	case req.Command != "":
		args = append(args, "--command", req.Command)
	case profile != "":
		args = append(args, "--profile", profile)
	}
	if req.Fresh {
		args = append(args, "--fresh")
	}
	if !cfg.Launchers.Check {
		args = append(args, "--no-check")
	}
	if !cfg.Launchers.KeepShell {
		args = append(args, "--no-shell")
	}
	return args
}

// Agents counts the plan's agents.
func (p Plan) Agents() int {
	n := 0
	for _, w := range p.Workspaces {
		n += len(w.Agents)
	}
	return n
}
