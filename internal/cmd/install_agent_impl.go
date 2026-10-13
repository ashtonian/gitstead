package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/ashtonian/git-mgr/internal/config"
	"github.com/ashtonian/git-mgr/internal/scheduler"
	"github.com/ashtonian/git-mgr/internal/state"
)

// InstallAgentCmd installs, lists or removes background syncs using the
// host's scheduler: launchd (macOS), a systemd user timer (Linux), or cron.
type InstallAgentCmd struct {
	Name      string        `help:"Schedule name; install several to sync different repos at different intervals." default:"default"`
	Interval  time.Duration `help:"How often to run sync; must divide an hour or a day." default:"15m"`
	Repos     string        `help:"Sync only repos matching this glob." default:"" predictor:"repo"`
	Tag       string        `help:"Sync only repos with this tag." default:"" predictor:"tag"`
	Set       string        `help:"Sync only this named set (from config 'sets:')." default:"" predictor:"set"`
	Kind      string        `help:"Backend: launchd, systemd, or cron (default: per-OS)." default:"" enum:",launchd,systemd,cron"`
	Bin       string        `help:"git-mgr binary the schedule runs (default: the git-mgr on PATH)." default:"" type:"path"`
	Daemon    bool          `help:"macOS: install a LaunchDaemon that runs as you, so sync runs when nobody is logged in (uses sudo)."`
	Linger    bool          `help:"Linux: enable lingering, so the timer runs while you are logged out (loginctl enable-linger)."`
	Log       string        `help:"Log file for sync output (launchd, cron; systemd logs to the journal)." default:"~/.local/share/git-mgr/git-mgr.log" type:"path"`
	List      bool          `help:"List installed schedules."`
	Uninstall bool          `help:"Remove the named schedule instead of installing it."`
	Print     bool          `help:"Print the planned actions and unit files without applying them."`
}

func (c *InstallAgentCmd) Run(ctx *Context) error {
	cfg, err := ctx.LoadConfig()
	if err != nil {
		return err
	}
	st, err := ctx.LoadState()
	if err != nil {
		return err
	}
	if c.Name == "" {
		c.Name = scheduler.DefaultName
	}
	host, err := newHost(ctx)
	if err != nil {
		return err
	}

	switch {
	case c.List:
		return ctx.finish(scheduleList(st), listSchedules(st))
	case c.Uninstall:
		doc, err := c.uninstall(cfg, st, host)
		return ctx.finish(doc, err)
	}
	doc, err := c.install(ctx, cfg, st, host)
	return ctx.finish(doc, err)
}

// scheduleJSON is what install-agent did, or with --print would do: the
// steps on this machine, and what the schedule's environment will lack.
type scheduleJSON struct {
	Name    string   `json:"name"`
	Kind    string   `json:"kind"`
	Planned bool     `json:"planned,omitempty"` // --print: nothing was done
	Actions []string `json:"actions"`
	Notes   []string `json:"notes,omitempty"`
}

// scheduleList is the recorded schedules, by name.
func scheduleList(st *state.State) []*state.ScheduleState {
	out := []*state.ScheduleState{}
	for _, name := range slices.Sorted(maps.Keys(st.Schedules)) {
		out = append(out, st.Schedules[name])
	}
	return out
}

func (c *InstallAgentCmd) install(ctx *Context, cfg *config.Config, st *state.State, host scheduler.Host) (scheduleJSON, error) {
	doc := scheduleJSON{Name: c.Name, Planned: c.Print, Actions: []string{}}
	if _, err := (selection{c.Repos, c.Tag, c.Set}).predicate(cfg); err != nil {
		return doc, err
	}
	s, notes, err := c.schedule(ctx)
	if err != nil {
		return doc, err
	}
	doc.Kind = string(s.Kind)
	if err := s.Validate(); err != nil {
		return doc, err
	}

	// Moving a schedule to another backend removes it from the old one.
	if old := st.Schedules[s.Name]; old != nil && (scheduler.Kind(old.Kind) != s.Kind || old.Daemon != s.Daemon) {
		actions, err := host.Uninstall(scheduleOf(old), c.Print)
		printActions("remove the "+old.Kind+" schedule", actions)
		doc.Actions = append(doc.Actions, actions...)
		if err != nil {
			return doc, fmt.Errorf("removing the %s schedule %q: %w", old.Kind, old.Name, err)
		}
	}
	actions, err := host.Install(s, c.Print)
	doc.Actions = append(doc.Actions, actions...)
	if c.Print {
		printActions(fmt.Sprintf("install plan (%s)", s.Kind), actions)
		printNotes(notes)
		doc.Notes = notes
		return doc, err
	}
	if err != nil {
		return doc, fmt.Errorf("installing the %s schedule %q: %w", s.Kind, s.Name, err)
	}
	printActions(fmt.Sprintf("Installed schedule %q (%s)", s.Name, s.Kind), actions)

	err = st.Update(cfg.LockTimeout, func(st *state.State) error {
		st.Schedules[s.Name] = recordOf(s, time.Now())
		return nil
	})
	if err != nil {
		return doc, fmt.Errorf("recording the schedule: %w", err)
	}

	notes = append(notes, c.afterInstall(s, host)...)
	printNotes(notes)
	doc.Notes = notes
	fmt.Printf("\ngit-mgr will sync %s every %s. Check it with `git-mgr doctor`; remove it with `git-mgr install-agent --uninstall --name %s`.\n",
		describeSelection(s.Sync), s.Every(), s.Name)
	return doc, nil
}

// schedule builds the schedule the flags describe, with notes on what the
// scheduled environment won't have.
func (c *InstallAgentCmd) schedule(ctx *Context) (scheduler.Schedule, []string, error) {
	kind := scheduler.Kind(c.Kind)
	if kind == "" {
		kind = scheduler.DefaultKind()
	}
	bin := c.Bin
	if bin == "" {
		exe, err := os.Executable()
		if err != nil {
			return scheduler.Schedule{}, nil, fmt.Errorf("finding the git-mgr binary: %w", err)
		}
		if bin, err = scheduler.StableBinary(exe, exec.LookPath, scheduler.SameFile); err != nil {
			return scheduler.Schedule{}, nil, err
		}
	}
	env, notes := scheduler.CaptureEnv(os.Getenv)
	var global []string
	if ctx.Config != "" {
		global = append(global, "--config", config.ExpandHome(ctx.Config))
	}
	if ctx.State != "" {
		global = append(global, "--state", config.ExpandHome(ctx.State))
	}
	var sync []string
	for _, f := range []struct{ flag, value string }{{"--repos", c.Repos}, {"--tag", c.Tag}, {"--set", c.Set}} {
		if f.value != "" {
			sync = append(sync, f.flag, f.value)
		}
	}
	s := scheduler.Schedule{
		Name: c.Name, Kind: kind, Interval: c.Interval, Bin: bin,
		Global: global, Sync: sync, Env: env, LogPath: config.ExpandHome(c.Log), Daemon: c.Daemon,
	}
	if c.Daemon {
		u, err := user.Current()
		if err != nil {
			return s, nil, err
		}
		s.User, s.Home = u.Username, u.HomeDir
		notes = append(notes, "a daemon runs before anyone logs in, when the login keychain is locked: "+
			"a token_command that reads the keychain (gh auth token) fails then; read the token from a file instead")
	}
	return s, notes, nil
}

// afterInstall makes or suggests what lets the schedule run without a login.
func (c *InstallAgentCmd) afterInstall(s scheduler.Schedule, host scheduler.Host) []string {
	switch {
	case s.Kind == scheduler.Systemd:
		u, err := user.Current()
		if err != nil {
			return nil
		}
		lingering, err := host.Lingering(u.Username)
		switch {
		case err != nil:
			return []string{fmt.Sprintf("couldn't check lingering (%v); without it the timer stops when you log out", err)}
		case lingering:
			return nil
		case c.Linger:
			if err := host.EnableLingering(u.Username); err != nil {
				return []string{fmt.Sprintf("enabling lingering failed (%v); run `loginctl enable-linger %s` (may need sudo)", err, u.Username)}
			}
			return []string{"enabled lingering: the timer runs while you are logged out"}
		default:
			return []string{"the timer stops when you log out; re-run with --linger (loginctl enable-linger) to keep it running"}
		}
	case s.Kind == scheduler.Launchd && !s.Daemon:
		return []string{"a LaunchAgent runs only while you are logged in to the Mac; for a machine nobody logs in to, re-run with --daemon"}
	}
	return nil
}

func (c *InstallAgentCmd) uninstall(cfg *config.Config, st *state.State, host scheduler.Host) (scheduleJSON, error) {
	s := scheduler.Schedule{Name: c.Name, Kind: scheduler.Kind(c.Kind), Daemon: c.Daemon}
	if rec := st.Schedules[c.Name]; rec != nil {
		s = scheduleOf(rec)
	} else if s.Kind == "" {
		s.Kind = scheduler.DefaultKind()
	}
	actions, err := host.Uninstall(s, c.Print)
	doc := scheduleJSON{Name: s.Name, Kind: string(s.Kind), Planned: c.Print, Actions: append([]string{}, actions...)}
	if c.Print {
		printActions(fmt.Sprintf("uninstall plan (%s)", s.Kind), actions)
		return doc, err
	}
	if err != nil {
		return doc, fmt.Errorf("removing the %s schedule %q: %w", s.Kind, s.Name, err)
	}
	printActions(fmt.Sprintf("Removed schedule %q (%s)", s.Name, s.Kind), actions)
	return doc, st.Update(cfg.LockTimeout, func(st *state.State) error {
		delete(st.Schedules, c.Name)
		return nil
	})
}

func listSchedules(st *state.State) error {
	if len(st.Schedules) == 0 {
		fmt.Println("No schedules installed. Add one with `git-mgr install-agent`.")
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tKIND\tEVERY\tSYNCS\tBINARY")
	for _, name := range slices.Sorted(maps.Keys(st.Schedules)) {
		s := st.Schedules[name]
		kind := s.Kind
		if s.Daemon {
			kind += " (daemon)"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", name, kind, scheduleOf(s).Every(), describeSelection(s.Sync), s.Bin)
	}
	return tw.Flush()
}

// newHost is the scheduler host for this machine and state file.
func newHost(ctx *Context) (scheduler.Host, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return scheduler.Host{}, err
	}
	return scheduler.Host{
		Home:      home,
		UID:       os.Getuid(),
		Run:       runScheduler,
		BackupDir: filepath.Join(filepath.Dir(config.ExpandHome(ctx.State)), "backups"),
	}, nil
}

// runScheduler runs a scheduler tool (launchctl, systemctl, crontab, sudo).
// sudo asks for a password on the terminal itself.
func runScheduler(name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) || stderr.Len() > 0 {
			return out, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
		}
		return out, fmt.Errorf("%s: %w", name, err)
	}
	return out, nil
}

func scheduleOf(r *state.ScheduleState) scheduler.Schedule {
	s := scheduler.Schedule{
		Name: r.Name, Kind: scheduler.Kind(r.Kind), Daemon: r.Daemon, Interval: r.Interval, Bin: r.Bin,
		Global: r.Global, Sync: r.Sync, Env: r.Env, LogPath: r.LogPath,
	}
	if r.Daemon {
		if u, err := user.Current(); err == nil {
			s.User, s.Home = u.Username, u.HomeDir
		}
	}
	return s
}

func recordOf(s scheduler.Schedule, now time.Time) *state.ScheduleState {
	return &state.ScheduleState{
		Name: s.Name, Kind: string(s.Kind), Daemon: s.Daemon, Interval: s.Interval, Bin: s.Bin,
		Global: s.Global, Sync: s.Sync, Env: s.Env, LogPath: s.LogPath, Installed: now,
	}
}

func describeSelection(sync []string) string {
	if len(sync) == 0 {
		return "all repos"
	}
	return strings.Join(sync, " ")
}

func printActions(title string, actions []string) {
	fmt.Printf("%s:\n", title)
	for _, a := range actions {
		fmt.Printf("  - %s\n", a)
	}
}

func printNotes(notes []string) {
	for _, n := range notes {
		fmt.Printf("Note: %s\n", n)
	}
}
