package cmd

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/ashtonian/git-mgr/internal/config"
	"github.com/ashtonian/git-mgr/internal/gitops"
	"github.com/ashtonian/git-mgr/internal/launch"
	"github.com/ashtonian/git-mgr/internal/reconcile"
	"github.com/ashtonian/git-mgr/internal/shellutil"
	"github.com/ashtonian/git-mgr/internal/state"
	"github.com/ashtonian/git-mgr/internal/workspace"
)

// FleetCmd groups the fleet subcommands.
type FleetCmd struct {
	Open   FleetOpenCmd   `cmd:"" help:"Open a fleet's agents, side by side, with its launcher."`
	Plan   FleetPlanCmd   `cmd:"" help:"Print a fleet's launch plan: what launchers and plugins get."`
	List   FleetListCmd   `cmd:"" name:"ls" help:"List fleets."`
	Status FleetStatusCmd `cmd:"" help:"Show the agents and copies of a fleet's workspaces."`
	Stop   FleetStopCmd   `cmd:"" help:"End the running agents of a fleet's workspaces."`
}

// launchFlags are what opening a fleet or a workspace takes.
type launchFlags struct {
	Launcher string `help:"Launcher to open it with: tmux, iterm, muxy, auto or a plugin (default: the fleet's, else launchers.default)."`
	Agent    string `help:"Agent profile every agent runs (default: each workspace's)."`
	Cmd      string `help:"A command every agent's terminal runs instead of an agent profile."`
	Fresh    bool   `help:"Start every agent afresh, even when its profile can resume a session."`
	NoAttach bool   `name:"no-attach" help:"tmux: build the session without attaching to it."`
	Print    bool   `help:"Print what the launcher would do (tmux commands, iTerm's AppleScript, else the plan) instead of doing it."`
}

// FleetOpenCmd opens a fleet.
type FleetOpenCmd struct {
	Name     string `arg:"" help:"Fleet name." predictor:"fleet"`
	NoEnsure bool   `name:"no-ensure" help:"Don't make the fleet's missing workspaces and copies first."`
	launchFlags
}

func (c *FleetOpenCmd) Run(ctx *Context) error {
	cfg, err := ctx.LoadConfig()
	if err != nil {
		return err
	}
	st, err := ctx.LoadState()
	if err != nil {
		return err
	}
	f := cfg.FindFleet(c.Name)
	if f == nil {
		return fmt.Errorf("no fleet %q in config", c.Name)
	}
	var plan launch.Plan
	if f.Host != "" {
		if plan, err = launch.RemotePlan(ctx.GoCtx(), f.Host, f.RemoteCommand, c.Name, c.planArgs(f)...); err != nil {
			return err
		}
	} else {
		if !c.NoEnsure && !c.Print {
			if err := ensureFleet(ctx, cfg, st, f.Workspaces); err != nil {
				return err
			}
		}
		if plan, err = fleetPlan(ctx, cfg, st, c.Name, f.Workspaces, c.agent(f), c.Cmd, c.Fresh); err != nil {
			return err
		}
	}
	launcher := c.Launcher
	if launcher == "" {
		launcher = f.Launcher
	}
	return openPlan(ctx, cfg, st, plan, launcher, c.launchFlags)
}

func (c *FleetOpenCmd) agent(f *config.Fleet) string {
	if c.Agent != "" {
		return c.Agent
	}
	return f.Agent
}

// planArgs pass the flags that shape the plan to the host's git-mgr.
func (c *FleetOpenCmd) planArgs(f *config.Fleet) []string {
	var args []string
	if a := c.agent(f); a != "" {
		args = append(args, "--agent", a)
	}
	if c.Cmd != "" {
		args = append(args, "--cmd", c.Cmd)
	}
	if c.Fresh {
		args = append(args, "--fresh")
	}
	return args
}

// ensureFleet makes what is missing of the fleet's workspaces declared in
// config, as `ws ensure` does; moves and removals wait for `git-mgr apply`.
func ensureFleet(ctx *Context, cfg *config.Config, st *state.State, names []string) error {
	scope := workspace.Scope{Only: func(n string) bool { return slices.Contains(names, n) }}
	plan := workspace.Compute(ctx.GoCtx(), cfg, st, scope)
	if plan.Empty() {
		return nil
	}
	results := workspace.Apply(ctx.GoCtx(), cfg, reconcileEnv(cfg, st), plan, scope, reconcile.Safe)
	reportApplied(ctx, plan, results, reconcile.Safe)
	return changesOutcome(results, plan)
}

// fleetPlan plans opening workspaces, after checking every account they
// need signing from can sign without a terminal: an agent that can't would
// stall on its first commit.
func fleetPlan(ctx *Context, cfg *config.Config, st *state.State, fleet string, names []string, agent, command string, fresh bool) (launch.Plan, error) {
	var copies []state.CopyState
	for _, name := range names {
		if ws := st.Workspaces[name]; ws != nil {
			for _, r := range ws.Replicas {
				copies = append(copies, r.Copies...)
			}
		}
	}
	if err := checkSigning(ctx, cfg, st, copies); err != nil {
		return launch.Plan{}, err
	}
	self, err := selfInvocation(ctx)
	if err != nil {
		return launch.Plan{}, err
	}
	return launch.Build(cfg, st, launch.Request{
		Fleet: fleet, Workspaces: names, Agent: agent, Command: command, Fresh: fresh, Self: self,
	})
}

// checkSigning makes a test signature for each account with signing.mode
// required that covers a repo copies are of: an agent that can't sign
// without a terminal would stall on its first commit. Every agent launch --
// fleet open, ws open, ws run -- passes through it.
func checkSigning(ctx *Context, cfg *config.Config, st *state.State, copies []state.CopyState) error {
	tested := map[string]bool{}
	var errs []error
	for _, c := range copies {
		repo := st.Repos[c.Repo]
		if repo == nil {
			continue
		}
		a := cfg.AccountFor(c.Repo, repo.DiscoveredBy)
		if a == nil || a.Signing.Mode != "required" || tested[a.Name] {
			continue
		}
		tested[a.Name] = true
		if err := gitops.TestSign(ctx.GoCtx(), reconcile.AccountConfig(a)); err != nil {
			errs = append(errs, fmt.Errorf("account %q requires signing, but a test signature failed, so agents would stall on their first commit: %w", a.Name, err))
		}
	}
	return errors.Join(errs...)
}

// selfInvocation is how a terminal runs this git-mgr, with this run's
// config and state.
func selfInvocation(ctx *Context) (launch.Invocation, error) {
	bin, err := os.Executable()
	if err != nil {
		return launch.Invocation{}, err
	}
	if resolved, err := filepath.EvalSymlinks(bin); err == nil {
		bin = resolved
	}
	abs := func(p string) string {
		if p == "" {
			return ""
		}
		p = config.ExpandHome(p)
		if a, err := filepath.Abs(p); err == nil {
			return a
		}
		return p
	}
	return launch.Invocation{Bin: bin, Config: abs(ctx.Config), State: abs(ctx.State)}, nil
}

// openPlan opens plan with the named launcher and records what it left.
func openPlan(ctx *Context, cfg *config.Config, st *state.State, plan launch.Plan, name string, flags launchFlags) error {
	if plan.Agents() == 0 {
		return errors.New("nothing to open: the workspaces have no agents")
	}
	l, err := launch.New(name, cfg.Launchers, launch.ProcessEnv(confirm))
	if err != nil {
		return err
	}
	if flags.Print {
		if ctx.JSON {
			return ctx.Emit(plan)
		}
		return printLaunch(l, plan)
	}
	var muxy *launch.Muxy
	switch l := l.(type) {
	case *launch.Tmux:
		// Under --json stdout isn't a terminal to attach.
		l.NoAttach = flags.NoAttach || ctx.JSON
	case *launch.Muxy:
		if fs := st.Fleets[plan.Fleet]; fs != nil {
			l.Recorded = fs.MuxyProjects
		}
		muxy = l
	}
	// A tmux attach replaces this process, so record before opening.
	record := func() error {
		return st.Update(cfg.LockTimeout, func(st *state.State) error {
			fs := &state.FleetState{Name: plan.Fleet, Launcher: l.Name(), Opened: time.Now()}
			if muxy != nil {
				fs.MuxyProjects = muxy.Projects
			} else if old := st.Fleets[plan.Fleet]; old != nil {
				fs.MuxyProjects = old.MuxyProjects
			}
			st.Fleets[plan.Fleet] = fs
			return nil
		})
	}
	logEvent(ctx, "fleet_open", plan.Fleet, l.Name())
	opened := func(err error) error {
		return ctx.finish(map[string]any{"fleet": plan.Fleet, "launcher": l.Name(), "agents": plan.Agents()}, err)
	}
	if muxy == nil {
		if err := record(); err != nil {
			return err
		}
		return opened(l.Open(ctx.GoCtx(), plan))
	}
	if err := l.Open(ctx.GoCtx(), plan); err != nil {
		if len(muxy.Projects) > 0 {
			_ = record()
		}
		return err
	}
	return opened(record())
}

// printLaunch prints what the launcher would do.
func printLaunch(l launch.Launcher, plan launch.Plan) error {
	if p, ok := l.(interface {
		Print(launch.Plan) (string, error)
	}); ok {
		out, err := p.Print(plan)
		if err != nil {
			return err
		}
		fmt.Print(out)
		return nil
	}
	return printIndentedJSON(plan)
}

// FleetPlanCmd prints a fleet's launch plan.
type FleetPlanCmd struct {
	Name  string `arg:"" help:"Fleet name." predictor:"fleet"`
	Agent string `help:"Agent profile every agent runs (default: each workspace's)."`
	Cmd   string `help:"A command every agent's terminal runs instead of an agent profile."`
	Fresh bool   `help:"Start every agent afresh."`
}

func (c *FleetPlanCmd) Run(ctx *Context) error {
	cfg, err := ctx.LoadConfig()
	if err != nil {
		return err
	}
	st, err := ctx.LoadState()
	if err != nil {
		return err
	}
	f := cfg.FindFleet(c.Name)
	if f == nil {
		return fmt.Errorf("no fleet %q in config", c.Name)
	}
	agent := c.Agent
	if agent == "" {
		agent = f.Agent
	}
	plan, err := fleetPlan(ctx, cfg, st, c.Name, f.Workspaces, agent, c.Cmd, c.Fresh)
	if err != nil {
		return err
	}
	if ctx.JSON {
		return ctx.Emit(plan)
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "WORKSPACE\tAGENT\tSTATE\tPROFILE\tDIR")
	for _, ws := range plan.Workspaces {
		for _, a := range ws.Agents {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", ws.Name, a.Name, a.State, dashIfEmpty(a.Profile), tildify(a.Dir))
		}
	}
	return tw.Flush()
}

// FleetListCmd lists fleets.
type FleetListCmd struct{}

func (c *FleetListCmd) Run(ctx *Context) error {
	cfg, err := ctx.LoadConfig()
	if err != nil {
		return err
	}
	st, err := ctx.LoadState()
	if err != nil {
		return err
	}
	// A fleet, as fleet ls --json lists it.
	type listed struct {
		Name       string    `json:"name"`
		Workspaces []string  `json:"workspaces"`
		Launcher   string    `json:"launcher"`
		Host       string    `json:"host,omitempty"`
		Running    int       `json:"running"`
		Agents     int       `json:"agents"`
		Opened     time.Time `json:"opened,omitzero"`
		OpenedWith string    `json:"opened_with,omitempty"`
	}
	if ctx.JSON {
		doc := []listed{}
		for _, f := range cfg.Fleets {
			l := listed{Name: f.Name, Workspaces: f.Workspaces, Launcher: orDefault(f.Launcher, cfg.Launchers.Default), Host: f.Host}
			for _, name := range f.Workspaces {
				for _, ar := range agentRuns(st, name) {
					l.Agents++
					if ar.state == workspace.AgentRunning || ar.state == workspace.AgentStopping {
						l.Running++
					}
				}
			}
			if fs := st.Fleets[f.Name]; fs != nil {
				l.Opened, l.OpenedWith = fs.Opened, fs.Launcher
			}
			doc = append(doc, l)
		}
		return ctx.Emit(doc)
	}
	if len(cfg.Fleets) == 0 {
		fmt.Println("No fleets. Declare one under fleets: in config.")
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "FLEET\tWORKSPACES\tLAUNCHER\tHOST\tRUNNING\tLAST OPENED")
	for _, f := range cfg.Fleets {
		launcher := f.Launcher
		if launcher == "" {
			launcher = cfg.Launchers.Default
		}
		running := "-"
		if f.Host == "" {
			n, total := 0, 0
			for _, name := range f.Workspaces {
				for _, ar := range agentRuns(st, name) {
					total++
					if ar.state == workspace.AgentRunning || ar.state == workspace.AgentStopping {
						n++
					}
				}
			}
			running = fmt.Sprintf("%d/%d", n, total)
		}
		opened := "-"
		if fs := st.Fleets[f.Name]; fs != nil {
			opened = fs.Opened.Local().Format(time.DateTime) + " (" + fs.Launcher + ")"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", f.Name, strings.Join(f.Workspaces, ","), launcher, dashIfEmpty(f.Host), running, opened)
	}
	return tw.Flush()
}

// FleetStatusCmd shows a fleet's agents and copies.
type FleetStatusCmd struct {
	Name string `arg:"" help:"Fleet name." predictor:"fleet"`
}

func (c *FleetStatusCmd) Run(ctx *Context) error {
	cfg, err := ctx.LoadConfig()
	if err != nil {
		return err
	}
	f := cfg.FindFleet(c.Name)
	if f == nil {
		return fmt.Errorf("no fleet %q in config", c.Name)
	}
	if f.Host != "" {
		return onHost(ctx, f, "fleet", "status", c.Name)
	}
	if ctx.JSON {
		// Each workspace's copies, by workspace.
		doc := map[string][]copyStatusJSON{}
		var errs []error
		for _, name := range f.Workspaces {
			spec, st, err := operationalSpec(ctx, name)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			doc[name] = statusViews(spec, workspace.Health(ctx.GoCtx(), spec, spec.Targets()), agentRuns(st, name))
		}
		return ctx.finish(doc, errors.Join(errs...))
	}
	for i, name := range f.Workspaces {
		if i > 0 {
			fmt.Println()
		}
		fmt.Printf("== %s\n", name)
		if err := (&WSStatusCmd{Name: name}).Run(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "  %v\n", err)
		}
	}
	return nil
}

// FleetStopCmd ends a fleet's running agents.
type FleetStopCmd struct {
	Name    string        `arg:"" help:"Fleet name." predictor:"fleet"`
	Timeout time.Duration `help:"How long to wait for each run to end." default:"30s"`
	Kill    bool          `help:"Kill a run, and everything it started, that hasn't ended by the timeout."`
}

func (c *FleetStopCmd) Run(ctx *Context) error {
	cfg, err := ctx.LoadConfig()
	if err != nil {
		return err
	}
	f := cfg.FindFleet(c.Name)
	if f == nil {
		return fmt.Errorf("no fleet %q in config", c.Name)
	}
	if f.Host != "" {
		args := []string{"fleet", "stop", c.Name, "--timeout", c.Timeout.String()}
		if c.Kill {
			args = append(args, "--kill")
		}
		return onHost(ctx, f, args...)
	}
	st, err := ctx.LoadState()
	if err != nil {
		return err
	}
	doc := []stopJSON{}
	var errs []error
	for _, name := range f.Workspaces {
		stopped, err := stopAgents(ctx, cfg, st, name, "", c.Timeout, c.Kill)
		doc = append(doc, stopped...)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
		}
	}
	return ctx.finish(doc, errors.Join(errs...))
}

// onHost runs git-mgr with args on the fleet's host, on this terminal; under
// --json, without one, its JSON passed through as this command's.
func onHost(ctx *Context, f *config.Fleet, args ...string) error {
	remote := f.RemoteCommand
	if remote == "" {
		remote = "git-mgr"
	}
	if ctx.JSON {
		args = append(args, "--json")
	}
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = shellutil.Quote(a)
	}
	sshArgs := []string{"-t", f.Host, remote + " " + strings.Join(quoted, " ")}
	if !ctx.JSON {
		cmd := exec.Command("ssh", sshArgs...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		return cmd.Run()
	}
	// A terminal would mix the remote's stderr into its JSON.
	var out bytes.Buffer
	cmd := exec.Command("ssh", sshArgs[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, &out, os.Stderr
	err := cmd.Run()
	if out.Len() == 0 {
		return err // the run concludes with the error as its document
	}
	return errors.Join(err, ctx.relay(out.Bytes(), f.Host))
}
