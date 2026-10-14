package cmd

import (
	"runtime/debug"
	"testing"
)

func TestVersion(t *testing.T) {
	checkout := &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}, Settings: []debug.BuildSetting{
		{Key: "vcs.revision", Value: "4707bef"}, {Key: "vcs.time", Value: "2026-10-14T21:00:00Z"},
	}}
	for _, tt := range []struct {
		name                  string
		version, commit, date string
		info                  *debug.BuildInfo
		want                  string
	}{
		{"a release", "0.1.0", "4707bef", "2026-10-14T21:27:12Z", checkout, "0.1.0 (4707bef, built 2026-10-14T21:27:12Z)"},
		{"go install from the proxy", "dev", "none", "unknown", &debug.BuildInfo{Main: debug.Module{Version: "v0.1.0"}}, "0.1.0"},
		{"a build in a checkout", "dev", "none", "unknown", checkout, "dev (4707bef, built 2026-10-14T21:00:00Z)"},
		{"a checkout past a tag", "dev", "none", "unknown",
			&debug.BuildInfo{Main: debug.Module{Version: "v0.1.1-0.20261014210000-4707bef7e139+dirty"}, Settings: checkout.Settings},
			"0.1.1-0.20261014210000-4707bef7e139+dirty (4707bef, built 2026-10-14T21:00:00Z)"},
		{"nothing recorded", "dev", "none", "unknown", nil, "dev"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := Version(tt.version, tt.commit, tt.date, tt.info); got != tt.want {
				t.Errorf("Version = %q, want %q", got, tt.want)
			}
		})
	}
}
