package scheduler_test

import (
	"strings"
	"testing"
	"time"

	"github.com/ashtonian/git-mgr/internal/scheduler"
)

func opts() scheduler.Options {
	return scheduler.Options{
		BinPath:    "/usr/local/bin/git-mgr",
		Interval:   15 * time.Minute,
		ConfigPath: "/cfg/config.yaml",
		StatePath:  "/state/state.yaml",
		LogPath:    "/log/git-mgr.log",
	}
}

func TestPlistContent(t *testing.T) {
	p := opts().PlistContent()
	for _, want := range []string{
		"<key>Label</key><string>com.ashtonian.git-mgr</string>",
		"<string>/usr/local/bin/git-mgr</string>",
		"<string>--config</string>",
		"<string>sync</string>",
		"<key>StartInterval</key><integer>900</integer>",
		"<string>/log/git-mgr.log</string>",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("plist missing %q\n%s", want, p)
		}
	}
}

func TestSystemdContent(t *testing.T) {
	o := opts()
	if want := "ExecStart=/usr/local/bin/git-mgr --config /cfg/config.yaml --state /state/state.yaml sync --quiet"; !strings.Contains(o.ServiceContent(), want) {
		t.Errorf("service ExecStart wrong:\n%s", o.ServiceContent())
	}
	tm := o.TimerContent()
	if !strings.Contains(tm, "OnUnitActiveSec=900") || !strings.Contains(tm, "WantedBy=timers.target") {
		t.Errorf("timer wrong:\n%s", tm)
	}
}

func TestCronLine(t *testing.T) {
	l := opts().CronLine()
	if !strings.HasPrefix(l, "*/15 * * * * /usr/local/bin/git-mgr") {
		t.Errorf("cron prefix/schedule wrong: %s", l)
	}
	if !strings.Contains(l, "sync --quiet") || !strings.Contains(l, "2>> /log/git-mgr.log") || !strings.HasSuffix(l, scheduler.Label) {
		t.Errorf("cron line wrong: %s", l)
	}
}

func TestDefaultKind(t *testing.T) {
	if k := scheduler.DefaultKind(); k != scheduler.Launchd && k != scheduler.Systemd && k != scheduler.Cron {
		t.Errorf("unexpected default kind %q", k)
	}
}
