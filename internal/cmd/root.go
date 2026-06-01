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
	Init       InitCmd       `cmd:"" help:"Create a config file with sensible defaults."`
	Bootstrap  BootstrapCmd  `cmd:"" help:"Set up a fresh machine: init, doctor, discover, sync, install agent."`
	Discover   DiscoverCmd   `cmd:"" help:"Discover repos from configured providers."`
	Sync       SyncCmd       `cmd:"" help:"Fetch all tracked repos and backup uncommitted changes."`
	Backup     BackupCmd     `cmd:"" help:"Backup uncommitted changes."`
	Restore    RestoreCmd    `cmd:"" help:"Restore a backup (print steps, apply, or --into a scratch dir)."`
	Status     StatusCmd     `cmd:"" help:"Show state of all managed repos."`
	Doctor     DoctorCmd     `cmd:"" help:"Diagnose tooling, auth, config, and repo health."`
	Run        RunCmd        `cmd:"" help:"Run a command in each matching repo (after --)."`
	Clone      CloneCmd      `cmd:"" help:"Manually clone and track a repo."`
	Adopt      AdoptCmd      `cmd:"" help:"Import existing local repos into management."`
	Remove     RemoveCmd     `cmd:"" help:"Stop tracking repos."`
	Manifest   ManifestCmd   `cmd:"" help:"Export/import a portable manifest of the tracked repo set."`
	RemoteSync RemoteSyncCmd `cmd:"" name:"remote-sync" help:"Push backup refs to remotes."`
	ConfigShow ConfigCmd     `cmd:"" name:"config" help:"Print resolved configuration."`
	Report     ReportCmd     `cmd:"" help:"Summarize recent activity from the operation log."`
	Log        LogCmd        `cmd:"" help:"Print recent operation-log events."`
	Workgroup  WorkgroupCmd  `cmd:"" aliases:"wg" help:"Manage workgroups: parallel worktrees or multi-repo bundles."`

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
	cfg, err := config.Load(c.Config)
	if err != nil {
		return nil, err
	}
	if c.Jobs > 0 {
		cfg.Concurrency = c.Jobs
	}
	c.cfg = cfg
	return cfg, nil
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
