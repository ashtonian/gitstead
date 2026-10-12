package config

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Fleet opens several workspaces side by side with one launcher.
type Fleet struct {
	Name       string   `koanf:"name"       yaml:"name"       validate:"required"`
	Workspaces []string `koanf:"workspaces" yaml:"workspaces" validate:"required,min=1"`
	// Agent is the profile every agent of the fleet runs; empty for each
	// workspace's own.
	Agent string `koanf:"agent" yaml:"agent,omitempty"`
	// Launcher opens the terminals: tmux, iterm, muxy, auto, or a plugin's
	// name; empty for launchers.default.
	Launcher string `koanf:"launcher" yaml:"launcher,omitempty"`
	// Host is the SSH destination the workspaces live on; empty for this
	// machine. git-mgr there makes the launch plan.
	Host string `koanf:"host" yaml:"host,omitempty"`
	// RemoteCommand runs git-mgr on Host, with any flags it needs there.
	RemoteCommand string `koanf:"remote_command" yaml:"remote_command,omitempty"`
}

// LaunchersConfig is how fleets and `ws open` open terminals.
type LaunchersConfig struct {
	// Default is the launcher used when a fleet or `ws open` names none:
	// auto picks tmux inside tmux, iTerm inside iTerm, else tmux.
	Default string `koanf:"default" yaml:"default" default:"auto"`
	// Title names each agent's terminal ({{.Fleet}}, {{.Workspace}},
	// {{.Agent}}, {{.Profile}}).
	Title string `koanf:"title" yaml:"title" default:"{{.Workspace}}/{{.Agent}}"`
	// Check makes each terminal check its agent's copies first.
	Check bool `koanf:"check" yaml:"check" default:"true"`
	// KeepShell leaves a shell in the replica when the agent exits.
	KeepShell bool                      `koanf:"keep_shell" yaml:"keep_shell" default:"true"`
	Tmux      TmuxConfig                `koanf:"tmux"       yaml:"tmux"`
	ITerm     ITermConfig               `koanf:"iterm"      yaml:"iterm"`
	Muxy      MuxyConfig                `koanf:"muxy"       yaml:"muxy"`
	Plugins   map[string]map[string]any `koanf:"plugins"    yaml:"plugins,omitempty"`
}

// TmuxConfig is the tmux launcher's: a session per fleet, a window per
// workspace (or agent), a pane per agent.
type TmuxConfig struct {
	Session   string `koanf:"session"    yaml:"session"    default:"git-mgr-{{.Fleet}}"`
	WindowPer string `koanf:"window_per" yaml:"window_per" default:"workspace" validate:"oneof=workspace agent"`
	Layout    string `koanf:"layout"     yaml:"layout"     default:"tiled" validate:"oneof=tiled even-horizontal even-vertical main-vertical main-horizontal"`
	// MaxPanes caps a window's panes; more agents spill into another window.
	MaxPanes   int  `koanf:"max_panes"   yaml:"max_panes"   default:"4" validate:"min=1,max=64"`
	PaneTitles bool `koanf:"pane_titles" yaml:"pane_titles" default:"true"`
	// ControlMode attaches with tmux -CC, as native iTerm windows: auto does
	// inside iTerm.
	ControlMode string `koanf:"control_mode" yaml:"control_mode" default:"auto" validate:"oneof=auto always never"`
	// Socket runs on a separate tmux server (tmux -L).
	Socket string `koanf:"socket" yaml:"socket,omitempty"`
	// OnExists is what opening a fleet whose session exists does: attach to
	// it, add the agents missing from it, or recreate it.
	OnExists     string `koanf:"on_exists"      yaml:"on_exists"      default:"add-missing" validate:"oneof=attach add-missing recreate"`
	RemainOnExit bool   `koanf:"remain_on_exit" yaml:"remain_on_exit" default:"true"`
}

// ITermConfig is the iTerm launcher's: a window per fleet, a tab per
// workspace (or agent), a split per agent.
type ITermConfig struct {
	Window   string `koanf:"window"    yaml:"window"    default:"new"       validate:"oneof=new current"`
	TabPer   string `koanf:"tab_per"   yaml:"tab_per"   default:"workspace" validate:"oneof=workspace agent"`
	Split    string `koanf:"split"     yaml:"split"     default:"grid"      validate:"oneof=grid columns rows"`
	MaxPanes int    `koanf:"max_panes" yaml:"max_panes" default:"4"         validate:"min=1,max=64"`
	Profile  string `koanf:"profile"   yaml:"profile"   default:"Default"`
	// Badge is each pane's watermark ({{.Workspace}}, {{.Agent}}, ...);
	// empty turns it off.
	Badge string `koanf:"badge" yaml:"badge" default:"{{.Agent}}"`
	Focus bool   `koanf:"focus" yaml:"focus" default:"true"`
	// TypeDelay is the pause before typing into a new split, while its shell
	// starts.
	TypeDelay time.Duration `koanf:"type_delay" yaml:"type_delay" default:"150ms" validate:"min=0"`
}

// MuxyConfig is the Muxy launcher's: a project per workspace, a tab per
// agent, on this machine or a Muxy server over SSH.
type MuxyConfig struct {
	// Host is the SSH destination of the Muxy server; empty for the fleet's
	// host, or this machine.
	Host        string `koanf:"host"         yaml:"host,omitempty"`
	ProjectName string `koanf:"project_name" yaml:"project_name" default:"{{.Workspace}}"`
	// Open is how agents' tabs appear: layout writes a layout to apply with
	// Apply Layout; sessions starts them as server sessions, listed under
	// Existing Terminals; state adds them as tabs to the desktop app's saved
	// state, restarting it.
	Open string `koanf:"open" yaml:"open" default:"layout" validate:"oneof=layout sessions state"`
	// LayoutName names the layout file; empty for the agent profile's name.
	LayoutName string `koanf:"layout_name" yaml:"layout_name,omitempty"`
	// Restart is whether open: state may quit and relaunch the desktop app:
	// ask, always or never.
	Restart string `koanf:"restart" yaml:"restart" default:"ask" validate:"oneof=ask always never"`
	// Color is each project's color: auto picks one per workspace, none
	// leaves it, or #RRGGBB.
	Color string `koanf:"color" yaml:"color" default:"auto"`
	// PruneProjects removes the projects it made for workspaces no longer
	// in the fleet.
	PruneProjects bool `koanf:"prune_projects" yaml:"prune_projects,omitempty"`
	// Command is the muxy CLI; empty for muxy on PATH, else the app's.
	Command string `koanf:"command" yaml:"command,omitempty"`
}

// BuiltinLaunchers are the launchers git-mgr has itself.
var BuiltinLaunchers = []string{"auto", "tmux", "iterm", "muxy"}

// FindFleet returns the named fleet, or nil.
func (c *Config) FindFleet(name string) *Fleet {
	for i := range c.Fleets {
		if c.Fleets[i].Name == name {
			return &c.Fleets[i]
		}
	}
	return nil
}

var colorRE = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)

// validateFleets checks fleets and launchers.
func validateFleets(cfg *Config) error {
	l := cfg.Launchers
	for name := range l.Plugins {
		if !validName(name) || strings.Contains(name, ".") || slices.Contains(BuiltinLaunchers, name) {
			return fmt.Errorf("launchers.plugins: %q can't name a plugin: use letters, digits, '-' and '_', and not a built-in launcher's name", name)
		}
	}
	known := func(launcher string) bool {
		_, plugin := l.Plugins[launcher]
		return launcher == "" || slices.Contains(BuiltinLaunchers, launcher) || plugin || validName(launcher)
	}
	if !known(l.Default) {
		return fmt.Errorf("launchers.default %q is not a launcher", l.Default)
	}
	if c := l.Muxy.Color; c != "auto" && c != "none" && !colorRE.MatchString(c) {
		return fmt.Errorf("launchers.muxy.color %q: use auto, none or #RRGGBB", c)
	}
	for _, tmpl := range []struct{ key, value string }{
		{"launchers.title", l.Title}, {"launchers.tmux.session", l.Tmux.Session},
		{"launchers.iterm.badge", l.ITerm.Badge}, {"launchers.muxy.project_name", l.Muxy.ProjectName},
	} {
		if _, err := RenderLaunch(tmpl.value, LaunchNames{Fleet: "f", Workspace: "w", Agent: "a", Profile: "p"}); err != nil {
			return fmt.Errorf("%s: %w", tmpl.key, err)
		}
	}

	names := map[string]bool{}
	for _, f := range cfg.Fleets {
		if !validName(f.Name) || strings.Contains(f.Name, ".") {
			return fmt.Errorf("fleet %q: a name is letters, digits, '-' and '_', starting with a letter or digit", f.Name)
		}
		if names[f.Name] {
			return fmt.Errorf("duplicate fleet name: %q", f.Name)
		}
		names[f.Name] = true
		seen := map[string]bool{}
		for _, w := range f.Workspaces {
			if seen[w] {
				return fmt.Errorf("fleet %q lists workspace %q twice", f.Name, w)
			}
			seen[w] = true
			if f.Host == "" && cfg.FindWorkspace(w) == nil && !validName(w) {
				return fmt.Errorf("fleet %q: %q is not a workspace name", f.Name, w)
			}
		}
		if !known(f.Launcher) {
			return fmt.Errorf("fleet %q: launcher %q is not a launcher", f.Name, f.Launcher)
		}
		if f.Agent != "" && f.Host == "" && len(cfg.Agents) > 0 && cfg.FindAgent(f.Agent) == nil {
			return fmt.Errorf("fleet %q: unknown agent %q (declare it under agents:)", f.Name, f.Agent)
		}
	}
	return nil
}

// LaunchNames fill in a launcher's templates.
type LaunchNames struct {
	Fleet, Workspace, Agent, Profile string
}

// RenderLaunch renders a launcher template.
func RenderLaunch(tmpl string, names LaunchNames) (string, error) {
	return render("template", tmpl, names)
}
