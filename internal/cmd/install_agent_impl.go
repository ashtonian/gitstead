package cmd

import (
	"fmt"
	"os"
	"time"

	"github.com/ashtonian/git-mgr/internal/config"
	"github.com/ashtonian/git-mgr/internal/scheduler"
)

// InstallAgentCmd installs (or removes) a periodic `git-mgr sync` agent using
// the host's native scheduler: launchd (macOS), a systemd user timer (Linux),
// or a crontab entry as a fallback.
type InstallAgentCmd struct {
	Interval  time.Duration `help:"How often to run sync." default:"15m"`
	Kind      string        `help:"Backend: launchd, systemd, or cron (default: per-OS)." default:"" enum:",launchd,systemd,cron"`
	Uninstall bool          `help:"Remove the agent instead of installing it."`
	Print     bool          `help:"Print the planned actions/unit files without applying them."`
	Log       string        `help:"Log file for sync output." default:"~/.local/share/git-mgr/git-mgr.log" type:"path"`
}

func (c *InstallAgentCmd) Run(ctx *Context) error {
	kind := scheduler.Kind(c.Kind)
	if c.Kind == "" {
		kind = scheduler.DefaultKind()
	}

	bin, err := os.Executable()
	if err != nil || bin == "" {
		bin = "git-mgr"
	}

	o := scheduler.Options{
		BinPath:    bin,
		Interval:   c.Interval,
		ConfigPath: ctx.Config,
		StatePath:  ctx.State,
		LogPath:    config.ExpandHome(c.Log),
	}

	verb := "install"
	apply := scheduler.Install
	if c.Uninstall {
		verb, apply = "uninstall", scheduler.Uninstall
	}
	actions, err := apply(kind, o, c.Print)
	if err != nil {
		return err
	}

	if c.Print {
		fmt.Printf("%s plan (%s):\n", verb, kind)
	} else {
		fmt.Printf("%sed git-mgr sync agent (%s):\n", verb, kind)
	}
	for _, a := range actions {
		fmt.Printf("  - %s\n", a)
	}
	if !c.Print && !c.Uninstall {
		fmt.Printf("\ngit-mgr will sync every %s. Remove with: git-mgr install-agent --uninstall\n", c.Interval)
	}
	return nil
}
