// Package scheduler installs and removes background `gitten sync` runs with
// the host's scheduler: a launchd agent or daemon on macOS, a systemd user
// timer on Linux, or a crontab entry.
package scheduler

import (
	"encoding/xml"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/ashtonian/gitten/internal/shellutil"
)

// labelPrefix names every schedule's launchd label and crontab marker.
const labelPrefix = "com.ashtonian.gitten"

// DefaultName is the schedule installed when none is named.
const DefaultName = "default"

// niceness is how far below normal priority a scheduled sync runs.
const niceness = 10

// Kind is a scheduler backend.
type Kind string

// The scheduler backends: macOS's launchd, Linux's systemd user timers, and
// cron anywhere else.
const (
	Launchd Kind = "launchd"
	Systemd Kind = "systemd"
	Cron    Kind = "cron"
)

// DefaultKind picks the native scheduler for the current OS.
func DefaultKind() Kind {
	switch runtime.GOOS {
	case "darwin":
		return Launchd
	case "linux":
		return Systemd
	default:
		return Cron
	}
}

// Schedule is one background sync.
type Schedule struct {
	Name     string
	Kind     Kind
	Interval time.Duration
	Bin      string            // a stable path to gitten; see StableBinary
	Global   []string          // flags before the subcommand: --config, --state
	Sync     []string          // flags for sync: which repos it runs on
	Env      map[string]string // captured at install; see CaptureEnv
	LogPath  string            // launchd and cron; systemd logs to the journal
	// Daemon installs a LaunchDaemon that runs as User instead of a
	// LaunchAgent, so sync runs on a Mac nobody is logged in to. It needs
	// sudo to install.
	Daemon bool
	User   string
	Home   string
}

var validName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// Validate checks the schedule can be installed as given.
func (s Schedule) Validate() error {
	if !validName.MatchString(s.Name) {
		return fmt.Errorf("schedule name %q: use up to 32 lowercase letters, digits and dashes", s.Name)
	}
	switch s.Kind {
	case Launchd, Systemd, Cron:
	default:
		return fmt.Errorf("unknown scheduler kind %q", s.Kind)
	}
	if s.Daemon && s.Kind != Launchd {
		return fmt.Errorf("a daemon schedule is a launchd LaunchDaemon; %s runs without a login already (systemd: --linger)", s.Kind)
	}
	if s.Daemon && (s.User == "" || s.Home == "") {
		return fmt.Errorf("a daemon schedule needs the user and home directory to run as")
	}
	if !filepath.IsAbs(s.Bin) {
		return fmt.Errorf("binary path %q is not absolute", s.Bin)
	}
	_, err := calendar(s.Interval)
	return err
}

// Label is the schedule's launchd label and crontab marker.
func (s Schedule) Label() string {
	if s.Name == DefaultName {
		return labelPrefix
	}
	return labelPrefix + "." + s.Name
}

// unit is the schedule's systemd unit name, without the suffix.
func (s Schedule) unit() string {
	if s.Name == DefaultName {
		return "gitten"
	}
	return "gitten-" + s.Name
}

// Every is the interval as people write it: 15m, 2h.
func (s Schedule) Every() string { return FormatInterval(s.Interval) }

// FormatInterval writes an interval as people do: 15m, 2h.
func FormatInterval(d time.Duration) string {
	return strings.TrimSuffix(strings.TrimSuffix(d.String(), "0s"), "0m")
}

// Argv is the command the schedule runs.
func (s Schedule) Argv() []string {
	args := append([]string{s.Bin}, s.Global...)
	args = append(args, "sync", "--quiet")
	return append(args, s.Sync...)
}

// Files are where the schedule's definition lives; cron keeps it in the
// crontab instead.
func (s Schedule) Files(home string) []string {
	switch {
	case s.Kind == Launchd && s.Daemon:
		return []string{filepath.Join("/Library/LaunchDaemons", s.Label()+".plist")}
	case s.Kind == Launchd:
		return []string{filepath.Join(home, "Library", "LaunchAgents", s.Label()+".plist")}
	case s.Kind == Systemd:
		dir := filepath.Join(home, ".config", "systemd", "user")
		return []string{filepath.Join(dir, s.unit()+".service"), filepath.Join(dir, s.unit()+".timer")}
	}
	return nil
}

// ValidInterval reports whether a sync can be scheduled every d: d must
// divide an hour or a day, which cron and systemd can run exactly.
func ValidInterval(d time.Duration) error {
	_, err := calendar(d)
	return err
}

// calendar is the interval as a schedule both cron and systemd can run
// exactly: every m minutes within an hour, or every h hours within a day.
type cal struct{ minutes, hours int }

func calendar(d time.Duration) (cal, error) {
	switch {
	case d >= time.Minute && d < time.Hour && d%time.Minute == 0 && 60%int(d/time.Minute) == 0:
		return cal{minutes: int(d / time.Minute)}, nil
	case d >= time.Hour && d <= 24*time.Hour && d%time.Hour == 0 && 24%int(d/time.Hour) == 0:
		return cal{hours: int(d / time.Hour)}, nil
	}
	return cal{}, fmt.Errorf("interval %s can't be scheduled exactly; use one that divides an hour or a day "+
		"(5m, 10m, 15m, 20m, 30m, 1h, 2h, 3h, 4h, 6h, 8h, 12h, 24h)", d)
}

// Plist renders the launchd job.
func (s Schedule) Plist() string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	b.WriteString(`<plist version="1.0">` + "\n<dict>\n")
	fmt.Fprintf(&b, "  <key>Label</key><string>%s</string>\n", xmlEscape(s.Label()))
	b.WriteString("  <key>ProgramArguments</key>\n  <array>\n")
	for _, a := range s.Argv() {
		fmt.Fprintf(&b, "    <string>%s</string>\n", xmlEscape(a))
	}
	b.WriteString("  </array>\n")
	env := maps.Clone(s.Env)
	if s.Daemon {
		fmt.Fprintf(&b, "  <key>UserName</key><string>%s</string>\n", xmlEscape(s.User))
		if env == nil {
			env = map[string]string{}
		}
		env["HOME"] = s.Home
	}
	if len(env) > 0 {
		b.WriteString("  <key>EnvironmentVariables</key>\n  <dict>\n")
		for _, k := range slices.Sorted(maps.Keys(env)) {
			fmt.Fprintf(&b, "    <key>%s</key><string>%s</string>\n", xmlEscape(k), xmlEscape(env[k]))
		}
		b.WriteString("  </dict>\n")
	}
	fmt.Fprintf(&b, "  <key>StartInterval</key><integer>%d</integer>\n", int(s.Interval.Seconds()))
	b.WriteString("  <key>RunAtLoad</key><true/>\n")
	b.WriteString("  <key>ProcessType</key><string>Background</string>\n")
	b.WriteString("  <key>LowPriorityIO</key><true/>\n")
	fmt.Fprintf(&b, "  <key>Nice</key><integer>%d</integer>\n", niceness)
	if s.LogPath != "" {
		fmt.Fprintf(&b, "  <key>StandardOutPath</key><string>%s</string>\n", xmlEscape(s.LogPath))
		fmt.Fprintf(&b, "  <key>StandardErrorPath</key><string>%s</string>\n", xmlEscape(s.LogPath))
	}
	b.WriteString("</dict>\n</plist>\n")
	return b.String()
}

// Service renders the systemd service unit.
func (s Schedule) Service() string {
	var b strings.Builder
	fmt.Fprintf(&b, "[Unit]\nDescription=gitten sync (%s)\n\n[Service]\nType=oneshot\n", s.Name)
	fmt.Fprintf(&b, "ExecStart=%s\n", systemdArgv(s.Argv()))
	for _, k := range slices.Sorted(maps.Keys(s.Env)) {
		fmt.Fprintf(&b, "Environment=%s\n", systemdQuote(k+"="+s.Env[k]))
	}
	fmt.Fprintf(&b, "Nice=%d\nIOSchedulingClass=idle\n", niceness)
	return b.String()
}

// Timer renders the systemd timer unit. OnCalendar with Persistent=true runs
// once at boot for any run missed while the machine was off.
func (s Schedule) Timer() string {
	c, _ := calendar(s.Interval)
	spec := "*-*-* 00:00:00" // every 24h
	switch {
	case c.minutes > 0:
		spec = fmt.Sprintf("*-*-* *:00/%d:00", c.minutes)
	case c.hours < 24:
		spec = fmt.Sprintf("*-*-* 00/%d:00:00", c.hours)
	}
	return fmt.Sprintf(`[Unit]
Description=Run gitten sync (%s) every %s

[Timer]
OnCalendar=%s
Persistent=true

[Install]
WantedBy=timers.target
`, s.Name, s.Every(), spec)
}

// CronLine renders the crontab entry, marked with the schedule's label.
// cron runs the line with /bin/sh, so every word is shell-quoted, and %
// (a newline to cron) is escaped.
func (s Schedule) CronLine() string {
	c, _ := calendar(s.Interval)
	when := "0 0 * * *"
	switch {
	case c.minutes == 1:
		when = "* * * * *"
	case c.minutes > 0:
		when = fmt.Sprintf("*/%d * * * *", c.minutes)
	case c.hours == 1:
		when = "0 * * * *"
	case c.hours < 24:
		when = fmt.Sprintf("0 */%d * * *", c.hours)
	}
	var words []string
	for _, k := range slices.Sorted(maps.Keys(s.Env)) {
		words = append(words, k+"="+shellutil.Quote(s.Env[k]))
	}
	words = append(words, "nice", "-n", fmt.Sprint(niceness))
	for _, a := range s.Argv() {
		words = append(words, shellutil.Quote(a))
	}
	if s.LogPath != "" {
		words = append(words, ">>", shellutil.Quote(s.LogPath), "2>&1")
	}
	command := strings.ReplaceAll(strings.Join(words, " "), "%", `\%`)
	return when + " " + command + "  " + s.marker()
}

func (s Schedule) marker() string { return "# " + s.Label() }

func xmlEscape(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// systemdArgv renders argv for ExecStart: each word quoted, so paths with
// spaces stay one word.
func systemdArgv(args []string) string {
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = systemdQuote(strings.ReplaceAll(a, "$", "$$"))
	}
	return strings.Join(quoted, " ")
}

// systemdQuote double-quotes a unit-file value, escaping what systemd would
// otherwise interpret: backslashes, quotes and % specifiers.
func systemdQuote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, "%", "%%")
	return `"` + s + `"`
}

// CaptureEnv picks what a scheduled run needs from the installing shell's
// environment: PATH, so gh, credential helpers and ssh are found, and
// SSH_AUTH_SOCK when the agent's socket outlives the login. A socket in a
// temporary directory belongs to one login; it is left out, with a note.
func CaptureEnv(getenv func(string) string) (env map[string]string, notes []string) {
	env = map[string]string{}
	if p := getenv("PATH"); p != "" {
		env["PATH"] = p
	}
	sock := getenv("SSH_AUTH_SOCK")
	switch {
	case sock == "":
	case perLogin(sock):
		notes = append(notes, fmt.Sprintf("SSH_AUTH_SOCK (%s) changes every login, so it isn't recorded; "+
			"scheduled runs use keys without a passphrase (an account's ssh_key) or the system agent on macOS", sock))
	default:
		env["SSH_AUTH_SOCK"] = sock
	}
	return env, notes
}

// perLogin reports whether an agent socket belongs to one login or boot:
// one in a temporary directory, or the one launchd makes per session.
func perLogin(sock string) bool {
	return inTempDir(sock) || strings.HasPrefix(sock, "/var/run/com.apple.launchd.") ||
		strings.HasPrefix(sock, "/private/var/run/com.apple.launchd.")
}

func inTempDir(path string) bool {
	for _, dir := range []string{"/tmp/", "/private/tmp/", "/var/folders/", "/private/var/folders/", filepath.Clean(os.TempDir()) + "/"} {
		if strings.HasPrefix(path, dir) {
			return true
		}
	}
	return false
}

// StableBinary chooses the gitten path a schedule runs: the one on PATH when
// it is this binary (exe), since a package manager's link there survives
// upgrades. A path an upgrade or cleanup removes -- the Nix store, a Homebrew
// Cellar, a temporary build -- is refused. sameFile reports whether two paths
// are one file; SameFile does for real files.
func StableBinary(exe string, lookPath func(string) (string, error), sameFile func(a, b string) bool) (string, error) {
	if onPath, err := lookPath("gitten"); err == nil && !ephemeral(onPath) && sameFile(onPath, exe) {
		if abs, err := filepath.Abs(onPath); err == nil {
			return abs, nil
		}
	}
	if ephemeral(exe) {
		return "", fmt.Errorf("gitten is running from %s, which an upgrade or cleanup removes; "+
			"put gitten on PATH (or pass --bin) and install again", exe)
	}
	return exe, nil
}

func ephemeral(path string) bool {
	return strings.HasPrefix(path, "/nix/store/") || strings.Contains(path, "/Cellar/") ||
		strings.Contains(path, "/go-build") || inTempDir(path)
}

// SameFile reports whether a and b name the same file.
func SameFile(a, b string) bool {
	ai, err := os.Stat(a)
	if err != nil {
		return false
	}
	bi, err := os.Stat(b)
	return err == nil && os.SameFile(ai, bi)
}
