package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"golang.org/x/term"

	"github.com/ashtonian/gitstead/internal/config"
	"github.com/ashtonian/gitstead/internal/scheduler"
)

// Run sets up a fresh machine end to end by orchestrating the existing commands
// on the shared context: setup (only if there is no config file or fragment; it asks on a
// terminal and takes the recommended answers otherwise) -> doctor (report,
// continue) -> discover --apply -> sync -> install-agent (unless --no-agent,
// or another tool installed the schedule).
// Each step takes/releases the state lock independently, so sequential reuse is
// safe. --profile scopes discovery rules and sync to a tag.
func (c *BootstrapCmd) Run(ctx *Context) error {
	files, err := config.FilesFor(ctx.ConfigPath())
	if err != nil {
		return err
	}
	needSetup := len(files) == 0

	if c.DryRun {
		var steps []string
		if needSetup {
			steps = append(steps, "setup")
		}
		steps = append(steps, "doctor", "discover --apply"+tagSuffix(c.Profile), "sync"+tagSuffix(c.Profile))
		if !c.NoAgent {
			st, err := ctx.LoadState()
			if err != nil {
				return err
			}
			others, err := unmanagedHere(ctx, st, c.scheduleName())
			switch {
			case err != nil:
				return fmt.Errorf("looking for a background sync another tool installed: %w", err)
			case len(others) > 0:
				steps = append(steps, fmt.Sprintf("leave the background sync %s alone: another tool installed it (%s)", others[0].Job(), definitionsWhere(others)))
			default:
				steps = append(steps, fmt.Sprintf("install-agent --interval %s%s", c.Interval, c.scheduleFlags()))
			}
		}
		for _, s := range steps {
			fmt.Println("would: " + s)
		}
		return ctx.finish(map[string][]string{"would": steps}, nil)
	}

	// Each step's result, under --json: one document of them all.
	steps := map[string]json.RawMessage{}
	step := func(name string, run func(*Context) error) error {
		fmt.Printf("\n==> %s\n", name)
		doc, err := ctx.captured(run)
		if len(doc) > 0 {
			steps[name] = doc
		}
		return err
	}
	finish := func(err error) error { return ctx.finish(steps, err) }

	if needSetup {
		// Repos are cloned and the agent installed below, with bootstrap's
		// own profile and interval.
		s := &SetupCmd{Yes: ctx.JSON || !term.IsTerminal(int(os.Stdin.Fd())), noSchedule: true, noClone: true}
		if err := step("setup", s.Run); err != nil {
			return finish(fmt.Errorf("setup: %w", err))
		}
	}

	// doctor failures are worth surfacing, but bootstrap should still try to make
	// progress (e.g. an unreachable provider shouldn't block local setup).
	if err := step("doctor", (&DoctorCmd{}).Run); err != nil {
		fmt.Fprintf(os.Stderr, "doctor reported issues (continuing): %v\n", err)
	}
	if err := step("discover", (&DiscoverCmd{Apply: true, Tag: c.Profile}).Run); err != nil {
		return finish(fmt.Errorf("discover: %w", err))
	}
	if err := step("sync", (&SyncCmd{Tag: c.Profile}).Run); err != nil {
		return finish(fmt.Errorf("sync: %w", err))
	}
	// What's left undone after this point is reported, and the run exits 3,
	// rather than stopping a setup that has already cloned and synced.
	var unfinished error
	if !c.NoAgent {
		// A profile gets a schedule of its own that syncs just its repos, so
		// bootstrapping a second profile doesn't replace the first.
		// One another tool installed is that tool's to manage: left alone.
		agent := &InstallAgentCmd{Name: c.scheduleName(), Tag: c.Profile, Interval: c.Interval}
		var theirs *unmanagedError
		switch err := step("install-agent", agent.Run); {
		case errors.As(err, &theirs):
			fmt.Printf("  left alone: %v\n", err)
		case err != nil:
			fmt.Fprintf(os.Stderr, "install-agent failed (run it later): %v\n", err)
			unfinished = &exitError{exitPartial, fmt.Errorf("bootstrapped, but installing the background sync failed: %w; run gitstead install-agent%s", err, c.scheduleFlags())}
		}
	}

	// Nudge when there's nothing to manage yet (e.g. a freshly init'd config).
	if cfg, cerr := ctx.LoadConfig(); cerr == nil && len(cfg.Providers) == 0 {
		fmt.Printf("\nNo providers configured yet — add providers + discovery rules to %s, then re-run bootstrap.\n", ctx.ConfigPath())
	}

	if unfinished != nil {
		return finish(unfinished)
	}
	fmt.Println("\nBootstrap complete.")
	return finish(nil)
}

func tagSuffix(tag string) string {
	if tag == "" {
		return ""
	}
	return " --tag " + tag
}

// scheduleName is the schedule bootstrap installs: the profile's, else the
// default.
func (c *BootstrapCmd) scheduleName() string {
	if c.Profile == "" {
		return scheduler.DefaultName
	}
	return c.Profile
}

func (c *BootstrapCmd) scheduleFlags() string {
	if c.Profile == "" {
		return ""
	}
	return fmt.Sprintf(" --name %s --tag %s", c.Profile, c.Profile)
}
