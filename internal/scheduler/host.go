package scheduler

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
	// BackupDir receives a copy of the crontab before git-mgr changes it.
	BackupDir string
	Now       func() time.Time
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
		path := s.Files(h.Home)[0]
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
		path := s.Files(h.Home)[0]
		actions := []string{"launchctl bootout " + h.gui() + "/" + s.Label(), "remove " + path}
		if dryRun {
			return actions, nil
		}
		_, _ = h.Run("launchctl", "bootout", h.gui()+"/"+s.Label())
		return actions, removeIfPresent(path)
	case s.Kind == Systemd:
		files := s.Files(h.Home)
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
	for _, f := range s.Files(h.Home) {
		if _, err := os.Stat(f); err != nil {
			return false, nil
		}
	}
	return true, nil
}

func (h Host) gui() string { return fmt.Sprintf("gui/%d", h.UID) }

func (h Host) installAgent(s Schedule, dryRun bool) ([]string, error) {
	path := s.Files(h.Home)[0]
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
	path := s.Files(h.Home)[0]
	actions := []string{
		fmt.Sprintf("sudo install -m 0644 <plist> %s:\n%s", path, indent(s.Plist())),
		fmt.Sprintf("sudo launchctl bootout system/%s (if loaded)", s.Label()),
		"sudo launchctl bootstrap system " + path,
	}
	if dryRun {
		return actions, nil
	}
	tmp, err := os.CreateTemp("", "git-mgr-*.plist")
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
	files := s.Files(h.Home)
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
	f, err := os.CreateTemp("", "git-mgr-crontab-*")
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
