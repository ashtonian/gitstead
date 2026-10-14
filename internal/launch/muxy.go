package launch

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/ashtonian/gitstead/internal/config"
)

// muxyApp is the desktop app's CLI, used when muxy isn't on PATH.
const muxyApp = "/Applications/Muxy.app/Contents/MacOS/muxy"

// muxyColors are the project colors color: auto picks from, by workspace.
var muxyColors = []string{"#4F8EF7", "#34C759", "#FF9F0A", "#BF5AF2", "#FF375F", "#64D2FF", "#FFD60A", "#AC8E68"}

// Muxy opens a fleet in Muxy: a project per workspace and a tab per agent,
// on this machine or on a Muxy server over SSH. Muxy has no command that
// opens tabs in the desktop app, so Opts.Open picks how agents appear: a
// layout to apply, server sessions, or tabs added to the app's saved state.
type Muxy struct {
	Opts config.MuxyConfig
	Env  Env
	// Recorded maps each workspace to the project made for it by an earlier
	// open, for PruneProjects; Projects is the same after Open, to record.
	Recorded map[string]string
	Projects map[string]string
	// cli runs the muxy CLI and returns its output; nil runs it for real.
	cli func(ctx context.Context, args ...string) ([]byte, error)
	// app controls the desktop app, for open: state; nil uses the real one.
	app desktopApp
}

func (m *Muxy) Name() string { return "muxy" }

func (m *Muxy) Open(ctx context.Context, p Plan) error {
	host := m.Opts.Host
	if host == "" {
		host = p.Host
	}
	muxy := func(args ...string) ([]byte, error) {
		if host != "" {
			args = append([]string{"--host", host}, args...)
		}
		if m.cli != nil {
			return m.cli(ctx, args...)
		}
		return runner{}.output(ctx, nil, m.binary(), args...)
	}

	m.Projects = map[string]string{}
	for i, ws := range p.Workspaces {
		name, err := config.RenderLaunch(m.Opts.ProjectName, config.LaunchNames{Fleet: p.Fleet, Workspace: ws.Name})
		if err != nil {
			return err
		}
		out, err := muxy("project", "add", ws.Dir, "--name", name, "--reuse", "--json")
		if err != nil {
			return err
		}
		var project struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(out, &project); err != nil || project.ID == "" {
			return fmt.Errorf("muxy project add printed %q, not a project", strings.TrimSpace(string(out)))
		}
		m.Projects[ws.Name] = project.ID
		if color := m.color(i); color != "" {
			if _, err := muxy("project", "set-color", project.ID, color); err != nil {
				return err
			}
		}
		fmt.Fprintf(m.Env.Stdout, "%-12s %s  %s\n", ws.Name, project.ID, ws.Dir)
	}
	if m.Opts.PruneProjects {
		for _, ws := range slices.Sorted(maps.Keys(m.Recorded)) {
			id := m.Recorded[ws]
			if _, kept := m.Projects[ws]; kept {
				continue
			}
			if _, err := muxy("project", "delete", id, "--yes"); err != nil {
				return err
			}
			fmt.Fprintf(m.Env.Stdout, "%-12s %s  removed (its files stay)\n", ws, id)
		}
	}

	switch m.Opts.Open {
	case "sessions":
		return m.sessions(p, muxy)
	case "state":
		app := m.app
		if app == nil {
			app = realApp{env: m.Env}
		}
		return m.openState(ctx, p, app, muxy)
	default:
		return m.layouts(p, muxy)
	}
}

func (m *Muxy) binary() string {
	if m.Opts.Command != "" {
		return m.Opts.Command
	}
	if path, err := m.Env.LookPath("muxy"); err == nil {
		return path
	}
	return muxyApp
}

func (m *Muxy) color(i int) string {
	switch m.Opts.Color {
	case "none":
		return ""
	case "auto":
		return muxyColors[i%len(muxyColors)]
	default:
		return m.Opts.Color
	}
}

// layoutName names the layout file: Opts.LayoutName, else the agents'
// profile, else gitstead.
func (m *Muxy) layoutName(p Plan) string {
	if m.Opts.LayoutName != "" {
		return m.Opts.LayoutName
	}
	for _, ws := range p.Workspaces {
		for _, a := range ws.Agents {
			if a.Profile != "" {
				return a.Profile
			}
		}
	}
	return "gitstead"
}

// muxyLayout is a Muxy layout file: what Apply Layout opens.
type muxyLayout struct {
	Tabs []muxyTab `json:"tabs"`
}

type muxyTab struct {
	Name    string `json:"name"`
	Command string `json:"command"`
}

func layoutOf(ws Workspace) muxyLayout {
	var l muxyLayout
	for _, a := range ws.Agents {
		l.Tabs = append(l.Tabs, muxyTab{Name: a.Title, Command: a.Command})
	}
	return l
}

// layouts writes each project's .muxy/layouts/<name>.json, through muxy
// exec so it lands wherever the server is.
func (m *Muxy) layouts(p Plan, muxy func(...string) ([]byte, error)) error {
	name := m.layoutName(p)
	const write = `mkdir -p "$(dirname "$1")" && printf '%s\n' "$2" > "$1.tmp" && mv "$1.tmp" "$1"`
	for _, ws := range p.Workspaces {
		data, err := json.MarshalIndent(layoutOf(ws), "", "  ")
		if err != nil {
			return err
		}
		if _, err := muxy("exec", m.Projects[ws.Name], "--", "/bin/sh", "-c", write, "sh", ".muxy/layouts/"+name+".json", string(data)); err != nil {
			return err
		}
	}
	fmt.Fprintf(m.Env.Stdout, "Wrote layout %q to each project; in Muxy, open the project and choose Apply Layout… to start its agents.\n", name)
	return nil
}

// sessions starts each agent that isn't running as a server session; they
// show under Existing Terminals.
func (m *Muxy) sessions(p Plan, muxy func(...string) ([]byte, error)) error {
	for _, ws := range p.Workspaces {
		for _, a := range ws.Agents {
			if a.State == "running" {
				fmt.Fprintf(m.Env.Stdout, "%-12s %s already running\n", ws.Name, a.Name)
				continue
			}
			// Muxy types the command, joined with spaces, so it goes as one word.
			out, err := muxy("session", "create", m.Projects[ws.Name], "--directory", a.Dir, "--", a.Command)
			if err != nil {
				return err
			}
			fmt.Fprintf(m.Env.Stdout, "%-12s %s session %s\n", ws.Name, a.Name, strings.TrimSpace(string(out)))
		}
	}
	fmt.Fprintln(m.Env.Stdout, "Agents started as Muxy sessions; open them from Existing Terminals.")
	return nil
}
