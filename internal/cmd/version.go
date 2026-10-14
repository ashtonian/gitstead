package cmd

import (
	"fmt"
	"runtime/debug"
	"strings"
)

// VersionCmd prints version information.
type VersionCmd struct {
	Version string `kong:"-"` // injected, not a flag
}

func (v *VersionCmd) Run(ctx *Context) error {
	fmt.Println("gitten " + ctx.VersionStr)
	return ctx.finish(map[string]string{"version": ctx.VersionStr}, nil)
}

// Version is gitten's version as `gitten version` prints it. A release's
// comes from the build flags GoReleaser sets; any other build's from what the
// Go toolchain recorded: the module version `go install ...@v0.1.0` built,
// and a checkout's commit and its time.
func Version(version, commit, date string, info *debug.BuildInfo) string {
	if version == "dev" && info != nil {
		if v := info.Main.Version; v != "" && v != "(devel)" {
			version = strings.TrimPrefix(v, "v")
		}
		for _, s := range info.Settings {
			switch {
			case s.Key == "vcs.revision" && commit == "none":
				commit = s.Value
			case s.Key == "vcs.time" && date == "unknown":
				date = s.Value
			}
		}
	}
	if commit == "none" && date == "unknown" {
		return version
	}
	return fmt.Sprintf("%s (%s, built %s)", version, commit, date)
}
