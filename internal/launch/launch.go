package launch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"

	"github.com/ashtonian/git-mgr/internal/config"
)

// Launcher opens a plan's terminals.
type Launcher interface {
	Name() string
	Open(ctx context.Context, p Plan) error
}

// Env is what launchers take from the process they run in.
type Env struct {
	Getenv   func(string) string
	LookPath func(string) (string, error)
	Stdin    io.Reader
	Stdout   io.Writer
	Stderr   io.Writer
	// Confirm asks a yes/no question; false when there is no one to ask.
	Confirm func(question string) bool
	// Exec replaces this process with argv, to hand the terminal over (a
	// tmux attach).
	Exec func(argv []string) error
}

// ProcessEnv is the Env of this process; confirm asks on its terminal.
func ProcessEnv(confirm func(string) bool) Env {
	return Env{
		Getenv: os.Getenv, LookPath: exec.LookPath,
		Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr,
		Confirm: confirm,
		Exec: func(argv []string) error {
			path, err := exec.LookPath(argv[0])
			if err != nil {
				return err
			}
			return syscall.Exec(path, argv, os.Environ())
		},
	}
}

// PluginPrefix starts the name of a launcher plugin's executable.
const PluginPrefix = "git-mgr-launch-"

// ErrNoLauncher reports that auto found no launcher to use.
var ErrNoLauncher = errors.New("no launcher found: install tmux, or set launchers.default to iterm, muxy or a plugin")

// New returns the named launcher: tmux, iterm, muxy, auto, or a plugin.
func New(name string, cfg config.LaunchersConfig, env Env) (Launcher, error) {
	if name == "" {
		name = cfg.Default
	}
	switch name {
	case "auto", "":
		return auto(cfg, env)
	case "tmux":
		return &Tmux{Opts: cfg.Tmux, Env: env}, nil
	case "iterm":
		return &ITerm{Opts: cfg.ITerm, Env: env}, nil
	case "muxy":
		return &Muxy{Opts: cfg.Muxy, Env: env}, nil
	}
	path, err := env.LookPath(PluginPrefix + name)
	if err != nil {
		return nil, fmt.Errorf("launcher %q: no %s%s on PATH", name, PluginPrefix, name)
	}
	return &Plugin{Launcher: name, Path: path, Options: cfg.Plugins[name], Env: env}, nil
}

// auto picks the launcher for the terminal git-mgr runs in: tmux inside
// tmux, Muxy inside Muxy, iTerm inside iTerm, else tmux when installed.
func auto(cfg config.LaunchersConfig, env Env) (Launcher, error) {
	switch {
	case env.Getenv("TMUX") != "":
		return New("tmux", cfg, env)
	case env.Getenv("MUXY_SESSION_ID") != "":
		return New("muxy", cfg, env)
	case env.Getenv("TERM_PROGRAM") == "iTerm.app" || env.Getenv("LC_TERMINAL") == "iTerm2":
		return New("iterm", cfg, env)
	}
	if _, err := env.LookPath("tmux"); err == nil {
		return New("tmux", cfg, env)
	}
	return nil, ErrNoLauncher
}
