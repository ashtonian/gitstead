package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"github.com/ashtonian/git-mgr/internal/shellutil"
	"github.com/ashtonian/git-mgr/internal/workgroup"
)

// WGOpenCmd opens (or attaches to) a tmux session with one pane per member,
// each running `wg check` on startup so drift is flagged immediately. Under
// iTerm it uses tmux control mode (-CC) so the panes render as native iTerm
// splits while the underlying tmux session survives disconnects and is rebuilt
// deterministically after a restart.
type WGOpenCmd struct {
	Name     string `arg:"" help:"Workgroup name."`
	Cmd      string `help:"Run this command in each pane before dropping to a shell (e.g. 'claude')."`
	NoCheck  bool   `name:"no-check" help:"Skip the per-pane 'wg check' on startup."`
	Print    bool   `help:"Print the tmux script instead of running it."`
	Recreate bool   `help:"Kill and rebuild the session if it already exists."`
	NoAttach bool   `name:"no-attach" help:"Build the session but do not attach."`
	Plain    bool   `help:"Force a plain tmux attach even under iTerm (no -CC control mode)."`
}

func (c *WGOpenCmd) Run(ctx *Context) error {
	spec, _, err := operationalSpec(ctx, c.Name)
	if err != nil {
		return err
	}
	if len(spec.Members) == 0 {
		return fmt.Errorf("workgroup %q has no members", c.Name)
	}

	selfBin, err := os.Executable()
	if err != nil || selfBin == "" {
		selfBin = "git-mgr"
	}
	session := tmuxSession(c.Name)
	build, attach := c.plan(session, spec, selfBin, ctx.Config, ctx.State)

	if c.Print {
		printTmuxScript(session, build, attach, c.Recreate, c.NoAttach)
		return nil
	}

	tmuxPath, err := exec.LookPath("tmux")
	if err != nil {
		return fmt.Errorf("tmux not found in PATH (install tmux, or use --print to see the script): %w", err)
	}

	exists := exec.Command(tmuxPath, "has-session", "-t", session).Run() == nil
	if exists && c.Recreate {
		_ = exec.Command(tmuxPath, "kill-session", "-t", session).Run()
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
		fmt.Printf("Workgroup %q ready in tmux session %q (%d panes). Attach with: tmux attach -t %s\n",
			c.Name, session, len(spec.Members), session)
		return nil
	}

	// Replace this process with the attach so tmux takes over the terminal.
	argv := append([]string{tmuxPath}, attach...)
	return syscall.Exec(tmuxPath, argv, os.Environ())
}

// plan returns the tmux build steps (argv lists) and the attach argv.
func (c *WGOpenCmd) plan(session string, spec *workgroup.Spec, selfBin, cfgPath, statePath string) (build [][]string, attach []string) {
	pane := func(m workgroup.Member) string {
		return paneCommand(selfBin, cfgPath, statePath, c.Name, m.Name, c.Cmd, !c.NoCheck)
	}

	first := spec.Members[0]
	build = append(build,
		// -x/-y give the detached window room so several split-windows don't
		// fail with "no space for new pane"; it resizes to the client on attach.
		[]string{"new-session", "-d", "-s", session, "-n", "workgroup", "-x", "250", "-y", "50", "-c", first.Path},
		[]string{"send-keys", "-t", session, pane(first), "C-m"},
	)
	for _, m := range spec.Members[1:] {
		build = append(build,
			[]string{"split-window", "-t", session, "-c", m.Path},
			[]string{"select-layout", "-t", session, "tiled"},
			[]string{"send-keys", "-t", session, pane(m), "C-m"},
		)
	}
	build = append(build, []string{"select-layout", "-t", session, "tiled"})

	attach = []string{"attach", "-t", session}
	if c.useITerm() {
		attach = []string{"-CC", "attach", "-t", session}
	}
	return build, attach
}

func (c *WGOpenCmd) useITerm() bool {
	if c.Plain {
		return false
	}
	return os.Getenv("TERM_PROGRAM") == "iTerm.app" || os.Getenv("LC_TERMINAL") == "iTerm2"
}

// paneCommand is the line typed into each pane: an optional one-shot in-sync
// check, then an optional user command (e.g. 'claude' to launch an agent per
// member), then an interactive shell so the pane stays open after the command
// exits. The user command is typed into the pane's interactive shell, so shell
// aliases and functions resolve. Paths are shell-quoted for the pane's shell.
func paneCommand(selfBin, cfgPath, statePath, wgName, member, userCmd string, check bool) string {
	var segs []string
	if check {
		parts := []string{shellutil.Quote(selfBin)}
		if cfgPath != "" {
			parts = append(parts, "--config", shellutil.Quote(cfgPath))
		}
		if statePath != "" {
			parts = append(parts, "--state", shellutil.Quote(statePath))
		}
		parts = append(parts, "wg", "check", shellutil.Quote(wgName), "--member", shellutil.Quote(member))
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
	fmt.Printf("# tmux layout for git-mgr workgroup session %q\n", session)
	if recreate {
		fmt.Printf("tmux kill-session -t %s 2>/dev/null\n", shellutil.Quote(session))
	}
	fmt.Printf("if ! tmux has-session -t %s 2>/dev/null; then\n", shellutil.Quote(session))
	for _, args := range build {
		fmt.Printf("  tmux %s\n", shJoin(args))
	}
	fmt.Println("fi")
	if !noAttach {
		fmt.Printf("tmux %s\n", shJoin(attach))
	}
}

// tmuxSession derives a safe tmux session name (tmux disallows '.' and ':').
func tmuxSession(name string) string {
	return "wg-" + strings.NewReplacer(".", "-", ":", "-", " ", "-").Replace(name)
}

func shJoin(args []string) string {
	q := make([]string, len(args))
	for i, a := range args {
		q[i] = shellutil.Quote(a)
	}
	return strings.Join(q, " ")
}
