package cmd

import (
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
}

func (c *WSRunCmd) Run(ctx *Context) error {
	err := c.run(ctx)
	if c.NoShell {
		return err
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "git-mgr: %v\n", err)
	}
	return execShell(c.dir(ctx), nil)
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
	now := time.Now()
	run := state.RunState{
		Profile: profile.Name, Command: command, PID: os.Getpid(), Started: now,
		Launcher: os.Getenv("GIT_MGR_LAUNCHER"), Session: sessionID(),
	}
	lease, err := agents.Acquire(agents.LeasePath(st.Dir(), c.Name, c.Agent), agents.Holder{
		PID: run.PID, Profile: run.Profile, Command: command, Launcher: run.Launcher, Session: run.Session, Started: now,
	})
	if err != nil {
		return fmt.Errorf("%s/%s: %w", c.Name, c.Agent, err)
	}
	defer lease.Release()
	if err := c.record(cfg, st, func(rs *state.ReplicaState) { rs.Run = &run }); err != nil {
		return err
	}

	env := append(envList(profile.Env),
		"GIT_MGR_WORKSPACE="+c.Name, "GIT_MGR_AGENT="+c.Agent, "GIT_MGR_REPLICA="+r.Path)
	code, runErr := agents.Run(agents.RunOptions{Dir: r.Path, Command: command, Env: env})
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
	return syscall.Exec(shell, []string{shell}, append(os.Environ(), env...))
}

// sessionID names the terminal session this process runs in, for the run's
// record: what a launcher set, else what the terminal set.
func sessionID() string {
	for _, k := range []string{"GIT_MGR_SESSION", "TMUX_PANE", "ITERM_SESSION_ID", "TERM_SESSION_ID"} {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
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
	ws := st.Workspaces[c.Name]
	if ws == nil {
		return fmt.Errorf("workspace %q is not created", c.Name)
	}
	var names []string
	for _, r := range ws.Replicas {
		if c.Agent == "" || r.Agent == c.Agent {
			names = append(names, r.Agent)
		}
	}
	if len(names) == 0 {
		return fmt.Errorf("no agent %q in workspace %q", c.Agent, c.Name)
	}

	failed, tried := 0, 0
	for _, name := range names {
		path := agents.LeasePath(st.Dir(), c.Name, name)
		if _, held := agents.Read(path); !held {
			if c.Agent != "" && !ctx.Quiet {
				fmt.Printf("  %s: not running\n", name)
			}
			continue
		}
		tried++
		err := st.Update(cfg.LockTimeout, func(st *state.State) error {
			if r := st.Workspaces[c.Name].Replica(name); r != nil && r.Run != nil {
				r.Run.StopRequested = time.Now()
			}
			return nil
		})
		if err != nil {
			return err
		}
		ended, err := agents.Stop(path, c.Timeout, c.Kill)
		switch {
		case err != nil:
			failed++
			fmt.Fprintf(os.Stderr, "  %s: %v\n", name, err)
		case !ended:
			failed++
			fmt.Fprintf(os.Stderr, "  %s: still running after %s; --kill ends it and everything it started\n", name, c.Timeout)
		default:
			if !ctx.Quiet {
				fmt.Printf("  %s: stopped\n", name)
			}
		}
	}
	if tried == 0 && !ctx.Quiet {
		fmt.Printf("No agent of %q is running.\n", c.Name)
	}
	if failed > 0 {
		return outcome(failed, tried, "agents of %q are still running", c.Name)
	}
	return nil
}
