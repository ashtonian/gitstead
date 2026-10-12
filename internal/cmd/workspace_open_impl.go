package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"github.com/ashtonian/git-mgr/internal/shellutil"
	"github.com/ashtonian/git-mgr/internal/workspace"
)

// WSOpenCmd opens (or attaches to) a tmux session with one pane per agent,
// each running `ws check` on startup so problems show at once. Under iTerm it
// uses tmux control mode (-CC) so the panes render as native iTerm splits
// while the tmux session survives disconnects.
type WSOpenCmd struct {
	Name     string `arg:"" help:"Workspace name." predictor:"workspace"`
	Agent    string `help:"The agent profile each pane runs (config 'agents:')."`
	Cmd      string `help:"A command each pane runs instead of an agent profile."`
	NoCheck  bool   `name:"no-check" help:"Skip each pane's 'ws check' on startup."`
	Print    bool   `help:"Print the tmux script instead of running it."`
	Recreate bool   `help:"Kill and rebuild the session if it already exists."`
	NoAttach bool   `name:"no-attach" help:"Build the session but do not attach."`
	Plain    bool   `help:"Force a plain tmux attach even under iTerm (no -CC control mode)."`
}

func (c *WSOpenCmd) Run(ctx *Context) error {
	if c.Agent != "" && c.Cmd == "" {
		cfg, err := ctx.LoadConfig()
		if err != nil {
			return err
		}
		a := cfg.FindAgent(c.Agent)
		if a == nil {
			return fmt.Errorf("no agent %q in config 'agents:'", c.Agent)
		}
		c.Cmd = a.Command
	}
	spec, _, err := operationalSpec(ctx, c.Name)
	if err != nil {
		return err
	}
	if len(spec.Replicas) == 0 {
		return fmt.Errorf("workspace %q has no agents", c.Name)
	}

	selfBin, err := os.Executable()
	if err != nil || selfBin == "" {
		selfBin = "git-mgr"
	}
	session := "ws-" + c.Name
	build, attach := c.plan(session, spec, selfBin, ctx.Config, ctx.State)
	if c.Print {
		printTmuxScript(session, build, attach, c.Recreate, c.NoAttach)
		return nil
	}

	tmuxPath, err := exec.LookPath("tmux")
	if err != nil {
		return fmt.Errorf("tmux not found in PATH (install tmux, or use --print to see the script): %w", err)
	}
	exists := exec.Command(tmuxPath, "has-session", "-t", "="+session).Run() == nil
	if exists && c.Recreate {
		_ = exec.Command(tmuxPath, "kill-session", "-t", "="+session).Run()
		exists = false
	}
	if !exists {
		for _, args := range build {
			if out, err := exec.Command(tmuxPath, args...).CombinedOutput(); err != nil {
				return fmt.Errorf("tmux %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
			}
		}
	}
	if c.NoAttach {
		fmt.Printf("Workspace %q ready in tmux session %q (%d panes). Attach with: tmux attach -t %s\n",
			c.Name, session, len(spec.Replicas), session)
		return nil
	}
	// Replace this process with the attach so tmux takes over the terminal.
	return syscall.Exec(tmuxPath, append([]string{tmuxPath}, attach...), os.Environ())
}

// plan returns the tmux build steps (argv lists) and the attach argv.
func (c *WSOpenCmd) plan(session string, spec *workspace.Spec, selfBin, cfgPath, statePath string) (build [][]string, attach []string) {
	pane := func(r workspace.Replica) string {
		return paneCommand(selfBin, cfgPath, statePath, spec, r.Agent, c.Cmd, !c.NoCheck)
	}
	target := "=" + session
	first := spec.Replicas[0]
	build = append(build,
		// -x/-y give the detached window room so several split-windows don't
		// fail with "no space for new pane"; it resizes to the client on attach.
		[]string{"new-session", "-d", "-s", session, "-n", spec.Name, "-x", "250", "-y", "50", "-c", first.Path},
		[]string{"send-keys", "-t", target, pane(first), "C-m"},
	)
	for _, r := range spec.Replicas[1:] {
		build = append(build,
			[]string{"split-window", "-t", target, "-c", r.Path},
			[]string{"select-layout", "-t", target, "tiled"},
			[]string{"send-keys", "-t", target, pane(r), "C-m"},
		)
	}
	build = append(build, []string{"select-layout", "-t", target, "tiled"})

	attach = []string{"attach", "-t", target}
	if c.useITerm() {
		attach = []string{"-CC", "attach", "-t", target}
	}
	return build, attach
}

func (c *WSOpenCmd) useITerm() bool {
	if c.Plain {
		return false
	}
	return os.Getenv("TERM_PROGRAM") == "iTerm.app" || os.Getenv("LC_TERMINAL") == "iTerm2"
}

// paneCommand is the line typed into each pane: an optional check of the
// agent's copies, then an optional command (an agent, say), then an
// interactive shell so the pane stays open after the command exits. The
// command is typed into the pane's own shell, so shell functions and aliases
// resolve.
func paneCommand(selfBin, cfgPath, statePath string, spec *workspace.Spec, agent, userCmd string, check bool) string {
	var segs []string
	if check && !spec.Shared() {
		parts := []string{shellutil.Quote(selfBin)}
		if cfgPath != "" {
			parts = append(parts, "--config", shellutil.Quote(cfgPath))
		}
		if statePath != "" {
			parts = append(parts, "--state", shellutil.Quote(statePath))
		}
		parts = append(parts, "ws", "check", shellutil.Quote(spec.Name), "--agent", shellutil.Quote(agent))
		segs = append(segs, strings.Join(parts, " "))
	}
	if userCmd != "" {
		segs = append(segs, userCmd)
	}
	segs = append(segs, "exec $SHELL")
	return strings.Join(segs, "; ")
}

func printTmuxScript(session string, build [][]string, attach []string, recreate, noAttach bool) {
	fmt.Println("#!/bin/sh")
	fmt.Printf("# tmux layout for git-mgr workspace session %q\n", session)
	if recreate {
		fmt.Printf("tmux kill-session -t %s 2>/dev/null\n", shellutil.Quote("="+session))
	}
	fmt.Printf("if ! tmux has-session -t %s 2>/dev/null; then\n", shellutil.Quote("="+session))
	for _, args := range build {
		fmt.Printf("  tmux %s\n", shJoin(args))
	}
	fmt.Println("fi")
	if !noAttach {
		fmt.Printf("tmux %s\n", shJoin(attach))
	}
}

func shJoin(args []string) string {
	q := make([]string, len(args))
	for i, a := range args {
		q[i] = shellutil.Quote(a)
	}
	return strings.Join(q, " ")
}
