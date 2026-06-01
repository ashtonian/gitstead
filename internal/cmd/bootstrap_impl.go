package cmd

import (
	"fmt"
	"os"

	"github.com/ashtonian/git-mgr/internal/config"
)

// Run sets up a fresh machine end to end by orchestrating the existing commands
// on the shared context: init (only if the config is missing) -> doctor (report,
// continue) -> discover --apply -> sync -> install-agent (unless --no-agent).
// Each step takes/releases the state lock independently, so sequential reuse is
// safe. --profile scopes discovery rules and sync to a tag.
func (c *BootstrapCmd) Run(ctx *Context) error {
	needInit := false
	if _, err := os.Stat(config.ExpandHome(ctx.Config)); os.IsNotExist(err) {
		needInit = true
	}

	if c.DryRun {
		if needInit {
			fmt.Println("would: init (write a starter config)")
		}
		fmt.Println("would: doctor")
		fmt.Printf("would: discover --apply%s\n", tagSuffix(c.Profile))
		fmt.Printf("would: sync%s\n", tagSuffix(c.Profile))
		if !c.NoAgent {
			fmt.Printf("would: install-agent --interval %s\n", c.Interval)
		}
		return nil
	}

	if needInit {
		fmt.Println("==> init")
		if err := (&InitCmd{}).Run(ctx); err != nil {
			return fmt.Errorf("init: %w", err)
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

	fmt.Println("\nBootstrap complete.")
	return nil
}

func tagSuffix(tag string) string {
	if tag == "" {
		return ""
	}
	return " --tag " + tag
}
