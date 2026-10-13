package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"golang.org/x/term"

	"github.com/ashtonian/git-mgr/internal/config"
)

// Run sets up a fresh machine end to end by orchestrating the existing commands
// on the shared context: setup (only if the config is missing; it asks on a
// terminal and takes the recommended answers otherwise) -> doctor (report,
// continue) -> discover --apply -> sync -> install-agent (unless --no-agent).
// Each step takes/releases the state lock independently, so sequential reuse is
// safe. --profile scopes discovery rules and sync to a tag.
func (c *BootstrapCmd) Run(ctx *Context) error {
	needSetup := false
	if _, err := os.Stat(config.ExpandHome(ctx.Config)); os.IsNotExist(err) {
		needSetup = true
	}

	if c.DryRun {
		var steps []string
		if needSetup {
			steps = append(steps, "setup")
		}
		steps = append(steps, "doctor", "discover --apply"+tagSuffix(c.Profile), "sync"+tagSuffix(c.Profile))
		if !c.NoAgent {
			steps = append(steps, fmt.Sprintf("install-agent --interval %s%s", c.Interval, c.scheduleFlags()))
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
		// The agent is installed below, with bootstrap's own interval.
		s := &SetupCmd{Yes: ctx.JSON || !term.IsTerminal(int(os.Stdin.Fd())), noSchedule: true}
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
	if !c.NoAgent {
		// A profile gets a schedule of its own that syncs just its repos, so
		// bootstrapping a second profile doesn't replace the first.
		agent := &InstallAgentCmd{Name: c.Profile, Tag: c.Profile, Interval: c.Interval, Log: "~/.local/share/git-mgr/git-mgr.log"}
		if err := step("install-agent", agent.Run); err != nil {
			fmt.Fprintf(os.Stderr, "install-agent failed (run it later): %v\n", err)
		}
	}

	// Nudge when there's nothing to manage yet (e.g. a freshly init'd config).
	if cfg, cerr := ctx.LoadConfig(); cerr == nil && len(cfg.Providers) == 0 {
		fmt.Printf("\nNo providers configured yet — add providers + discovery rules to %s, then re-run bootstrap.\n", config.ExpandHome(ctx.Config))
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

func (c *BootstrapCmd) scheduleFlags() string {
	if c.Profile == "" {
		return ""
	}
	return fmt.Sprintf(" --name %s --tag %s", c.Profile, c.Profile)
}
