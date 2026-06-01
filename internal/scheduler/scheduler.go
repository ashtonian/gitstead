// Package scheduler installs (and removes) a periodic `git-mgr sync` agent
// using the host's native scheduler: launchd on macOS, a systemd user timer on
// Linux, or a crontab entry as a fallback.
package scheduler

import (
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/ashtonian/git-mgr/internal/shellutil"
)

// Label is the launchd label / systemd unit / crontab marker for the agent.
const Label = "com.ashtonian.git-mgr"

// Kind is a scheduler backend.
type Kind string

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

// Options configures the periodic sync agent.
type Options struct {
	BinPath    string        // absolute path to the git-mgr binary
	Interval   time.Duration // how often to run sync
	ConfigPath string        // optional --config to pass through
	StatePath  string        // optional --state to pass through
	LogPath    string        // file to capture sync output
}

// syncArgs is the argv the agent runs (binary + flags).
func (o Options) syncArgs() []string {
	args := []string{o.BinPath}
	if o.ConfigPath != "" {
		args = append(args, "--config", o.ConfigPath)
	}
	if o.StatePath != "" {
		args = append(args, "--state", o.StatePath)
	}
	return append(args, "sync", "--quiet")
}

// PlistContent renders the launchd plist.
func (o Options) PlistContent() string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	b.WriteString(`<plist version="1.0">` + "\n<dict>\n")
	fmt.Fprintf(&b, "  <key>Label</key><string>%s</string>\n", Label)
	b.WriteString("  <key>ProgramArguments</key>\n  <array>\n")
	for _, a := range o.syncArgs() {
		fmt.Fprintf(&b, "    <string>%s</string>\n", xmlEscape(a))
	}
	b.WriteString("  </array>\n")
	fmt.Fprintf(&b, "  <key>StartInterval</key><integer>%d</integer>\n", int(o.Interval.Seconds()))
	b.WriteString("  <key>RunAtLoad</key><true/>\n")
	if o.LogPath != "" {
		fmt.Fprintf(&b, "  <key>StandardOutPath</key><string>%s</string>\n", xmlEscape(o.LogPath))
		fmt.Fprintf(&b, "  <key>StandardErrorPath</key><string>%s</string>\n", xmlEscape(o.LogPath))
	}
	b.WriteString("</dict>\n</plist>\n")
	return b.String()
}

// ServiceContent renders the systemd user service unit.
func (o Options) ServiceContent() string {
	return fmt.Sprintf(`[Unit]
Description=git-mgr periodic sync

[Service]
Type=oneshot
ExecStart=%s
`, systemdQuote(o.syncArgs()))
}

// TimerContent renders the systemd user timer unit.
func (o Options) TimerContent() string {
	return fmt.Sprintf(`[Unit]
Description=Run git-mgr sync every %s

[Timer]
OnBootSec=2min
OnUnitActiveSec=%d
Persistent=true

[Install]
WantedBy=timers.target
`, o.Interval, int(o.Interval.Seconds()))
}

// CronLine renders the crontab entry. The argv and log path are shell-quoted
// because cron executes the line via /bin/sh, so spaces or shell metacharacters
// in any path would otherwise break it.
func (o Options) CronLine() string {
	line := fmt.Sprintf("%s %s", cronSchedule(o.Interval), shellJoin(o.syncArgs()))
	if o.LogPath != "" {
		line += " 2>> " + shellutil.Quote(o.LogPath)
	}
	return line + "  # " + Label
}

// xmlEscape escapes a string for safe inclusion in a plist <string> body.
func xmlEscape(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// systemdQuote renders argv as a systemd ExecStart value: each argument is
// double-quoted (with " and \ escaped) so paths containing spaces survive
// systemd's own word-splitting.
func systemdQuote(args []string) string {
	quoted := make([]string, len(args))
	for i, a := range args {
		a = strings.ReplaceAll(a, `\`, `\\`)
		a = strings.ReplaceAll(a, `"`, `\"`)
		quoted[i] = `"` + a + `"`
	}
	return strings.Join(quoted, " ")
}

// shellJoin renders argv as a shell-safe command string (each arg single-quoted).
func shellJoin(args []string) string {
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = shellutil.Quote(a)
	}
	return strings.Join(quoted, " ")
}

// cronSchedule maps an interval to a 5-field cron schedule.
func cronSchedule(d time.Duration) string {
	m := int(d.Minutes())
	if m < 1 {
		m = 15
	}
	if m < 60 {
		return fmt.Sprintf("*/%d * * * *", m)
	}
	return fmt.Sprintf("0 */%d * * *", m/60)
}

func plistPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", Label+".plist"), nil
}

func systemdPaths() (service, timer string, err error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", err
	}
	dir := filepath.Join(home, ".config", "systemd", "user")
	return filepath.Join(dir, "git-mgr.service"), filepath.Join(dir, "git-mgr.timer"), nil
}

// Install sets up the agent. With dryRun it reports the planned actions and
// changes nothing. It returns a human-readable list of actions taken/planned.
func Install(kind Kind, o Options, dryRun bool) ([]string, error) {
	switch kind {
	case Launchd:
		return installLaunchd(o, dryRun)
	case Systemd:
		return installSystemd(o, dryRun)
	case Cron:
		return installCron(o, dryRun)
	default:
		return nil, fmt.Errorf("unknown scheduler kind %q", kind)
	}
}

// Uninstall removes the agent. With dryRun it only reports.
func Uninstall(kind Kind, o Options, dryRun bool) ([]string, error) {
	switch kind {
	case Launchd:
		return uninstallLaunchd(dryRun)
	case Systemd:
		return uninstallSystemd(dryRun)
	case Cron:
		return uninstallCron(dryRun)
	default:
		return nil, fmt.Errorf("unknown scheduler kind %q", kind)
	}
}

func installLaunchd(o Options, dryRun bool) ([]string, error) {
	path, err := plistPath()
	if err != nil {
		return nil, err
	}
	actions := []string{
		fmt.Sprintf("write %s:\n%s", path, indent(o.PlistContent())),
		fmt.Sprintf("launchctl unload %s (if loaded)", path),
		fmt.Sprintf("launchctl load -w %s", path),
	}
	if dryRun {
		return actions, nil
	}
	if err := writeFile(path, o.PlistContent(), 0644); err != nil {
		return nil, err
	}
	_ = exec.Command("launchctl", "unload", path).Run()
	if out, err := exec.Command("launchctl", "load", "-w", path).CombinedOutput(); err != nil {
		return actions, fmt.Errorf("launchctl load: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return actions, nil
}

func uninstallLaunchd(dryRun bool) ([]string, error) {
	path, err := plistPath()
	if err != nil {
		return nil, err
	}
	actions := []string{
		fmt.Sprintf("launchctl unload %s", path),
		fmt.Sprintf("remove %s", path),
	}
	if dryRun {
		return actions, nil
	}
	_ = exec.Command("launchctl", "unload", path).Run()
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return actions, err
	}
	return actions, nil
}

func installSystemd(o Options, dryRun bool) ([]string, error) {
	service, timer, err := systemdPaths()
	if err != nil {
		return nil, err
	}
	actions := []string{
		fmt.Sprintf("write %s:\n%s", service, indent(o.ServiceContent())),
		fmt.Sprintf("write %s:\n%s", timer, indent(o.TimerContent())),
		"systemctl --user daemon-reload",
		"systemctl --user enable --now git-mgr.timer",
	}
	if dryRun {
		return actions, nil
	}
	if err := writeFile(service, o.ServiceContent(), 0644); err != nil {
		return nil, err
	}
	if err := writeFile(timer, o.TimerContent(), 0644); err != nil {
		return nil, err
	}
	_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
	if out, err := exec.Command("systemctl", "--user", "enable", "--now", "git-mgr.timer").CombinedOutput(); err != nil {
		return actions, fmt.Errorf("systemctl enable: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return actions, nil
}

func uninstallSystemd(dryRun bool) ([]string, error) {
	service, timer, err := systemdPaths()
	if err != nil {
		return nil, err
	}
	actions := []string{
		"systemctl --user disable --now git-mgr.timer",
		fmt.Sprintf("remove %s and %s", timer, service),
		"systemctl --user daemon-reload",
	}
	if dryRun {
		return actions, nil
	}
	_ = exec.Command("systemctl", "--user", "disable", "--now", "git-mgr.timer").Run()
	// Removing the unit files is the part that actually matters: if they linger,
	// systemd may reload the timer on next daemon-reload/boot. A "not found"
	// error is the expected idempotent case; anything else is reported.
	if err := os.Remove(timer); err != nil && !os.IsNotExist(err) {
		return actions, fmt.Errorf("removing %s: %w", timer, err)
	}
	if err := os.Remove(service); err != nil && !os.IsNotExist(err) {
		return actions, fmt.Errorf("removing %s: %w", service, err)
	}
	if out, err := exec.Command("systemctl", "--user", "daemon-reload").CombinedOutput(); err != nil {
		return actions, fmt.Errorf("systemctl daemon-reload: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return actions, nil
}

func installCron(o Options, dryRun bool) ([]string, error) {
	line := o.CronLine()
	actions := []string{fmt.Sprintf("add crontab entry:\n  %s", line)}
	if dryRun {
		return actions, nil
	}
	existing := currentCrontab()
	if strings.Contains(existing, Label) {
		existing = removeCronLines(existing)
	}
	updated := strings.TrimRight(existing, "\n")
	if updated != "" {
		updated += "\n"
	}
	updated += line + "\n"
	if err := setCrontab(updated); err != nil {
		return actions, err
	}
	return actions, nil
}

func uninstallCron(dryRun bool) ([]string, error) {
	actions := []string{"remove git-mgr crontab entry"}
	if dryRun {
		return actions, nil
	}
	return actions, setCrontab(removeCronLines(currentCrontab()))
}

func currentCrontab() string {
	out, err := exec.Command("crontab", "-l").Output()
	if err != nil {
		return "" // no crontab yet
	}
	return string(out)
}

func setCrontab(content string) error {
	cmd := exec.Command("crontab", "-")
	cmd.Stdin = strings.NewReader(content)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("crontab: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func removeCronLines(crontab string) string {
	var kept []string
	for _, l := range strings.Split(crontab, "\n") {
		if !strings.Contains(l, Label) {
			kept = append(kept, l)
		}
	}
	return strings.Join(kept, "\n")
}

func writeFile(path, content string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), mode)
}

func indent(s string) string {
	var b strings.Builder
	for _, l := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		b.WriteString("    " + l + "\n")
	}
	return b.String()
}
