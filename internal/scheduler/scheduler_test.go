package scheduler_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ashtonian/git-mgr/internal/scheduler"
)

func sched(mods ...func(*scheduler.Schedule)) scheduler.Schedule {
	s := scheduler.Schedule{
		Name:     scheduler.DefaultName,
		Kind:     scheduler.Launchd,
		Interval: 15 * time.Minute,
		Bin:      "/usr/local/bin/git-mgr",
		Global:   []string{"--config", "/cfg/config.yaml", "--state", "/state/state.yaml"},
		Env:      map[string]string{"PATH": "/opt/homebrew/bin:/usr/bin:/bin"},
		LogPath:  "/log/git-mgr.log",
	}
	for _, m := range mods {
		m(&s)
	}
	return s
}

func named(name string) func(*scheduler.Schedule) {
	return func(s *scheduler.Schedule) { s.Name = name }
}

func kind(k scheduler.Kind) func(*scheduler.Schedule) {
	return func(s *scheduler.Schedule) { s.Kind = k }
}

func every(d time.Duration) func(*scheduler.Schedule) {
	return func(s *scheduler.Schedule) { s.Interval = d }
}

func mustContain(t *testing.T, what, got string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("%s missing %q:\n%s", what, w, got)
		}
	}
}

func TestPlist(t *testing.T) {
	p := sched(func(s *scheduler.Schedule) { s.Sync = []string{"--tag", "work"} }).Plist()
	mustContain(t, "plist", p,
		"<key>Label</key><string>com.ashtonian.git-mgr</string>",
		"<string>/usr/local/bin/git-mgr</string>",
		"<string>sync</string>\n    <string>--quiet</string>\n    <string>--tag</string>\n    <string>work</string>",
		"<key>PATH</key><string>/opt/homebrew/bin:/usr/bin:/bin</string>",
		"<key>StartInterval</key><integer>900</integer>",
		"<key>ProcessType</key><string>Background</string>",
		"<key>LowPriorityIO</key><true/>",
		"<key>Nice</key><integer>10</integer>",
		"<string>/log/git-mgr.log</string>",
	)
	if strings.Contains(p, "UserName") {
		t.Errorf("an agent runs as whoever is logged in; it names no user:\n%s", p)
	}

	d := sched(func(s *scheduler.Schedule) { s.Daemon, s.User, s.Home = true, "me", "/Users/me" }).Plist()
	mustContain(t, "daemon plist", d, "<key>UserName</key><string>me</string>", "<key>HOME</key><string>/Users/me</string>")
}

func TestSystemd(t *testing.T) {
	s := sched(kind(scheduler.Systemd), named("work"), func(s *scheduler.Schedule) {
		s.Env["SSH_AUTH_SOCK"] = "/run/user/1000/ssh-agent.socket"
	})
	mustContain(t, "service", s.Service(),
		`ExecStart="/usr/local/bin/git-mgr" "--config" "/cfg/config.yaml" "--state" "/state/state.yaml" "sync" "--quiet"`,
		`Environment="PATH=/opt/homebrew/bin:/usr/bin:/bin"`,
		`Environment="SSH_AUTH_SOCK=/run/user/1000/ssh-agent.socket"`,
		"Nice=10", "IOSchedulingClass=idle",
	)
	mustContain(t, "timer", s.Timer(), "OnCalendar=*-*-* *:00/15:00", "Persistent=true", "WantedBy=timers.target")
	if strings.Contains(s.Timer(), "OnUnitActiveSec") {
		t.Error("Persistent= only catches up OnCalendar= timers")
	}

	for d, want := range map[time.Duration]string{
		time.Minute:    "*-*-* *:00/1:00",
		2 * time.Hour:  "*-*-* 00/2:00:00",
		time.Hour:      "*-*-* 00/1:00:00",
		24 * time.Hour: "*-*-* 00:00:00",
	} {
		if got := sched(kind(scheduler.Systemd), every(d)).Timer(); !strings.Contains(got, "OnCalendar="+want+"\n") {
			t.Errorf("%s: timer\n%s\nwant OnCalendar=%s", d, got, want)
		}
	}
}

func TestCronLine(t *testing.T) {
	l := sched(kind(scheduler.Cron)).CronLine()
	want := "*/15 * * * * PATH='/opt/homebrew/bin:/usr/bin:/bin' nice -n 10 '/usr/local/bin/git-mgr' '--config' '/cfg/config.yaml'" +
		" '--state' '/state/state.yaml' 'sync' '--quiet' >> '/log/git-mgr.log' 2>&1  # com.ashtonian.git-mgr"
	if l != want {
		t.Errorf("cron line\n%s\nwant\n%s", l, want)
	}
	for d, when := range map[time.Duration]string{
		time.Minute:      "* * * * * ",
		30 * time.Minute: "*/30 * * * * ",
		time.Hour:        "0 * * * * ",
		6 * time.Hour:    "0 */6 * * * ",
		24 * time.Hour:   "0 0 * * * ",
	} {
		if got := sched(kind(scheduler.Cron), every(d)).CronLine(); !strings.HasPrefix(got, when) {
			t.Errorf("%s: cron line %q, want it to start %q", d, got, when)
		}
	}
}

// Paths with spaces, quotes and the characters cron, systemd and XML treat
// specially stay one literal argument.
func TestEscaping(t *testing.T) {
	s := sched(func(s *scheduler.Schedule) {
		s.Bin = "/Apps/My Tools/git-mgr"
		s.Global = []string{"--config", "/c fg/a&b 100%$HOME.yaml"}
		s.LogPath = "/log dir/out.log"
	})
	mustContain(t, "service", s.Service(), `"/Apps/My Tools/git-mgr"`, `"/c fg/a&b 100%%$$HOME.yaml"`)
	p := s.Plist()
	if !strings.Contains(p, "a&amp;b") || strings.Contains(p, "a&b") {
		t.Errorf("plist not XML-escaped:\n%s", p)
	}
	mustContain(t, "cron line", s.CronLine(), `'/Apps/My Tools/git-mgr'`, `'/c fg/a&b 100\%$HOME.yaml'`, `>> '/log dir/out.log'`)
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name string
		mod  func(*scheduler.Schedule)
		err  string
	}{
		{"default", func(*scheduler.Schedule) {}, ""},
		{"a named schedule", named("work-2"), ""},
		{"90 minutes can't run exactly under cron or OnCalendar", every(90 * time.Minute), "can't be scheduled exactly"},
		{"neither can 7 minutes", every(7 * time.Minute), "can't be scheduled exactly"},
		{"nor 5 hours", every(5 * time.Hour), "can't be scheduled exactly"},
		{"nor seconds", every(30 * time.Second), "can't be scheduled exactly"},
		{"a name with spaces", named("my work"), "schedule name"},
		{"an uppercase name", named("Work"), "schedule name"},
		{"a relative binary", func(s *scheduler.Schedule) { s.Bin = "git-mgr" }, "not absolute"},
		{"a daemon on systemd", func(s *scheduler.Schedule) { s.Kind, s.Daemon = scheduler.Systemd, true }, "LaunchDaemon"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := sched(tt.mod).Validate()
			if tt.err == "" && err != nil || tt.err != "" && (err == nil || !strings.Contains(err.Error(), tt.err)) {
				t.Errorf("Validate() = %v, want %q", err, tt.err)
			}
		})
	}
}

func TestLabels_NamedSchedulesDontCollide(t *testing.T) {
	def, work := sched(), sched(named("work"))
	if def.Label() != "com.ashtonian.git-mgr" || work.Label() != "com.ashtonian.git-mgr.work" {
		t.Errorf("labels = %s, %s", def.Label(), work.Label())
	}
	home := "/home/me"
	if a, b := sched(kind(scheduler.Systemd)).Files(home), sched(kind(scheduler.Systemd), named("work")).Files(home); a[1] == b[1] {
		t.Errorf("both schedules use %s", a[1])
	}
}

func TestCaptureEnv(t *testing.T) {
	env := func(vars map[string]string) func(string) string {
		return func(k string) string { return vars[k] }
	}
	got, notes := scheduler.CaptureEnv(env(map[string]string{
		"PATH": "/opt/homebrew/bin:/usr/bin", "SSH_AUTH_SOCK": "/private/tmp/com.apple.launchd.x/Listeners", "GITHUB_TOKEN": "secret",
	}))
	if got["PATH"] != "/opt/homebrew/bin:/usr/bin" || got["SSH_AUTH_SOCK"] != "" || got["GITHUB_TOKEN"] != "" || len(notes) != 1 {
		t.Errorf("env = %v, notes = %v; want PATH only, and a note that the login's agent socket wasn't kept", got, notes)
	}
	if got, _ := scheduler.CaptureEnv(env(map[string]string{"SSH_AUTH_SOCK": "/var/run/com.apple.launchd.9tEq/Listeners"})); got["SSH_AUTH_SOCK"] != "" {
		t.Error("macOS's per-session agent socket changes every boot; it shouldn't be kept")
	}
	got, notes = scheduler.CaptureEnv(env(map[string]string{"PATH": "/usr/bin", "SSH_AUTH_SOCK": "/home/me/.1password/agent.sock"}))
	if got["SSH_AUTH_SOCK"] != "/home/me/.1password/agent.sock" || len(notes) != 0 {
		t.Errorf("a socket that outlives the login should be kept: %v %v", got, notes)
	}
}

func TestStableBinary(t *testing.T) {
	const (
		cellar = "/opt/homebrew/Cellar/git-mgr/1.2.0/bin/git-mgr"
		link   = "/opt/homebrew/bin/git-mgr"
		plain  = "/usr/local/bin/git-mgr"
	)
	// The Homebrew link and the Cellar binary are one file.
	same := func(a, b string) bool { return a == b || a == link && b == cellar }
	found := func(path string) func(string) (string, error) {
		return func(string) (string, error) { return path, nil }
	}
	notFound := func(string) (string, error) { return "", errors.New("not found") }

	tests := []struct {
		name     string
		exe      string
		lookPath func(string) (string, error)
		want     string // "" when refused
	}{
		{"a Homebrew install runs the link on PATH, which survives upgrades", cellar, found(link), link},
		{"a Cellar path nothing on PATH links to is refused", cellar, notFound, ""},
		{"a Nix store path is refused", "/nix/store/abc-git-mgr/bin/git-mgr", notFound, ""},
		{"so is a temporary build", "/tmp/go-build123/b001/exe/git-mgr", notFound, ""},
		{"a stable binary that isn't the one on PATH runs itself", plain, found(link), plain},
		{"a store path on PATH (a nix shell) doesn't count", cellar, found("/nix/store/abc/bin/git-mgr"), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := scheduler.StableBinary(tt.exe, tt.lookPath, same)
			if tt.want == "" && err == nil || tt.want != "" && (err != nil || got != tt.want) {
				t.Errorf("StableBinary = %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}
