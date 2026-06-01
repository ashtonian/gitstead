package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/alecthomas/kong"
	"github.com/ashtonian/git-mgr/internal/cmd"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	cli := &cmd.CLI{}
	ctx := kong.Parse(cli,
		kong.Name("git-mgr"),
		kong.Description("Discover, clone, sync, and back up git repositories."),
		kong.UsageOnError(),
		kong.Vars{
			"version": fmt.Sprintf("%s (%s, built %s)", version, commit, date),
		},
	)

	// Cancel in-flight git/API operations on Ctrl-C / SIGTERM. A second signal
	// restores default behavior so a stuck run can always be force-killed.
	rootCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	ver := fmt.Sprintf("%s (%s, built %s)", version, commit, date)
	if err := ctx.Run(&cmd.Context{
		Config:     cli.Config,
		State:      cli.State,
		Verbose:    cli.Verbose,
		Quiet:      cli.Quiet,
		Jobs:       cli.Jobs,
		VersionStr: ver,
		Ctx:        rootCtx,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
