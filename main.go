package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/alecthomas/kong"
	"github.com/ashtonian/git-mgr/internal/cmd"
	"github.com/willabides/kongplete"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	cli := &cmd.CLI{}
	parser := kong.Must(cli,
		kong.Name("git-mgr"),
		kong.Description("Discover, clone, sync, and back up git repositories; run agent fleets."),
		kong.UsageOnError(),
		kong.Vars{
			"version": fmt.Sprintf("%s (%s, built %s)", version, commit, date),
		},
	)

	// Shell completion: handles `install-completions` and, when the shell sets
	// COMP_LINE, prints candidates and exits. Predictors complete repo keys,
	// tags, sets, and workgroup names for fields tagged `predictor:"..."`.
	kongplete.Complete(parser, cmd.CompletionPredictors()...)

	ctx, err := parser.Parse(os.Args[1:])
	parser.FatalIfErrorf(err)

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
		os.Exit(cmd.ExitCode(err))
	}
}
