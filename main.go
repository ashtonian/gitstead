package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/alecthomas/kong"
	"github.com/ashtonian/gitstead/internal/cmd"
	"github.com/willabides/kongplete"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	cli := &cmd.CLI{}
	options := []kong.Option{
		kong.Name("gitstead"),
		kong.Description("Clone and keep in sync every repo you work on: whole orgs, users or searches, across accounts and forges."),
		kong.UsageOnError(),
		kong.Vars{
			"version": fmt.Sprintf("%s (%s, built %s)", version, commit, date),
		},
	}
	// Under --json, stdout carries only the one document: help and usage go
	// to stderr, and help, which exits 0, is the document too. A shell asking
	// for completions passes its own arguments, so it is left alone.
	wantsJSON := cmd.WantsJSON(os.Args[1:]) && os.Getenv("COMP_LINE") == ""
	if wantsJSON {
		var help bytes.Buffer
		options = append(options,
			kong.Writers(io.MultiWriter(os.Stderr, &help), os.Stderr),
			kong.Exit(func(code int) {
				if code == 0 {
					cmd.HelpDocument(os.Stdout, help.String())
				}
				os.Exit(code)
			}))
	}
	parser := kong.Must(cli, options...)

	// Shell completion: handles `install-completions` and, when the shell sets
	// COMP_LINE, prints candidates and exits. Predictors complete repo keys,
	// tags, sets, and workspace names for fields tagged `predictor:"..."`.
	kongplete.Complete(parser, cmd.CompletionPredictors()...)

	ctx, err := parser.Parse(os.Args[1:])
	if err != nil && wantsJSON {
		cmd.UsageError(os.Stdout, err)
	}
	parser.FatalIfErrorf(err)

	// Cancel in-flight git/API operations on Ctrl-C / SIGTERM. A second signal
	// restores default behavior so a stuck run can always be force-killed.
	rootCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	ver := fmt.Sprintf("%s (%s, built %s)", version, commit, date)
	run := &cmd.Context{
		Config:     cli.Config,
		State:      cli.State,
		Verbose:    cli.Verbose,
		Quiet:      cli.Quiet,
		Jobs:       cli.Jobs,
		VersionStr: ver,
		Ctx:        rootCtx,
		JSON:       cli.JSON,
	}
	if cli.JSON {
		// stdout carries only the result; what commands print for people
		// goes to stderr, as quietly as --quiet makes it.
		run.Out, os.Stdout = os.Stdout, os.Stderr
		run.Quiet = true
	}
	run.Logger() // before the command, so everything logs by config's log settings
	err = ctx.Run(run)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
	}
	if code := cmd.Conclude(run, err); code != 0 {
		os.Exit(code)
	}
}
