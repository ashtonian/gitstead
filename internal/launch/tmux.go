package launch

import (
	"context"
	"fmt"
	"strings"

	"github.com/ashtonian/gitten/internal/config"
	"github.com/ashtonian/gitten/internal/shellutil"
)

// agentOption marks each pane with the agent it runs, as workspace/agent, so
// opening a fleet again finds what is already open.
const agentOption = "@gitten-agent"

// Tmux opens a fleet as a tmux session: a window per workspace (or per
// agent), a pane per agent, spilling into another window past MaxPanes.
type Tmux struct {
	Opts config.TmuxConfig
	Env  Env
	// NoAttach builds the session without attaching to it.
	NoAttach bool
	// run runs tmux with args and returns its output; nil runs it for real.
	run func(ctx context.Context, host string, args ...string) (string, error)
}

func (t *Tmux) Name() string { return "tmux" }

// Open builds the plan's session, or under OnExists attaches to it, adds the
// agents missing from it or rebuilds it, then attaches.
func (t *Tmux) Open(ctx context.Context, p Plan) error {
	session, err := t.session(p)
	if err != nil {
		return err
	}
	tmux := func(args ...string) (string, error) { return t.tmux(ctx, p.Host, args...) }
	_, err = tmux("has-session", "-t", "="+session)
	exists := err == nil
	if exists && t.Opts.OnExists == "recreate" {
		if _, err := tmux("kill-session", "-t", "="+session); err != nil {
			return err
		}
		exists = false
	}
	if !exists || t.Opts.OnExists != "attach" {
		if err := t.build(tmux, session, exists, p); err != nil {
			return err
		}
	}
	return t.attach(ctx, p.Host, session)
}

// Print is the shell script that builds the plan's session from scratch and
// attaches to it.
func (t *Tmux) Print(p Plan) (string, error) {
	session, err := t.session(p)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	windows, panes := 0, 0
	dry := *t
	dry.run = func(_ context.Context, host string, args ...string) (string, error) {
		line := remoteLine("tmux", args...)
		if host != "" {
			line = "ssh " + shellutil.Word(host) + " " + shellutil.Word(line)
		}
		b.WriteString(line + "\n")
		sub := args[0]
		if sub == "-L" && len(args) > 2 {
			sub = args[2]
		}
		switch sub {
		case "new-session", "new-window":
			windows++
			panes++
		case "split-window":
			panes++
		default:
			return "", nil
		}
		return fmt.Sprintf("@%d\t%%%d", windows, panes), nil
	}
	if err := dry.build(func(args ...string) (string, error) { return dry.tmux(context.Background(), p.Host, args...) }, session, false, p); err != nil {
		return "", err
	}
	attach := []string{"attach-session", "-t", "=" + session}
	if t.Opts.Socket != "" {
		attach = append([]string{"-L", t.Opts.Socket}, attach...)
	}
	argv := runner{host: p.Host}.argv("tmux", attach...)
	b.WriteString(strings.Join(argv, " ") + "\n")
	return b.String(), nil
}

func (t *Tmux) session(p Plan) (string, error) {
	name, err := config.RenderLaunch(t.Opts.Session, config.LaunchNames{Fleet: p.Fleet})
	if err != nil {
		return "", err
	}
	// tmux reads '.' and ':' in a target as window and pane separators.
	return strings.NewReplacer(".", "-", ":", "-").Replace(name), nil
}

func (t *Tmux) tmux(ctx context.Context, host string, args ...string) (string, error) {
	if t.Opts.Socket != "" {
		args = append([]string{"-L", t.Opts.Socket}, args...)
	}
	if t.run != nil {
		return t.run(ctx, host, args...)
	}
	out, err := runner{host: host}.output(ctx, nil, "tmux", args...)
	return strings.TrimSpace(string(out)), err
}

type tmuxWindow struct {
	id    string
	panes int
}

// build adds a pane for each agent the session doesn't have yet, creating
// the session first when it doesn't exist.
func (t *Tmux) build(tmux func(...string) (string, error), session string, exists bool, p Plan) error {
	present := map[string]bool{}
	windows := map[string]*tmuxWindow{}
	if exists {
		out, err := tmux("list-panes", "-s", "-t", "="+session, "-F", "#{window_id}\t#{window_name}\t#{"+agentOption+"}")
		if err != nil {
			return err
		}
		for _, line := range strings.Split(out, "\n") {
			f := strings.SplitN(line, "\t", 3)
			if len(f) != 3 {
				continue
			}
			w := windows[f[1]]
			if w == nil {
				w = &tmuxWindow{id: f[0]}
				windows[f[1]] = w
			}
			w.panes++
			if f[2] != "" {
				present[f[2]] = true
			}
		}
	}

	touched := map[string]bool{}
	created := exists
	for _, ws := range p.Workspaces {
		for _, a := range ws.Agents {
			key := ws.Name + "/" + a.Name
			if present[key] {
				continue
			}
			name := t.windowFor(windows, ws.Name, a.Name)
			env := []string{"-e", "GITTEN_LAUNCHER=tmux", "-c", a.Dir}
			format := []string{"-P", "-F", "#{window_id}\t#{pane_id}"}
			var args []string
			switch w := windows[name]; {
			case !created:
				args = append(append(append([]string{"new-session", "-d", "-s", session, "-n", name, "-x", "250", "-y", "50"}, env...), format...), a.Command)
			case w == nil:
				args = append(append(append([]string{"new-window", "-t", "=" + session + ":", "-n", name}, env...), format...), a.Command)
			default:
				args = append(append(append([]string{"split-window", "-t", w.id}, env...), format...), a.Command)
			}
			out, err := tmux(args...)
			if err != nil {
				return err
			}
			created = true
			windowID, paneID, ok := strings.Cut(out, "\t")
			if !ok {
				return fmt.Errorf("tmux %s printed %q, not a window and pane", args[0], out)
			}
			if w := windows[name]; w != nil {
				w.panes++
			} else {
				windows[name] = &tmuxWindow{id: windowID, panes: 1}
			}
			touched[windowID] = true
			steps := [][]string{{"set-option", "-p", "-t", paneID, agentOption, key}}
			if t.Opts.PaneTitles {
				steps = append(steps, []string{"select-pane", "-t", paneID, "-T", a.Title})
			}
			if t.Opts.RemainOnExit {
				steps = append(steps, []string{"set-option", "-p", "-t", paneID, "remain-on-exit", "on"})
			}
			// Each split shrinks the panes; laying out as we go keeps room for
			// the next.
			steps = append(steps, []string{"select-layout", "-t", windowID, t.Opts.Layout})
			for _, s := range steps {
				if _, err := tmux(s...); err != nil {
					return err
				}
			}
		}
	}
	for id := range touched {
		if !t.Opts.PaneTitles {
			continue
		}
		for _, s := range [][]string{
			{"set-option", "-w", "-t", id, "pane-border-status", "top"},
			{"set-option", "-w", "-t", id, "pane-border-format", " #{pane_title} "},
			// Agents set the terminal title; the pane keeps the agent's name.
			{"set-option", "-w", "-t", id, "allow-set-title", "off"},
		} {
			if _, err := tmux(s...); err != nil {
				return err
			}
		}
	}
	return nil
}

// windowFor names the window an agent's pane goes in: its workspace's, or
// that workspace's next one with room; with WindowPer agent, its own.
func (t *Tmux) windowFor(windows map[string]*tmuxWindow, workspace, agent string) string {
	if t.Opts.WindowPer == "agent" {
		return workspace + "/" + agent
	}
	for n := 1; ; n++ {
		name := workspace
		if n > 1 {
			name = fmt.Sprintf("%s-%d", workspace, n)
		}
		if w := windows[name]; w == nil || w.panes < t.Opts.MaxPanes {
			return name
		}
	}
}

// attach hands the terminal to the session: tmux attach (in control mode
// inside iTerm, under ControlMode auto), over ssh for a remote host, or a
// switch of the current client inside tmux.
func (t *Tmux) attach(ctx context.Context, host, session string) error {
	if t.NoAttach {
		fmt.Fprintf(t.Env.Stdout, "Fleet ready in tmux session %q. Attach with: tmux attach -t %s\n", session, session)
		return nil
	}
	if host == "" && t.Opts.Socket == "" && t.Env.Getenv("TMUX") != "" {
		_, err := t.tmux(ctx, host, "switch-client", "-t", "="+session)
		return err
	}
	args := []string{"attach-session", "-t", "=" + session}
	control := t.Opts.ControlMode == "always" ||
		t.Opts.ControlMode == "auto" && (t.Env.Getenv("TERM_PROGRAM") == "iTerm.app" || t.Env.Getenv("LC_TERMINAL") == "iTerm2")
	if control {
		args = append([]string{"-CC"}, args...)
	}
	if t.Opts.Socket != "" {
		args = append([]string{"-L", t.Opts.Socket}, args...)
	}
	return t.Env.Exec(runner{host: host}.argv("tmux", args...))
}
