package scheduler

import (
	"cmp"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Runner runs a command and returns its standard output; a failure's error
// includes what it printed on standard error.
type Runner func(name string, args ...string) ([]byte, error)

// Host installs schedules on this machine: it writes their files and runs
// launchctl, systemctl, loginctl, crontab and, for a daemon, sudo.
type Host struct {
	Home string
	UID  int
	Run  Runner
	// BackupDir receives a copy of the crontab before gitstead changes it.
	BackupDir string
	Now       func() time.Time
	// DaemonDir holds LaunchDaemons: /Library/LaunchDaemons unless set.
	DaemonDir string
}

// Files are where s's definition lives on this host; cron keeps it in the
// crontab instead.
func (h Host) Files(s Schedule) []string {
	switch {
	case s.Kind == Launchd && s.Daemon:
		return []string{filepath.Join(h.daemonDir(), s.Label()+".plist")}
	case s.Kind == Launchd:
		return []string{filepath.Join(h.agentDir(), s.Label()+".plist")}
	case s.Kind == Systemd:
		return []string{filepath.Join(h.unitDir(), s.unit()+".service"), filepath.Join(h.unitDir(), s.unit()+".timer")}
	}
	return nil
}

func (h Host) agentDir() string { return filepath.Join(h.Home, "Library", "LaunchAgents") }
func (h Host) unitDir() string  { return filepath.Join(h.Home, ".config", "systemd", "user") }

func (h Host) daemonDir() string {
	if h.DaemonDir != "" {
		return h.DaemonDir
	}
	return "/Library/LaunchDaemons"
}

// CrontabWhere is a Definition's Where for one in the crontab.
const CrontabWhere = "crontab"

// Definition is a schedule defined on this host under gitstead's naming --
// the launchd label, systemd unit or crontab marker install-agent gives it --
// whoever wrote it.
type Definition struct {
	Name   string
	Kind   Kind
	Daemon bool
	// Where defines it: its files, or CrontabWhere.
	Where []string
}

// Job is what the scheduler calls it, as Schedule.Job.
func (d Definition) Job() string { return Schedule{Name: d.Name, Kind: d.Kind}.Job() }

// Definitions are the schedules defined on this host under gitstead's
// naming, by name and then kind, whether gitstead or another tool -- a
// dotfiles repo, home-manager -- wrote them: launchd agents and daemons,
// systemd user units and crontab lines. A crontab that can't be read is an
// error, with the definitions found elsewhere; no crontab command is none.
func (h Host) Definitions() ([]Definition, error) {
	var defs []Definition
	for _, dir := range []struct {
		path   string
		daemon bool
	}{{h.agentDir(), false}, {h.daemonDir(), true}} {
		for _, path := range glob(dir.path, labelPrefix+"*.plist") {
			if name, ok := nameOfID(labelPrefix, ".", strings.TrimSuffix(filepath.Base(path), ".plist")); ok {
				defs = append(defs, Definition{Name: name, Kind: Launchd, Daemon: dir.daemon, Where: []string{path}})
			}
		}
	}
	units := map[string][]string{}
	for _, path := range glob(h.unitDir(), unitPrefix+"*") {
		base := filepath.Base(path)
		unit := strings.TrimSuffix(strings.TrimSuffix(base, ".service"), ".timer")
		if unit == base {
			continue
		}
		if name, ok := nameOfID(unitPrefix, "-", unit); ok {
			units[name] = append(units[name], path)
		}
	}
	for name, files := range units {
		defs = append(defs, Definition{Name: name, Kind: Systemd, Where: files})
	}
	tab, err := h.crontab()
	if errors.Is(err, exec.ErrNotFound) {
		tab, err = nil, nil
	}
	inTab := map[string]bool{}
	for _, line := range tab {
		i := strings.LastIndex(line, "# "+labelPrefix)
		if i < 0 {
			continue
		}
		if name, ok := nameOfID(labelPrefix, ".", strings.TrimSpace(line[i+2:])); ok && !inTab[name] {
			inTab[name] = true
			defs = append(defs, Definition{Name: name, Kind: Cron, Where: []string{CrontabWhere}})
		}
	}
	slices.SortFunc(defs, func(a, b Definition) int {
		return cmp.Or(strings.Compare(a.Name, b.Name), strings.Compare(string(a.Kind), string(b.Kind)), strings.Compare(a.Where[0], b.Where[0]))
	})
	return defs, err
}

// glob is dir's entries matching pattern, sorted; none when dir is missing.
func glob(dir, pattern string) []string {
	paths, _ := filepath.Glob(filepath.Join(dir, pattern)) // only a malformed pattern fails
	return paths
}

// Install installs s, replacing a schedule of the same name and kind. With
// dryRun it changes nothing. Either way it returns what it does.
func (h Host) Install(s Schedule, dryRun bool) ([]string, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	switch {
	case s.Kind == Launchd && s.Daemon:
		return h.installDaemon(s, dryRun)
	case s.Kind == Launchd:
		return h.installAgent(s, dryRun)
	case s.Kind == Systemd:
		return h.installSystemd(s, dryRun)
	default:
		return h.installCron(s, dryRun)
	}
}

// Uninstall removes s. Removing one that isn't installed is not an error.
func (h Host) Uninstall(s Schedule, dryRun bool) ([]string, error) {
	switch {
	case s.Kind == Launchd && s.Daemon:
		path := h.Files(s)[0]
		actions := []string{
			"sudo launchctl bootout system/" + s.Label(),
			"sudo rm -f " + path,
		}
		if dryRun {
			return actions, nil
		}
		_, _ = h.Run("sudo", "launchctl", "bootout", "system/"+s.Label())
		if _, err := h.Run("sudo", "rm", "-f", path); err != nil {
			return actions, err
		}
		return actions, nil
	case s.Kind == Launchd:
		path := h.Files(s)[0]
		actions := []string{"launchctl bootout " + h.gui() + "/" + s.Label(), "remove " + path}
		if dryRun {
			return actions, nil
		}
		_, _ = h.Run("launchctl", "bootout", h.gui()+"/"+s.Label())
		return actions, removeIfPresent(path)
	case s.Kind == Systemd:
		files := h.Files(s)
		actions := []string{
			"systemctl --user disable --now " + s.unit() + ".timer",
			"remove " + strings.Join(files, " and "),
			"systemctl --user daemon-reload",
		}
		if dryRun {
			return actions, nil
		}
		_, _ = h.Run("systemctl", "--user", "disable", "--now", s.unit()+".timer")
		for _, f := range files {
			if err := removeIfPresent(f); err != nil {
				return actions, err
			}
		}
		if _, err := h.Run("systemctl", "--user", "daemon-reload"); err != nil {
			return actions, err
		}
		return actions, nil
	default:
		actions := []string{"remove the crontab line marked " + s.marker()}
		if dryRun {
			return actions, nil
		}
		return actions, h.editCrontab(func(lines []string) []string { return withoutMarker(lines, s.marker()) })
	}
}

// Installed reports whether s's definition is still in place.
func (h Host) Installed(s Schedule) (bool, error) {
	if s.Kind == Cron {
		tab, err := h.crontab()
		if err != nil {
			return false, err
		}
		return len(withoutMarker(tab, s.marker())) != len(tab), nil
	}
	for _, f := range h.Files(s) {
		if _, err := os.Stat(f); err != nil {
			return false, nil
		}
	}
	return true, nil
}

func (h Host) gui() string { return fmt.Sprintf("gui/%d", h.UID) }

func (h Host) installAgent(s Schedule, dryRun bool) ([]string, error) {
	path := h.Files(s)[0]
	actions := []string{
		fmt.Sprintf("write %s:\n%s", path, indent(s.Plist())),
		fmt.Sprintf("launchctl bootout %s/%s (if loaded)", h.gui(), s.Label()),
		fmt.Sprintf("launchctl bootstrap %s %s", h.gui(), path),
	}
	if dryRun {
		return actions, nil
	}
	if err := writeFile(path, s.Plist()); err != nil {
		return nil, err
	}
	_, _ = h.Run("launchctl", "bootout", h.gui()+"/"+s.Label())
	if _, err := h.Run("launchctl", "bootstrap", h.gui(), path); err != nil {
		return actions, err
	}
	return actions, nil
}

// installDaemon installs a LaunchDaemon, which only root can: the plist is
// written to a private file first and installed with sudo.
func (h Host) installDaemon(s Schedule, dryRun bool) ([]string, error) {
	path := h.Files(s)[0]
	actions := []string{
		fmt.Sprintf("sudo install -m 0644 <plist> %s:\n%s", path, indent(s.Plist())),
		fmt.Sprintf("sudo launchctl bootout system/%s (if loaded)", s.Label()),
		"sudo launchctl bootstrap system " + path,
	}
	if dryRun {
		return actions, nil
	}
	tmp, err := os.CreateTemp("", "gitstead-*.plist")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(s.Plist()); err != nil {
		tmp.Close()
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}
	if _, err := h.Run("sudo", "install", "-m", "0644", "-o", "root", "-g", "wheel", tmp.Name(), path); err != nil {
		return actions, err
	}
	_, _ = h.Run("sudo", "launchctl", "bootout", "system/"+s.Label())
	if _, err := h.Run("sudo", "launchctl", "bootstrap", "system", path); err != nil {
		return actions, err
	}
	return actions, nil
}

func (h Host) installSystemd(s Schedule, dryRun bool) ([]string, error) {
	files := h.Files(s)
	service, timer := files[0], files[1]
	actions := []string{
		fmt.Sprintf("write %s:\n%s", service, indent(s.Service())),
		fmt.Sprintf("write %s:\n%s", timer, indent(s.Timer())),
		"systemctl --user daemon-reload",
		"systemctl --user enable --now " + s.unit() + ".timer",
	}
	if dryRun {
		return actions, nil
	}
	if err := writeFile(service, s.Service()); err != nil {
		return nil, err
	}
	if err := writeFile(timer, s.Timer()); err != nil {
		return nil, err
	}
	if _, err := h.Run("systemctl", "--user", "daemon-reload"); err != nil {
		return actions, err
	}
	if _, err := h.Run("systemctl", "--user", "enable", "--now", s.unit()+".timer"); err != nil {
		return actions, err
	}
	return actions, nil
}

func (h Host) installCron(s Schedule, dryRun bool) ([]string, error) {
	line := s.CronLine()
	actions := []string{"back up the crontab to " + h.BackupDir, "add crontab line:\n    " + line}
	if dryRun {
		return actions, nil
	}
	return actions, h.editCrontab(func(lines []string) []string {
		return append(withoutMarker(lines, s.marker()), line)
	})
}

// editCrontab rewrites the crontab through edit. It never writes over a
// crontab it couldn't read, and saves a copy of a non-empty one first.
func (h Host) editCrontab(edit func([]string) []string) error {
	lines, err := h.crontab()
	if err != nil {
		return err
	}
	if len(lines) > 0 {
		if err := h.backupCrontab(lines); err != nil {
			return fmt.Errorf("backing up the crontab, so leaving it alone: %w", err)
		}
	}
	content := strings.Join(edit(lines), "\n")
	if content != "" {
		content += "\n"
	}
	f, err := os.CreateTemp("", "gitstead-crontab-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(content); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	_, err = h.Run("crontab", f.Name())
	return err
}

// crontab returns the current crontab's lines. Having none is an empty
// crontab; any other failure is an error, so a transient one can't make an
// edit drop everyone else's jobs.
func (h Host) crontab() ([]string, error) {
	out, err := h.Run("crontab", "-l")
	if err != nil {
		if strings.Contains(err.Error(), "no crontab for") {
			return nil, nil
		}
		return nil, fmt.Errorf("reading the crontab: %w", err)
	}
	text := strings.TrimRight(string(out), "\n")
	if text == "" {
		return nil, nil
	}
	return strings.Split(text, "\n"), nil
}

func (h Host) backupCrontab(lines []string) error {
	if err := os.MkdirAll(h.BackupDir, 0o700); err != nil {
		return err
	}
	now := time.Now
	if h.Now != nil {
		now = h.Now
	}
	path := filepath.Join(h.BackupDir, "crontab-"+now().UTC().Format("20060102T150405Z")+".bak")
	return os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600)
}

func withoutMarker(lines []string, marker string) []string {
	var kept []string
	for _, l := range lines {
		if !strings.HasSuffix(strings.TrimSpace(l), marker) {
			kept = append(kept, l)
		}
	}
	return kept
}

// Lingering reports whether systemd keeps user's timers running while they
// are logged out.
func (h Host) Lingering(user string) (bool, error) {
	out, err := h.Run("loginctl", "show-user", user, "--property=Linger", "--value")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(string(out)) == "yes", nil
}

// EnableLingering makes systemd keep user's timers running while they are
// logged out.
func (h Host) EnableLingering(user string) error {
	_, err := h.Run("loginctl", "enable-linger", user)
	return err
}

func writeFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

func removeIfPresent(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("removing %s: %w", path, err)
	}
	return nil
}

func indent(s string) string {
	var b strings.Builder
	for _, l := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		b.WriteString("    " + l + "\n")
	}
	return b.String()
}
