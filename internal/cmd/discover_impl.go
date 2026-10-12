package cmd

import (
	"fmt"

	"github.com/ashtonian/git-mgr/internal/discovery"
	"github.com/ashtonian/git-mgr/internal/reconcile"
	"github.com/ashtonian/git-mgr/internal/state"
)

func (c *DiscoverCmd) Run(ctx *Context) error {
	cfg, err := ctx.LoadConfig()
	if err != nil {
		return err
	}

	// --apply mutates and saves state, so hold the lock (and re-read under it)
	// for that path; a dry-run preview stays lock-free.
	var st *state.State
	if c.Apply {
		var unlock func()
		st, unlock, err = lockedState(ctx)
		if err == nil {
			defer unlock()
		}
	} else {
		st, err = ctx.LoadState()
	}
	if err != nil {
		return err
	}

	results, discovered, err := discovery.RunDiscovery(ctx.GoCtx(), cfg, c.Rule, c.Tag)
	if err != nil {
		return err
	}

	existing := 0
	for key := range discovered {
		if _, ok := st.Repos[key]; ok {
			existing++
		}
	}
	for _, r := range results {
		if r.Err != nil {
			fmt.Printf("  [%s] %s: ERROR: %v\n", r.Provider, r.Rule, r.Err)
			continue
		}
		fmt.Printf("  [%s] %s: found %d repos\n", r.Provider, r.Rule, r.Total)
		for _, w := range r.Warnings {
			fmt.Printf("      warning: %s\n", w)
		}
	}
	fmt.Printf("\n%d new, %d existing\n\n", len(discovered)-existing, existing)

	// Bring repos in line before judging staleness, so a renamed repo is
	// rekeyed rather than marked stale under its old name.
	scope := discovery.StaleScopeOf(results, c.Rule, c.Tag)
	plan := reconcile.Compute(ctx.GoCtx(), cfg, st, reconcile.Scope{Discovered: discovered, Rules: scope, Repos: true})
	var applyErr error
	if c.Apply {
		applyErr = applyAuto(ctx, cfg, st, plan, reconcile.Safe)
	} else {
		printPlan(plan, "safe")
	}

	// Detect stale — but only on a fully successful run. If any rule failed,
	// the discovered set is incomplete and marking the missing repos stale
	// (or deleting them under remove_and_delete) would be destructive.
	if discovery.AnyFailed(results) {
		fmt.Println("\nSkipping stale detection: one or more discovery rules failed.")
	} else {
		discoveredKeys := make(map[string]bool)
		for k := range discovered {
			discoveredKeys[k] = true
		}
		outcome := discovery.DetectStale(st, discoveredKeys, scope, cfg.Cleanup, c.Apply,
			archivingRemover(ctx.GoCtx(), ctx.Logger(), cfg.Backup.ArchiveDir))
		if outcome.NewlyStale > 0 {
			fmt.Printf("\n%d newly stale repos\n", outcome.NewlyStale)
		}
		if !c.Apply && outcome.Reapable > 0 {
			fmt.Printf("%d stale repos are past the grace period; run with --apply to apply cleanup policy %q\n",
				outcome.Reapable, cfg.Cleanup.Policy)
		}
		if c.Apply && outcome.Reaped > 0 {
			fmt.Printf("cleaned up %d stale repos (policy %q)\n", outcome.Reaped, cfg.Cleanup.Policy)
		}
		if c.Apply && outcome.Failed > 0 {
			fmt.Printf("warning: %d stale repos could not be deleted (left tracked)\n", outcome.Failed)
		}
	}

	if !c.Apply {
		fmt.Println("\nUse --apply to make the changes that move and delete nothing, or `git-mgr apply` for all of them.")
		return nil
	}
	if err := st.Save(); err != nil {
		return fmt.Errorf("saving state: %w", err)
	}
	fmt.Printf("State updated: %d repos tracked.\n", len(st.Repos))
	return applyErr
}
