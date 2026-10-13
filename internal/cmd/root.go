package cmd

import (
	"context"
	"log/slog"
	"os"

	"github.com/ashtonian/git-mgr/internal/config"
	"github.com/ashtonian/git-mgr/internal/state"
	"github.com/willabides/kongplete"
)

// CLI defines the top-level command structure.
type CLI struct {
	// Global flags
	Config  string `help:"Config file path." default:"~/.config/git-mgr/config.yaml" env:"GIT_MGR_CONFIG" type:"path"`
	State   string `help:"State file path." default:"~/.local/share/git-mgr/state.yaml" env:"GIT_MGR_STATE" type:"path"`
	Verbose bool   `help:"Verbose output." short:"v"`
	Quiet   bool   `help:"Suppress non-error output." short:"q"`
	Jobs    int    `help:"Max concurrent git operations (default 8)." short:"j" name:"concurrency"`

	// Commands
	Version    VersionCmd    `cmd:"" help:"Print version information."`
	Setup      SetupCmd      `cmd:"" help:"Set up git-mgr, or change your setup: accounts, layout, what to track, features."`
	Init       InitCmd       `cmd:"" help:"Create a config file with sensible defaults, without asking."`
	Bootstrap  BootstrapCmd  `cmd:"" help:"Set up a fresh machine: init, doctor, discover, sync, install agent."`
	Discover   DiscoverCmd   `cmd:"" help:"Discover repos from configured providers."`
	Sync       SyncCmd       `cmd:"" help:"Fetch all tracked repos and backup uncommitted changes."`
	Plan       PlanCmd       `cmd:"" help:"Show what applying config would change, without changing anything."`
	Apply      ApplyCmd      `cmd:"" help:"Make repos and workspaces match config, including moves and removals."`
	Backup     BackupCmd     `cmd:"" help:"Backup uncommitted changes."`
	Restore    RestoreCmd    `cmd:"" help:"Restore a backup (print steps, apply, or --into a scratch dir)."`
	Status     StatusCmd     `cmd:"" help:"Show state of all managed repos."`
	Top        TopCmd        `cmd:"" help:"A live view of every repo and workspace copy: working trees, agents, activity."`
	Doctor     DoctorCmd     `cmd:"" help:"Diagnose tooling, auth, config, and repo health."`
	Run        RunCmd        `cmd:"" help:"Run a command in each matching repo (after --)."`
	Clone      CloneCmd      `cmd:"" help:"Manually clone and track a repo."`
	Adopt      AdoptCmd      `cmd:"" help:"Import existing local repos into management."`
	Remove     RemoveCmd     `cmd:"" help:"Stop tracking repos."`
	Manifest   ManifestCmd   `cmd:"" help:"Export/import a portable manifest of the tracked repo set."`
	RemoteSync RemoteSyncCmd `cmd:"" name:"remote-sync" help:"Back up and push WIP and unpushed commits offsite now (remote_sync); --list shows what is there."`
	ConfigShow ConfigCmd     `cmd:"" name:"config" help:"Print resolved configuration."`
	Report     ReportCmd     `cmd:"" help:"Summarize recent activity from the operation log."`
	Log        LogCmd        `cmd:"" help:"Print recent operation-log events."`
	Workspace  WorkspaceCmd  `cmd:"" aliases:"ws,wg" help:"Manage workspaces: repos worked on by several agents, each in a copy of its own."`
	Fleet      FleetCmd      `cmd:"" help:"Open fleets: several workspaces' agents side by side, with one launcher."`
	Archives   ArchivesCmd   `cmd:"" help:"The catalog of what was saved before copies and repos were taken away, and restoring it."`
	Hooks      HooksCmd      `cmd:"" help:"Run post_clone or post_create hooks again."`
	Mirror     MirrorCmd     `cmd:"" help:"Make or update full mirrors of repos' upstreams, for an offsite copy (config 'mirror:')."`
	Path       PathCmd       `cmd:"" help:"Print the path of a repo, workspace or agent's folder from part of its name."`
	Cd         CdCmd         `cmd:"" help:"Change directory to a repo, workspace or agent's folder (needs shell-init)."`
	Open       OpenCmd       `cmd:"" help:"Open a repo, workspace or agent's folder in your editor; --web opens the repo's page."`
	ShellInit  ShellInitCmd  `cmd:"" name:"shell-init" help:"Print the shell function that makes git-mgr cd change directory."`

	InstallAgent       InstallAgentCmd              `cmd:"" name:"install-agent" help:"Install/remove a periodic sync agent (launchd/systemd/cron)."`
	InstallCompletions kongplete.InstallCompletions `cmd:"" name:"install-completions" help:"Output a shell completion script (bash/zsh/fish)."`
}

// Context is passed to all command Run methods.
type Context struct {
	Config     string
	State      string
	Verbose    bool
	Quiet      bool
	Jobs       int
	VersionStr string

	// Ctx is the root context for the command, cancelled on SIGINT/SIGTERM so
	// in-flight git and API operations stop cleanly. Use GoCtx() to read it
	// (it falls back to context.Background() when unset, e.g. in tests).
	Ctx context.Context

	// Lazily loaded
	cfg    *config.Config
	st     *state.State
	logger *slog.Logger
}

// GoCtx returns the root context.Context for git/API operations, defaulting to
// context.Background() when no signal-aware context was injected.
func (c *Context) GoCtx() context.Context {
	if c.Ctx != nil {
		return c.Ctx
	}
	return context.Background()
}

// LoadConfig loads and validates the config file.
func (c *Context) LoadConfig() (*config.Config, error) {
	if c.cfg != nil {
		return c.cfg, nil
	}
	var opts []config.Option
	if config.IsDefaultPath(c.Config) {
		opts = append(opts, config.WithOptionalFile())
	}
	cfg, err := config.Load(c.Config, opts...)
	if err != nil {
		return nil, err
	}
	if c.Jobs > 0 {
		cfg.Concurrency = c.Jobs
	}
	c.cfg = cfg
	return cfg, nil
}

// ReloadConfig loads the config file again, after a command changed it.
func (c *Context) ReloadConfig() (*config.Config, error) {
	c.cfg = nil
	return c.LoadConfig()
}

// LoadState loads the state file, creating it if it doesn't exist.
func (c *Context) LoadState() (*state.State, error) {
	if c.st != nil {
		return c.st, nil
	}
	st, err := state.Load(c.State)
	if err != nil {
		return nil, err
	}
	c.st = st
	return st, nil
}

// Logger returns a configured slog.Logger.
func (c *Context) Logger() *slog.Logger {
	if c.logger != nil {
		return c.logger
	}
	level := slog.LevelInfo
	if c.Verbose {
		level = slog.LevelDebug
	}
	if c.Quiet {
		level = slog.LevelError
	}
	c.logger = slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	return c.logger
}
