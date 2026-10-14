package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/ashtonian/gitten/internal/config"
	"github.com/ashtonian/gitten/internal/repourl"
	"github.com/ashtonian/gitten/internal/state"
	"github.com/willabides/kongplete"
)

// CLI defines the top-level command structure.
type CLI struct {
	// Global flags
	Config  string `help:"Config file path ($GITTEN_CONFIG); without it, $XDG_CONFIG_HOME/gitten/config.yaml (~/.config/gitten/config.yaml) if it exists, else the defaults." env:"GITTEN_CONFIG" type:"path"`
	State   string `help:"State file path ($GITTEN_STATE); without it, $XDG_DATA_HOME/gitten/state.yaml (~/.local/share/gitten/state.yaml)." env:"GITTEN_STATE" type:"path"`
	Verbose bool   `help:"Verbose output." short:"v"`
	Quiet   bool   `help:"Suppress non-error output." short:"q"`
	Jobs    int    `help:"Max concurrent git operations (default 8)." short:"j" name:"concurrency"`
	JSON    bool   `help:"Print the command's result as one JSON document on stdout; everything else goes to stderr."`

	// Commands
	Version    VersionCmd    `cmd:"" help:"Print version information."`
	Setup      SetupCmd      `cmd:"" help:"Set up gitten, or change your setup: accounts, layout, what to track, features."`
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
	Accounts   AccountsCmd   `cmd:"" help:"List accounts, and make their identities your git's own under their folders (includeIf)."`
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
	ShellInit  ShellInitCmd  `cmd:"" name:"shell-init" help:"Print the shell function that makes gitten cd change directory."`

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
	// JSON asks for the result as JSON, which Emit writes to Out: the real
	// stdout, while os.Stdout carries everything else to stderr.
	JSON bool
	Out  io.Writer

	// Ctx is the root context for the command, cancelled on SIGINT/SIGTERM so
	// in-flight git and API operations stop cleanly. Use GoCtx() to read it
	// (it falls back to context.Background() when unset, e.g. in tests).
	Ctx context.Context

	// changes keeps what the plans the command applied did, for --json.
	changes changesJSON
	// emitted records that the command's document was written to Out.
	emitted bool

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

// ConfigPath is the config file's path: the one given, else the default.
func (c *Context) ConfigPath() string {
	if c.Config == "" {
		return config.DefaultConfigPath()
	}
	return config.ExpandHome(c.Config)
}

// StatePath is the state file's path: the one given, else the default.
func (c *Context) StatePath() string {
	if c.State == "" {
		return config.DefaultStatePath()
	}
	return config.ExpandHome(c.State)
}

// LoadConfig loads and validates the config file. Only the default file may
// be missing, when none was given: one given by --config or $GITTEN_CONFIG,
// whatever its path, must exist.
func (c *Context) LoadConfig() (*config.Config, error) {
	if c.cfg != nil {
		return c.cfg, nil
	}
	var opts []config.Option
	if c.Config == "" {
		opts = append(opts, config.WithOptionalFile())
	}
	cfg, err := config.Load(c.ConfigPath(), opts...)
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

// Emit writes v, the command's result, as the one JSON document --json
// prints. Any credential in a URL within it is redacted, whatever put it
// there.
func (c *Context) Emit(v any) error {
	out := c.Out
	if out == nil {
		out = os.Stdout
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return err
	}
	_, err := io.WriteString(out, repourl.Redact(buf.String()))
	c.emitted = err == nil
	return err
}

// relay writes data, the --json document of gitten run elsewhere, as this
// command's, once it has checked it is one.
func (c *Context) relay(data []byte, from string) error {
	var doc json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&doc); err != nil || !errors.Is(dec.Decode(new(json.RawMessage)), io.EOF) {
		return fmt.Errorf("gitten on %s didn't print one JSON document (is it older than this one?): %s", from, firstLineOf(string(data)))
	}
	return c.Emit(doc)
}

// errNoJSON is what a command with no result to print as JSON returns under
// --json: one that takes the terminal, or only changes your shell.
func errNoJSON(what string) error {
	return fmt.Errorf("%s has no JSON output", what)
}

// LoadState loads the state file, creating it if it doesn't exist.
func (c *Context) LoadState() (*state.State, error) {
	if c.st != nil {
		return c.st, nil
	}
	st, err := state.Load(c.StatePath())
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
	// A config that doesn't load is the command's error to report; its log
	// settings are the defaults meanwhile.
	settings := config.Defaults().Log
	if cfg, err := c.LoadConfig(); err == nil {
		settings = cfg.Log
	}
	configured := slog.LevelInfo
	_ = configured.UnmarshalText([]byte(settings.Level)) // validated with the config
	// --verbose and --quiet are for this terminal; the file, read later, keeps
	// the configured level unless --verbose asks for more.
	terminal, file := configured, configured
	switch {
	case c.Verbose:
		terminal, file = slog.LevelDebug, slog.LevelDebug
	case c.Quiet:
		terminal = slog.LevelError
	}
	handler := func(w io.Writer, level slog.Level) slog.Handler {
		opts := &slog.HandlerOptions{Level: level}
		if settings.Format == "text" {
			return slog.NewTextHandler(w, opts)
		}
		return slog.NewJSONHandler(w, opts)
	}
	h := handler(os.Stderr, terminal)
	if settings.File != "" {
		f, err := openLogFile(config.ExpandHome(settings.File))
		if err != nil {
			fmt.Fprintf(os.Stderr, "gitten: logging only to stderr: %v\n", err)
		} else {
			h = slog.NewMultiHandler(h, handler(f, file))
		}
	}
	c.logger = slog.New(h)
	// Packages that log through slog's default logger follow the same
	// settings.
	slog.SetDefault(c.logger)
	return c.logger
}

// openLogFile opens path for appending, making its directory.
func openLogFile(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
}
