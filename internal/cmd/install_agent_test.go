package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ashtonian/gitstead/internal/scheduler"
	"github.com/ashtonian/gitstead/internal/state"
)

// schedulerHost is a scheduler host in temporary directories whose commands
// only record what they were asked; its crontab is a string.
type schedulerHost struct {
	scheduler.Host
	calls   []string
	crontab string
}

func newSchedulerHost(t *testing.T) *schedulerHost {
	t.Helper()
	h := &schedulerHost{}
	h.Host = scheduler.Host{
		Home: t.TempDir(), UID: 501, BackupDir: t.TempDir(), DaemonDir: t.TempDir(),
		Run: func(name string, args ...string) ([]byte, error) {
			line := name + " " + strings.Join(args, " ")
			h.calls = append(h.calls, line)
			switch {
			case line == "crontab -l" && h.crontab == "":
				return nil, errors.New("crontab -l: exit status 1: crontab: no crontab for me")
			case line == "crontab -l":
				return []byte(h.crontab), nil
			case name == "crontab":
				data, err := os.ReadFile(args[0])
				h.crontab = string(data)
				return nil, err
			}
			return nil, nil
		},
	}
	return h
}

// ran reports whether a command starting with prefix was run.
func (h *schedulerHost) ran(prefix string) bool {
	for _, c := range h.calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

func scheduleContext(t *testing.T) (*Context, *state.State) {
	t.Helper()
	ctx := &Context{State: filepath.Join(t.TempDir(), "state.yaml"), Ctx: t.Context()}
	st, err := ctx.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	return ctx, st
}

func writeJob(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A schedule another tool installed under gitstead's name -- a launchd agent,
// systemd units or a crontab line -- is left alone: install-agent refuses,
// naming where it is, writes nothing and records nothing, until --force
// replaces it. After that it is gitstead's, and reinstalls need no --force.
func TestInstallAgent_LeavesAnotherToolsSchedule(t *testing.T) {
	const theirs = "# written by dotfiles\n"
	tests := []struct {
		kind  scheduler.Kind
		plant func(h *schedulerHost) string // where it is, in the message
	}{
		{scheduler.Launchd, func(h *schedulerHost) string {
			path := filepath.Join(h.Home, "Library", "LaunchAgents", "com.ashtonian.gitstead.plist")
			writeJob(t, path, theirs)
			return path
		}},
		{scheduler.Systemd, func(h *schedulerHost) string {
			path := filepath.Join(h.Home, ".config", "systemd", "user", "gitstead.timer")
			writeJob(t, path, theirs)
			return path
		}},
		{scheduler.Cron, func(h *schedulerHost) string {
			h.crontab = "*/15 * * * * flock -n /tmp/gitstead.lock gitstead sync --quiet  # com.ashtonian.gitstead\n"
			return "crontab"
		}},
	}
	for _, tt := range tests {
		t.Run(string(tt.kind), func(t *testing.T) {
			h := newSchedulerHost(t)
			where := tt.plant(h)
			crontab := h.crontab
			ctx, st := scheduleContext(t)
			cfg, err := ctx.LoadConfig()
			if err != nil {
				t.Fatal(err)
			}
			c := &InstallAgentCmd{Name: scheduler.DefaultName, Kind: string(tt.kind), Interval: 15 * time.Minute, Bin: "/usr/local/bin/gitstead"}

			_, err = c.install(ctx, cfg, st, h.Host)
			var refused *unmanagedError
			if !errors.As(err, &refused) || !strings.Contains(err.Error(), where) || !strings.Contains(err.Error(), "--force replaces it") {
				t.Fatalf("install = %v, want it refused, naming %s", err, where)
			}
			if h.ran("launchctl bootstrap") || h.ran("systemctl --user enable") || h.crontab != crontab {
				t.Errorf("a refused install ran %v", h.calls)
			}
			if where != "crontab" {
				if data, _ := os.ReadFile(where); string(data) != theirs {
					t.Errorf("a refused install wrote %s:\n%s", where, data)
				}
			}
			if len(st.Schedules) != 0 {
				t.Errorf("a refused install recorded %v", st.Schedules)
			}

			c.Print = true
			if _, err := c.install(ctx, cfg, st, h.Host); !errors.As(err, &refused) {
				t.Errorf("install --print = %v, want the same refusal", err)
			}
			c.Print, c.Force = false, true
			if _, err := c.install(ctx, cfg, st, h.Host); err != nil {
				t.Fatalf("install --force: %v", err)
			}
			if st.Schedules[scheduler.DefaultName] == nil {
				t.Fatal("install --force recorded nothing")
			}
			if where != "crontab" {
				if data, _ := os.ReadFile(where); string(data) == theirs {
					t.Errorf("install --force left %s as it was", where)
				}
			} else if strings.Count(h.crontab, "# com.ashtonian.gitstead\n") != 1 || strings.Contains(h.crontab, "flock -n") {
				t.Errorf("install --force crontab:\n%s", h.crontab)
			}
			c.Force = false
			if _, err := c.install(ctx, cfg, st, h.Host); err != nil {
				t.Errorf("reinstalling gitstead's own schedule: %v", err)
			}
		})
	}
}

// --force replaces another tool's schedule of the name, of whatever kind:
// one of another kind is removed, so the two don't both run.
func TestInstallAgent_ForceRemovesAnotherKind(t *testing.T) {
	h := newSchedulerHost(t)
	h.crontab = "0 3 * * * backup.sh\n*/15 * * * * gitstead sync  # com.ashtonian.gitstead\n"
	ctx, st := scheduleContext(t)
	cfg, err := ctx.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	c := &InstallAgentCmd{Name: scheduler.DefaultName, Kind: string(scheduler.Launchd), Interval: 15 * time.Minute, Bin: "/usr/local/bin/gitstead", Force: true}
	if _, err := c.install(ctx, cfg, st, h.Host); err != nil {
		t.Fatal(err)
	}
	if h.crontab != "0 3 * * * backup.sh\n" {
		t.Errorf("crontab after replacing its line with a launchd agent:\n%s", h.crontab)
	}
	if _, err := os.Stat(filepath.Join(h.Home, "Library", "LaunchAgents", "com.ashtonian.gitstead.plist")); err != nil {
		t.Error(err)
	}
}

// --uninstall leaves another tool's schedule too, unless forced.
func TestInstallAgent_UninstallLeavesAnotherToolsSchedule(t *testing.T) {
	h := newSchedulerHost(t)
	path := filepath.Join(h.Home, "Library", "LaunchAgents", "com.ashtonian.gitstead.plist")
	writeJob(t, path, "theirs")
	ctx, st := scheduleContext(t)
	cfg, err := ctx.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	c := &InstallAgentCmd{Name: scheduler.DefaultName, Kind: string(scheduler.Launchd), Uninstall: true}
	var refused *unmanagedError
	if _, err := c.uninstall(cfg, st, h.Host); !errors.As(err, &refused) || !strings.Contains(err.Error(), "--force removes it") {
		t.Fatalf("uninstall = %v, want it refused", err)
	}
	if _, err := os.Stat(path); err != nil || h.ran("launchctl bootout") {
		t.Fatalf("a refused uninstall removed it: %v, %v", err, h.calls)
	}
	c.Force = true
	if _, err := c.uninstall(cfg, st, h.Host); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("uninstall --force left %s", path)
	}
}

// A schedule state records is gitstead's; one of the same name in another
// backend, or of another name, is another tool's.
func TestUnmanagedSchedules(t *testing.T) {
	h := newSchedulerHost(t)
	agents := filepath.Join(h.Home, "Library", "LaunchAgents")
	writeJob(t, filepath.Join(agents, "com.ashtonian.gitstead.plist"), "ours")
	writeJob(t, filepath.Join(agents, "com.ashtonian.gitstead.work.plist"), "theirs")
	h.crontab = "*/15 * * * * gitstead sync  # com.ashtonian.gitstead\n"
	_, st := scheduleContext(t)
	st.Schedules[scheduler.DefaultName] = &state.ScheduleState{Name: scheduler.DefaultName, Kind: string(scheduler.Launchd)}

	others, err := unmanagedSchedules(h.Host, st, "")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, o := range others {
		got = append(got, o.Name+" "+string(o.Kind))
	}
	if strings.Join(got, ", ") != "default cron, work launchd" {
		t.Errorf("unmanaged = %v, want the default's crontab line and the work agent", got)
	}
	if others, _ := unmanagedSchedules(h.Host, st, "work"); len(others) != 1 || others[0].Name != "work" {
		t.Errorf("unmanaged work = %v", others)
	}
}

// doctor reports a schedule another tool installed, and doesn't call the
// background sync missing; it checks nothing of it.
func TestDoctor_ReportsAnotherToolsSchedule(t *testing.T) {
	h := newSchedulerHost(t)
	ctx, st := scheduleContext(t)
	d := &doctorReport{}
	checkSchedules(ctx, st, h.Host, d)
	if len(d.checks) != 1 || !strings.Contains(d.checks[0].Message, "no background schedule installed") {
		t.Errorf("with no schedule anywhere: %+v", d.checks)
	}

	path := filepath.Join(h.Home, "Library", "LaunchAgents", "com.ashtonian.gitstead.plist")
	writeJob(t, path, "theirs")
	d = &doctorReport{}
	checkSchedules(ctx, st, h.Host, d)
	want := "background sync com.ashtonian.gitstead is installed by another tool (" + path + "); doctor does not check its environment"
	if len(d.checks) != 1 || d.checks[0].Level != "info" || d.checks[0].Message != want || d.fails+d.warns != 0 {
		t.Errorf("checks = %+v, want one info: %s", d.checks, want)
	}
	if h.ran("launchctl") {
		t.Errorf("doctor ran %v", h.calls)
	}
}
