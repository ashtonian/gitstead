package scheduler_test

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ashtonian/gitstead/internal/scheduler"
)

// fakeHost records the commands it is asked to run and keeps a crontab.
type fakeHost struct {
	calls   []string
	crontab string
	listErr error // what `crontab -l` fails with
}

func (f *fakeHost) run(name string, args ...string) ([]byte, error) {
	line := name + " " + strings.Join(args, " ")
	f.calls = append(f.calls, line)
	switch {
	case line == "crontab -l":
		if f.listErr != nil {
			return nil, f.listErr
		}
		return []byte(f.crontab), nil
	case name == "crontab":
		data, err := os.ReadFile(args[0])
		if err != nil {
			return nil, err
		}
		f.crontab = string(data)
	case name == "loginctl" && args[0] == "show-user":
		return []byte("no\n"), nil
	}
	return nil, nil
}

func newHost(t *testing.T, f *fakeHost) scheduler.Host {
	t.Helper()
	return scheduler.Host{
		Home: t.TempDir(), UID: 501, Run: f.run, BackupDir: filepath.Join(t.TempDir(), "backups"),
		Now:       func() time.Time { return time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC) },
		DaemonDir: t.TempDir(),
	}
}

func TestCron_KeepsOtherJobs(t *testing.T) {
	theirs := "MAILTO=me\n0 3 * * * /usr/local/bin/backup.sh\n"
	f := &fakeHost{crontab: theirs}
	h := newHost(t, f)

	if _, err := h.Install(sched(kind(scheduler.Cron)), false); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Install(sched(kind(scheduler.Cron), named("work"), every(time.Hour)), false); err != nil {
		t.Fatal(err)
	}
	// Reinstalling replaces its own line and only its own.
	if _, err := h.Install(sched(kind(scheduler.Cron), every(30*time.Minute)), false); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(f.crontab), "\n")
	if len(lines) != 4 || !strings.HasPrefix(f.crontab, theirs) {
		t.Fatalf("crontab:\n%s", f.crontab)
	}
	if !strings.HasPrefix(lines[2], "0 * * * *") || !strings.HasSuffix(lines[2], "# com.ashtonian.gitstead.work") {
		t.Errorf("work line = %s", lines[2])
	}
	if !strings.HasPrefix(lines[3], "*/30 * * * *") || !strings.HasSuffix(lines[3], "# com.ashtonian.gitstead") {
		t.Errorf("default line = %s", lines[3])
	}

	backups, _ := filepath.Glob(filepath.Join(h.BackupDir, "crontab-*.bak"))
	if len(backups) == 0 {
		t.Fatal("the crontab was changed without a backup")
	}
	if data, _ := os.ReadFile(backups[0]); !strings.HasPrefix(string(data), theirs) {
		t.Errorf("backup = %q", data)
	}

	// Removing the default schedule leaves the named one.
	if _, err := h.Uninstall(sched(kind(scheduler.Cron)), false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.crontab, "gitstead.work") || strings.Contains(f.crontab, "*/30") {
		t.Errorf("after removing the default schedule:\n%s", f.crontab)
	}
	if ok, _ := h.Installed(sched(kind(scheduler.Cron), named("work"))); !ok {
		t.Error("the work schedule should still be installed")
	}
}

// A crontab that can't be read is never overwritten: that would drop every
// other job on it.
func TestCron_UnreadableCrontabIsLeftAlone(t *testing.T) {
	f := &fakeHost{crontab: "0 3 * * * backup.sh\n", listErr: errors.New("crontab -l: exit status 1: crontab: tmp/tmp.123: Permission denied")}
	h := newHost(t, f)
	if _, err := h.Install(sched(kind(scheduler.Cron)), false); err == nil {
		t.Fatal("install over an unreadable crontab should fail")
	}
	if f.crontab != "0 3 * * * backup.sh\n" {
		t.Errorf("the crontab was replaced:\n%s", f.crontab)
	}

	// Having no crontab yet is fine.
	f = &fakeHost{listErr: errors.New("crontab -l: exit status 1: crontab: no crontab for me")}
	if _, err := newHost(t, f).Install(sched(kind(scheduler.Cron)), false); err != nil || !strings.Contains(f.crontab, "gitstead") {
		t.Errorf("install with no crontab: %v\n%s", err, f.crontab)
	}
}

func TestLaunchd_Agent(t *testing.T) {
	f := &fakeHost{}
	h := newHost(t, f)
	s := sched(named("work"))
	if _, err := h.Install(s, false); err != nil {
		t.Fatal(err)
	}
	plist := filepath.Join(h.Home, "Library", "LaunchAgents", "com.ashtonian.gitstead.work.plist")
	if data, err := os.ReadFile(plist); err != nil || !strings.Contains(string(data), "com.ashtonian.gitstead.work") {
		t.Fatalf("plist: %v\n%s", err, data)
	}
	if got := strings.Join(f.calls, "\n"); !strings.Contains(got, "launchctl bootstrap gui/501 "+plist) {
		t.Errorf("calls:\n%s", got)
	}
	if ok, _ := h.Installed(s); !ok {
		t.Error("Installed() = false after install")
	}
	if _, err := h.Uninstall(s, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(plist); !os.IsNotExist(err) {
		t.Error("uninstall left the plist")
	}
}

func TestSystemd_Install(t *testing.T) {
	f := &fakeHost{}
	h := newHost(t, f)
	if _, err := h.Install(sched(kind(scheduler.Systemd), named("work")), false); err != nil {
		t.Fatal(err)
	}
	for _, unit := range []string{"gitstead-work.service", "gitstead-work.timer"} {
		if _, err := os.Stat(filepath.Join(h.Home, ".config", "systemd", "user", unit)); err != nil {
			t.Errorf("%s not written: %v", unit, err)
		}
	}
	if got := strings.Join(f.calls, "\n"); !strings.Contains(got, "systemctl --user enable --now gitstead-work.timer") {
		t.Errorf("calls:\n%s", got)
	}
	if lingering, err := h.Lingering("me"); err != nil || lingering {
		t.Errorf("Lingering = %v, %v", lingering, err)
	}
}

func TestPrint_ChangesNothing(t *testing.T) {
	f := &fakeHost{crontab: "0 3 * * * backup.sh\n"}
	h := newHost(t, f)
	for _, k := range []scheduler.Kind{scheduler.Launchd, scheduler.Systemd, scheduler.Cron} {
		if _, err := h.Install(sched(kind(k)), true); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.calls) != 0 || f.crontab != "0 3 * * * backup.sh\n" {
		t.Errorf("a dry run ran %v", f.calls)
	}
	if entries, _ := os.ReadDir(h.Home); len(entries) != 0 {
		t.Errorf("a dry run wrote files: %v", entries)
	}
}

// Every schedule install-agent writes is found under its own name and kind,
// so a definition someone else wrote under gitstead's naming is too.
func TestDefinitions_FindWhatInstallWrites(t *testing.T) {
	f := &fakeHost{crontab: "0 3 * * * backup.sh\n"}
	h := newHost(t, f)
	installs := []scheduler.Schedule{
		sched(),
		sched(named("work")),
		sched(func(s *scheduler.Schedule) { s.Daemon, s.User, s.Home = true, "me", "/home/me" }),
		sched(kind(scheduler.Systemd)),
		sched(kind(scheduler.Systemd), named("work")),
		sched(kind(scheduler.Cron), named("nightly"), every(24*time.Hour)),
	}
	for _, s := range installs {
		if s.Daemon {
			// install would sudo; the file is what it leaves.
			if err := os.WriteFile(h.Files(s)[0], []byte(s.Plist()), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if _, err := h.Install(s, false); err != nil {
			t.Fatal(err)
		}
	}
	defs, err := h.Definitions()
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, d := range defs {
		got = append(got, fmt.Sprintf("%s %s daemon=%v %s %v", d.Name, d.Kind, d.Daemon, d.Job(), d.Where))
	}
	agents, units := filepath.Join(h.Home, "Library", "LaunchAgents"), filepath.Join(h.Home, ".config", "systemd", "user")
	want := []string{
		fmt.Sprintf("default launchd daemon=false com.ashtonian.gitstead [%s]", filepath.Join(agents, "com.ashtonian.gitstead.plist")),
		fmt.Sprintf("default launchd daemon=true com.ashtonian.gitstead [%s]", filepath.Join(h.DaemonDir, "com.ashtonian.gitstead.plist")),
		fmt.Sprintf("default systemd daemon=false gitstead.timer [%s %s]", filepath.Join(units, "gitstead.service"), filepath.Join(units, "gitstead.timer")),
		"nightly cron daemon=false com.ashtonian.gitstead.nightly [crontab]",
		fmt.Sprintf("work launchd daemon=false com.ashtonian.gitstead.work [%s]", filepath.Join(agents, "com.ashtonian.gitstead.work.plist")),
		fmt.Sprintf("work systemd daemon=false gitstead-work.timer [%s %s]", filepath.Join(units, "gitstead-work.service"), filepath.Join(units, "gitstead-work.timer")),
	}
	if !slices.Equal(got, want) {
		t.Errorf("Definitions:\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// Only gitstead's own naming counts: neighbours whose names merely start the
// same, and names no schedule can have, are someone else's business.
func TestDefinitions_OnlyGitsteadsNaming(t *testing.T) {
	f := &fakeHost{crontab: "*/5 * * * * other.sh # com.ashtonian.gitsteadish\n0 * * * * a # com.ashtonian.gitstead.Bad_Name\n" +
		"*/15 * * * * gitstead sync  # com.ashtonian.gitstead\n# */15 * * * * gitstead sync  # com.ashtonian.gitstead\n"}
	h := newHost(t, f)
	agents, units := filepath.Join(h.Home, "Library", "LaunchAgents"), filepath.Join(h.Home, ".config", "systemd", "user")
	for _, path := range []string{
		filepath.Join(agents, "com.ashtonian.gitsteadish.plist"),
		filepath.Join(agents, "com.ashtonian.gitstead.Bad_Name.plist"),
		filepath.Join(agents, "com.ashtonian.gitstead.default.plist"), // the default schedule's label is the bare prefix
		filepath.Join(agents, "com.example.other.plist"),
		filepath.Join(units, "gitsteady.service"),
		filepath.Join(units, "gitstead.path"),
		filepath.Join(units, "gitstead-work.timer"), // a timer whose service is missing is still defined
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(units, "gitstead.service.d"), 0o755); err != nil {
		t.Fatal(err)
	}
	defs, err := h.Definitions()
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, d := range defs {
		got = append(got, d.Name+" "+string(d.Kind))
	}
	if want := []string{"default cron", "work systemd"}; !slices.Equal(got, want) {
		t.Errorf("Definitions = %v, want %v", got, want)
	}
}

// A crontab that can't be read is an error, with what the files show; a
// host with no crontab command has no crontab lines.
func TestDefinitions_Crontab(t *testing.T) {
	f := &fakeHost{listErr: errors.New("crontab -l: exit status 1: Permission denied")}
	h := newHost(t, f)
	if _, err := h.Install(sched(), false); err != nil {
		t.Fatal(err)
	}
	defs, err := h.Definitions()
	if err == nil || len(defs) != 1 || defs[0].Kind != scheduler.Launchd {
		t.Errorf("Definitions = %v, %v; want the agent and the crontab's error", defs, err)
	}
	f.listErr = fmt.Errorf("crontab: %w", exec.ErrNotFound)
	if defs, err := h.Definitions(); err != nil || len(defs) != 1 {
		t.Errorf("without a crontab command: %v, %v", defs, err)
	}
}
