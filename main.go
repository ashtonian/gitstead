package main

import (
	"fmt"
	"os"

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

	ver := fmt.Sprintf("%s (%s, built %s)", version, commit, date)
	if err := ctx.Run(&cmd.Context{
		Config:     cli.Config,
		State:      cli.State,
		Verbose:    cli.Verbose,
		Quiet:      cli.Quiet,
		Jobs:       cli.Jobs,
		VersionStr: ver,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
