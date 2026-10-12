package cmd

import (
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
		if needSetup {
			fmt.Println("would: setup (asking on a terminal, else taking the recommended answers)")
		}
		fmt.Println("would: doctor")
		fmt.Printf("would: discover --apply%s\n", tagSuffix(c.Profile))
		fmt.Printf("would: sync%s\n", tagSuffix(c.Profile))
		if !c.NoAgent {
			fmt.Printf("would: install-agent --interval %s\n", c.Interval)
		}
		return nil
	}

	if needSetup {
		fmt.Println("==> setup")
		// The agent is installed below, with bootstrap's own interval.
		s := &SetupCmd{Yes: !term.IsTerminal(int(os.Stdin.Fd())), noSchedule: true}
		if err := s.Run(ctx); err != nil {
			return fmt.Errorf("setup: %w", err)
		}
	}

	// doctor failures are worth surfacing, but bootstrap should still try to make
	// progress (e.g. an unreachable provider shouldn't block local setup).
	fmt.Println("\n==> doctor")
	if err := (&DoctorCmd{}).Run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "doctor reported issues (continuing): %v\n", err)
	}

	fmt.Println("\n==> discover --apply")
	if err := (&DiscoverCmd{Apply: true, Tag: c.Profile}).Run(ctx); err != nil {
		return fmt.Errorf("discover: %w", err)
	}

	fmt.Println("\n==> sync")
	if err := (&SyncCmd{Tag: c.Profile}).Run(ctx); err != nil {
		return fmt.Errorf("sync: %w", err)
	}

	if !c.NoAgent {
		fmt.Println("\n==> install-agent")
		agent := &InstallAgentCmd{Interval: c.Interval, Log: "~/.local/share/git-mgr/git-mgr.log"}
		if err := agent.Run(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "install-agent failed (run it later): %v\n", err)
		}
	}

	// Nudge when there's nothing to manage yet (e.g. a freshly init'd config).
	if cfg, cerr := ctx.LoadConfig(); cerr == nil && len(cfg.Providers) == 0 {
		fmt.Printf("\nNo providers configured yet — add providers + discovery rules to %s, then re-run bootstrap.\n", config.ExpandHome(ctx.Config))
	}

	fmt.Println("\nBootstrap complete.")
	return nil
}

func tagSuffix(tag string) string {
	if tag == "" {
		return ""
	}
	return " --tag " + tag
}
