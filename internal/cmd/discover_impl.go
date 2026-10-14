package cmd

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/ashtonian/gitten/internal/config"
	"github.com/ashtonian/gitten/internal/discovery"
	"github.com/ashtonian/gitten/internal/reconcile"
	"github.com/ashtonian/gitten/internal/scheduler"
	"github.com/ashtonian/gitten/internal/state"
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
	doc := discoverJSON{Repos: slices.Sorted(maps.Keys(discovered)), New: len(discovered) - existing, Existing: existing}
	for _, r := range results {
		doc.Rules = append(doc.Rules, ruleJSON{Rule: r.Rule, Provider: r.Provider, Found: r.Total, Error: errString(r.Err), Warnings: r.Warnings, Incomplete: r.Incomplete})
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
	plan := reconcile.Compute(ctx.GoCtx(), cfg, st, reconcile.Scope{Discovered: discovered, Rules: scope})
	var applyErr error
	safe := 0
	if c.Apply {
		applyErr = changesOutcome(applyAuto(ctx, cfg, st, plan, reconcile.Safe), plan)
		doc.Changes = &ctx.changes
	} else {
		safe = printPlan(plan, "safe")
		doc.Plan = plan
	}

	// Detect stale — but only on a fully successful run. If any rule failed,
	// the discovered set is incomplete and marking the missing repos stale
	// (or deleting them under remove_and_delete) would be destructive.
	if discovery.AnyFailed(results) {
		fmt.Println("\nSkipping stale detection: one or more discovery rules failed.")
		doc.Stale.Skipped = true
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
				doc.Stale.Kept = append(doc.Stale.Kept, targetError{key, outcome.Kept[key].Error()})
			}
		}
		doc.Stale.NewlyStale, doc.Stale.Reapable, doc.Stale.Reaped = outcome.NewlyStale, outcome.Reapable, outcome.Reaped
	}

	if !c.Apply {
		printPreviewNext(cfg, st, plan, safe)
		return ctx.finish(doc, nil)
	}
	fmt.Printf("State updated: %d repos tracked.\n", len(st.Repos))
	return ctx.finish(doc, applyErr)
}

// printPreviewNext says that a preview changed nothing, what would make its
// changes, and whether anything will without being asked: a background sync
// applies what reconcile.auto allows.
func printPreviewNext(cfg *config.Config, st *state.State, p *reconcile.Plan, safe int) {
	if len(p.Actions) == 0 {
		return
	}
	clones := 0
	for _, a := range p.Actions {
		if a.Kind == reconcile.KindClone {
			clones++
		}
	}
	fmt.Println("\nThis was a preview: nothing was cloned or changed.")
	if clones > 0 {
		fmt.Printf("%d %s would be cloned.\n", clones, plural(clones, "repo", "repos"))
	}
	fmt.Printf("  gitten discover --apply   makes %d of the %d changes: all but moving or deleting directories\n", safe, len(p.Actions))
	fmt.Println("  gitten apply              makes them all, asking before moves and deletions")
	fmt.Println("  gitten setup              changes which repos are tracked")
	if len(st.Schedules) == 0 {
		fmt.Println("No background sync is installed, so nothing changes until you run one of these.")
		return
	}
	var every []string
	for _, name := range slices.Sorted(maps.Keys(st.Schedules)) {
		every = append(every, fmt.Sprintf("%s every %s", name, scheduler.FormatInterval(st.Schedules[name].Interval)))
	}
	auto := reconcile.AutoFilter(cfg.Reconcile.Auto)
	bySync := 0
	for _, a := range p.Actions {
		if !a.Disruptive && !a.Serial && auto(a) {
			bySync++
		}
	}
	fmt.Printf("Your background sync (%s) makes %d of them on its next run (reconcile.auto: %s).\n",
		strings.Join(every, ", "), bySync, cfg.Reconcile.Auto)
}

// discoverJSON is what discover --json prints: what each rule found, the
// plan, or with --apply what it changed, and what went stale.
type discoverJSON struct {
	Rules    []ruleJSON      `json:"rules"`
	Repos    []string        `json:"repos"`
	New      int             `json:"new"`
	Existing int             `json:"existing"`
	Plan     *reconcile.Plan `json:"plan,omitempty"`
	Changes  *changesJSON    `json:"changes,omitempty"`
	Stale    staleJSON       `json:"stale"`
}

type ruleJSON struct {
	Rule       string   `json:"rule"`
	Provider   string   `json:"provider"`
	Found      int      `json:"found"`
	Error      string   `json:"error,omitempty"`
	Warnings   []string `json:"warnings,omitempty"`
	Incomplete []string `json:"incomplete,omitempty"`
}

// staleJSON is what judging staleness did.
type staleJSON struct {
	// Skipped: a rule failed, so nothing was judged.
	Skipped    bool          `json:"skipped,omitempty"`
	NewlyStale int           `json:"newly_stale"`
	Reapable   int           `json:"past_grace_period"`
	Reaped     int           `json:"cleaned_up"`
	Kept       []targetError `json:"kept,omitempty"`
}
