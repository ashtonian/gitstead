package cmd

import (
	"fmt"
	"maps"
	"slices"

	"github.com/ashtonian/git-mgr/internal/discovery"
	"github.com/ashtonian/git-mgr/internal/reconcile"
)

func (c *DiscoverCmd) Run(ctx *Context) error {
	cfg, err := ctx.LoadConfig()
	if err != nil {
		return err
	}

	// Applying locks the state per change, so this reads it unlocked.
	st, err := ctx.LoadState()
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
		for _, why := range r.Incomplete {
			fmt.Printf("      incomplete, so none of its repos are judged stale: %s\n", why)
		}
	}
	fmt.Printf("\n%d new, %d existing\n\n", len(discovered)-existing, existing)

	// Bring repos in line before judging staleness, so a renamed repo is
	// rekeyed rather than marked stale under its old name.
	scope := discovery.StaleScopeOf(results, c.Rule, c.Tag)
	plan := reconcile.Compute(ctx.GoCtx(), cfg, st, reconcile.Scope{Discovered: discovered, Rules: scope, Repos: true})
	var applyErr error
	if c.Apply {
		applyErr = changesOutcome(applyAuto(ctx, cfg, st, plan, reconcile.Safe), plan)
	} else {
		printPlan(plan, "safe")
	}

	// Detect stale — but only on a fully successful run. If any rule failed,
	// the discovered set is incomplete and marking the missing repos stale
	// (or deleting them under remove_and_delete) would be destructive.
	if discovery.AnyFailed(results) {
		fmt.Println("\nSkipping stale detection: one or more discovery rules failed.")
	} else {
		cleanup := staleCleanup(ctx, cfg, st)
		var outcome discovery.CleanupResult
		if c.Apply {
			var cerr error
			if outcome, cerr = cleanup.Run(st, discoveredKeys(discovered), scope); cerr != nil {
				return fmt.Errorf("recording stale repos: %w", cerr)
			}
		} else {
			outcome = cleanup.Preview(st, discoveredKeys(discovered), scope)
		}
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
		if len(outcome.Kept) > 0 {
			fmt.Printf("warning: %d stale repos kept their files and stay tracked:\n", len(outcome.Kept))
			for _, key := range slices.Sorted(maps.Keys(outcome.Kept)) {
				fmt.Printf("  %s: %v\n", key, outcome.Kept[key])
			}
		}
	}

	if !c.Apply {
		fmt.Println("\nUse --apply to make the changes that move and delete nothing, or `git-mgr apply` for all of them.")
		return nil
	}
	fmt.Printf("State updated: %d repos tracked.\n", len(st.Repos))
	return applyErr
}
