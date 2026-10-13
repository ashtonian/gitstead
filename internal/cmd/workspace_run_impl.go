package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"
	"syscall"
	"time"

	agents "github.com/ashtonian/git-mgr/internal/agent"
	"github.com/ashtonian/git-mgr/internal/config"
	"github.com/ashtonian/git-mgr/internal/state"
	"github.com/ashtonian/git-mgr/internal/workspace"
)

// WSRunCmd runs an agent in its replica, in this terminal: what each pane a
// launcher opens runs. It holds the agent's lease while the agent runs,
// records the run, and then leaves a shell in the replica.
type WSRunCmd struct {
	Name    string `arg:"" help:"Workspace name." predictor:"workspace"`
	Agent   string `arg:"" help:"The agent whose replica to run in."`
	Profile string `help:"Agent profile to run (default: the workspace's agent, else the first)."`
	Command string `help:"Run this command instead of an agent profile."`
	Fresh   bool   `help:"Start afresh even when the profile can resume a session."`
	NoCheck bool   `name:"no-check" help:"Skip checking the agent's copies first."`
	NoShell bool   `name:"no-shell" help:"Exit when the agent does instead of leaving a shell."`

	// env is the agent's environment, which the shell left behind keeps.
	env []string
}

func (c *WSRunCmd) Run(ctx *Context) error {
	if ctx.JSON {
		return errNoJSON("ws run, which gives the agent this terminal,")
	}
	err := c.run(ctx)
	if c.NoShell {
		return err
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "git-mgr: %v\n", err)
	}
	return execShell(c.dir(ctx), c.env)
}

func (c *WSRunCmd) run(ctx *Context) error {
	cfg, err := ctx.LoadConfig()
	if err != nil {
		return err
	}
	st, err := ctx.LoadState()
	if err != nil {
		return err
	}
	ws := st.Workspaces[c.Name]
	if ws == nil {
		return fmt.Errorf("workspace %q is not created; run 'git-mgr ws create %s' first", c.Name, c.Name)
	}
	r := ws.Replica(c.Agent)
	if r == nil {
		return fmt.Errorf("no agent %q in workspace %q", c.Agent, c.Name)
	}
	if !c.NoCheck && ws.Isolation != config.IsolationShared {
		spec := workspace.FromState(cfg, st, ws)
		for _, h := range workspace.Health(ctx.GoCtx(), spec, workspace.OfAgent(spec.Targets(), c.Agent)) {
			if len(h.Issues) > 0 {
				fmt.Printf("git-mgr: %s: %s\n", h.Label(spec), strings.Join(h.Issues, "; "))
			}
		}
	}
	if _, err := os.Stat(r.Path); err != nil {
		return fmt.Errorf("%s/%s's folder %s is missing; 'git-mgr ws ensure %s' makes it again", c.Name, c.Agent, r.Path, c.Name)
	}

	profile, command, err := c.command(cfg, r)
	if err != nil {
		return err
	}
	vars, err := agentEnv(cfg, st, c.Name, c.Agent)
	if err != nil {
		return err
	}
	c.env = vars
	// The profile's env comes first: the workspace's says what this agent's
	// is, such as its port.
	env := append(envList(profile.Env), vars...)
	now := time.Now()
	launcher, session, socket := terminal()
	run := state.RunState{
		Profile: profile.Name, Command: command, PID: os.Getpid(), Started: now,
		Launcher: launcher, Session: session, Socket: socket,
	}
	lease, err := agents.Acquire(agents.LeasePath(st.Dir(), c.Name, c.Agent), agents.Holder{
		PID: run.PID, Profile: run.Profile, Command: command, Launcher: launcher, Session: session, Socket: socket, Started: now,
	})
	if err != nil {
		return fmt.Errorf("%s/%s: %w", c.Name, c.Agent, err)
	}
	defer lease.Release()
	if err := c.record(cfg, st, func(rs *state.ReplicaState) { rs.Run = &run }); err != nil {
		return err
	}

	// Not ctx.GoCtx(): a Ctrl-C is the agent's, and mustn't end the watch.
	watchCtx, stopWatch := context.WithCancel(context.Background())
	go watchPane(watchCtx, cfg, ctx.StatePath(), c.Name, c.Agent, agents.Holder{Launcher: launcher, Session: session, Socket: socket})
	code, runErr := agents.Run(agents.RunOptions{Dir: r.Path, Command: command, Env: env})
	stopWatch()
	err = c.record(cfg, st, func(rs *state.ReplicaState) {
		if rs.Run != nil && rs.Run.PID == run.PID && rs.Run.Started.Equal(run.Started) {
			rs.Run.Ended, rs.Run.ExitCode = time.Now(), code
		}
	})
	switch {
	case runErr != nil:
		return runErr
	case err != nil:
		return err
	case code != 0:
		return &exitError{code, fmt.Errorf("%s exited with status %d", command, code)}
	}
	return nil
}

// command is the profile and the line the run types.
func (c *WSRunCmd) command(cfg *config.Config, r *state.ReplicaState) (config.AgentProfile, string, error) {
	if c.Command != "" {
		return config.AgentProfile{Command: c.Command}, c.Command, nil
	}
	name := c.Profile
	if name == "" {
		if w := cfg.FindWorkspace(c.Name); w != nil {
			name = cfg.WithDefaults(*w).Agent
		} else {
			name = cfg.WorkspaceDefaults.Agent
		}
	}
	p, err := agents.Profile(cfg, name)
	if err != nil {
		return p, "", err
	}
	return p, agents.CommandFor(p, r.Run != nil, c.Fresh), nil
}

// record changes the agent's replica record under the state lock.
func (c *WSRunCmd) record(cfg *config.Config, st *state.State, change func(*state.ReplicaState)) error {
	return st.Update(cfg.LockTimeout, func(st *state.State) error {
		if ws := st.Workspaces[c.Name]; ws != nil {
			if r := ws.Replica(c.Agent); r != nil {
				change(r)
			}
		}
		return nil
	})
}

// dir is where the shell left behind starts: the replica, if it exists.
func (c *WSRunCmd) dir(ctx *Context) string {
	st, err := ctx.LoadState()
	if err != nil {
		return ""
	}
	if ws := st.Workspaces[c.Name]; ws != nil {
		if r := ws.Replica(c.Agent); r != nil {
			if _, err := os.Stat(r.Path); err == nil {
				return r.Path
			}
		}
	}
	return ""
}

// execShell replaces this process with an interactive shell in dir (when
// it isn't ""), with env added.
func execShell(dir string, env []string) error {
	if dir != "" {
		if err := os.Chdir(dir); err != nil {
			return err
		}
	}
	shell := agents.Shell()
	return syscall.Exec(shell, []string{shell}, append(config.Environ(), env...))
}

// terminal is what this process runs in, for the run's record: the launcher
// that opened it (as it said, else as the terminal shows), the terminal
// session, and for tmux the server's socket, which `git-mgr top` reads the
// pane's activity from.
func terminal() (launcher, session, socket string) {
	launcher, session = os.Getenv("GIT_MGR_LAUNCHER"), os.Getenv("GIT_MGR_SESSION")
	switch {
	case os.Getenv("TMUX_PANE") != "":
		launcher = orDefault(launcher, "tmux")
		session = orDefault(session, os.Getenv("TMUX_PANE"))
		// $TMUX is the socket, the server's pid and the session, comma-separated.
		socket, _, _ = strings.Cut(os.Getenv("TMUX"), ",")
	case os.Getenv("MUXY_SESSION_ID") != "":
		launcher = orDefault(launcher, "muxy")
		session = orDefault(session, os.Getenv("MUXY_SESSION_ID"))
	case os.Getenv("ITERM_SESSION_ID") != "":
		launcher = orDefault(launcher, "iterm")
		session = orDefault(session, os.Getenv("ITERM_SESSION_ID"))
	default:
		session = orDefault(session, os.Getenv("TERM_SESSION_ID"))
	}
	return launcher, session, socket
}

func envList(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	return out
}

// WSStopCmd ends agents' runs.
type WSStopCmd struct {
	Name    string        `arg:"" help:"Workspace name." predictor:"workspace"`
	Agent   string        `arg:"" optional:"" help:"The agent to stop; every running one if omitted."`
	Timeout time.Duration `help:"How long to wait for each run to end." default:"30s"`
	Kill    bool          `help:"Kill a run, and everything it started, that hasn't ended by the timeout."`
}

func (c *WSStopCmd) Run(ctx *Context) error {
	cfg, err := ctx.LoadConfig()
	if err != nil {
		return err
	}
	st, err := ctx.LoadState()
	if err != nil {
		return err
	}
	doc, err := stopAgents(ctx, cfg, st, c.Name, c.Agent, c.Timeout, c.Kill)
	return ctx.finish(doc, err)
}

// stopAgents ends workspace name's running agents -- only agent's, when it
// isn't "" -- waiting up to timeout for each, and with kill killing what is
// left.
func stopAgents(ctx *Context, cfg *config.Config, st *state.State, name, agent string, timeout time.Duration, kill bool) ([]stopJSON, error) {
	ws := st.Workspaces[name]
	if ws == nil {
		return nil, fmt.Errorf("workspace %q is not created", name)
	}
	var names []string
	for _, r := range ws.Replicas {
		if agent == "" || r.Agent == agent {
			names = append(names, r.Agent)
		}
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("no agent %q in workspace %q", agent, name)
	}

	failed, tried := 0, 0
	doc := []stopJSON{}
	for _, a := range names {
		path := agents.LeasePath(st.Dir(), name, a)
		if _, held := agents.Read(path); !held {
			if agent != "" && !ctx.Quiet {
				fmt.Printf("  %s: not running\n", a)
			}
			continue
		}
		tried++
		err := st.Update(cfg.LockTimeout, func(st *state.State) error {
			if r := st.Workspaces[name].Replica(a); r != nil && r.Run != nil {
				r.Run.StopRequested = time.Now()
			}
			return nil
		})
		if err != nil {
			return doc, err
		}
		ended, err := agents.Stop(path, timeout, kill)
		res := stopJSON{Workspace: name, Agent: a, Stopped: err == nil && ended, Error: errString(err)}
		switch {
		case err != nil:
			failed++
			fmt.Fprintf(os.Stderr, "  %s: %v\n", a, err)
		case !ended:
			failed++
			res.Error = "still running after " + timeout.String()
			fmt.Fprintf(os.Stderr, "  %s: still running after %s; --kill ends it and everything it started\n", a, timeout)
		default:
			if !ctx.Quiet {
				fmt.Printf("  %s: stopped\n", a)
			}
		}
		doc = append(doc, res)
	}
	if tried == 0 && !ctx.Quiet {
		fmt.Printf("No agent of %q is running.\n", name)
	}
	return doc, outcome(failed, tried, "agents of %q are still running", name)
}

// stopJSON is what stopping one agent's run did.
type stopJSON struct {
	Workspace string `json:"workspace"`
	Agent     string `json:"agent"`
	Stopped   bool   `json:"stopped"`
	Error     string `json:"error,omitempty"`
}
