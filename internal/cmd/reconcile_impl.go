package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ashtonian/git-mgr/internal/config"
	"github.com/ashtonian/git-mgr/internal/discovery"
	"github.com/ashtonian/git-mgr/internal/reconcile"
	"github.com/ashtonian/git-mgr/internal/repourl"
	"github.com/ashtonian/git-mgr/internal/state"
	"golang.org/x/term"
)

// PlanCmd shows what applying config would change, without changing anything.
type PlanCmd struct {
	JSON bool `help:"Print the plan as JSON."`
}

// ApplyCmd makes repos and workgroups match config, including moves and
// removals that sync leaves for an explicit run.
type ApplyCmd struct {
	Yes bool `help:"Don't ask before moving or removing directories."`
}

func (c *PlanCmd) Run(ctx *Context) error {
	cfg, err := ctx.LoadConfig()
	if err != nil {
		return err
	}
	st, err := ctx.LoadState()
	if err != nil {
		return err
	}
	plan, _, err := fullPlan(ctx, cfg, st)
	if err != nil {
		return err
	}
	if c.JSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(plan)
	}
	printPlan(plan, cfg.Reconcile.Auto)
	return nil
}

func (c *ApplyCmd) Run(ctx *Context) error {
	cfg, err := ctx.LoadConfig()
	if err != nil {
		return err
	}
	// Each change takes the state lock for itself, so nothing is held while
	// you read the plan.
	st, err := ctx.LoadState()
	if err != nil {
		return err
	}

	plan, scope, err := fullPlan(ctx, cfg, st)
	if err != nil {
		return err
	}
	printPlan(plan, "all")
	if len(plan.Actions) == 0 {
		return blockedErr(plan)
	}
	if n := plan.Disruptive(); n > 0 && !c.Yes {
		if !confirm(fmt.Sprintf("Move or remove the %d %s marked > and - above?", n, plural(n, "directory", "directories"))) {
			fmt.Println("Cancelled; nothing changed.")
			return nil
		}
	}
	results := reconcile.ApplyPlan(ctx.GoCtx(), cfg, reconcileEnv(cfg, st), plan, scope, reconcile.All)
	logResults(ctx, results)
	printResults(results, resultsSummary)
	return changesOutcome(results, plan)
}

// fullPlan discovers and plans every repo and workgroup.
func fullPlan(ctx *Context, cfg *config.Config, st *state.State) (*reconcile.Plan, reconcile.Scope, error) {
	results, discovered, err := discovery.RunDiscovery(ctx.GoCtx(), cfg, "", "")
	if err != nil {
		return nil, reconcile.Scope{}, err
	}
	for _, r := range results {
		if r.Err != nil {
			fmt.Fprintf(os.Stderr, "warning: rule %s failed, its repos are left as they are: %v\n", r.Rule, r.Err)
		}
	}
	scope := reconcile.Scope{
		Discovered: discovered, Rules: discovery.StaleScopeOf(results, "", ""), Repos: true, Workgroups: true,
	}
	return reconcile.Compute(ctx.GoCtx(), cfg, st, scope), scope, nil
}

// reconcileEnv is what plans apply to: st, changed one action at a time under
// the state lock, so other git-mgr runs keep working while a plan applies.
// The caller must not hold the state lock itself.
func reconcileEnv(cfg *config.Config, st *state.State) *reconcile.Env {
	keep := []string{config.ExpandHome(cfg.BaseDir), config.ExpandHome(cfg.WorkgroupBaseDir)}
	for _, o := range cfg.Overrides {
		if o.BaseDir != "" {
			keep = append(keep, config.ExpandHome(o.BaseDir))
		}
	}
	return &reconcile.Env{
		State:        st,
		Update:       stateUpdater(cfg, st),
		ArchiveDir:   cfg.Backup.ArchiveDir,
		KeepDirs:     keep,
		CloneTimeout: cfg.Sync.CloneTimeout,
	}
}

// applyAuto applies what reconcile.auto allows, prints what happened and
// what is left, and returns the failures.
func applyAuto(ctx *Context, cfg *config.Config, st *state.State, plan *reconcile.Plan, include func(reconcile.Action) bool) []reconcile.Result {
	results := plan.Apply(ctx.GoCtx(), reconcileEnv(cfg, st), include)
	logResults(ctx, results)
	if ctx.Quiet {
		printResults(results, resultsQuiet)
		return results
	}
	printResults(results, resultsEach)
	printPending(plan, include)
	return results
}

// changesOutcome is the outcome of applying a plan: each failed or blocked
// change counts against the changes attempted.
func changesOutcome(results []reconcile.Result, plan *reconcile.Plan) error {
	return outcome(failedChanges(results)+len(plan.Blocked), len(results)+len(plan.Blocked), "changes failed or could not be made")
}

func failedChanges(results []reconcile.Result) int {
	n := 0
	for _, r := range results {
		if r.Err != nil {
			n++
		}
	}
	return n
}

func confirm(question string) bool {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Println("Input is not a terminal; re-run with --yes to apply.")
		return false
	}
	fmt.Printf("%s (y/n): ", question)
	var answer string
	fmt.Scanln(&answer)
	return answer == "y" || answer == "Y"
}

// printPlan lists the plan, marking what sync won't apply on its own under
// reconcile.auto (pass "all" when the caller applies everything).
func printPlan(p *reconcile.Plan, auto string) {
	if p.Empty() {
		fmt.Println("Nothing to change: disk and state match config.")
		return
	}
	bySync := reconcile.AutoFilter(auto)
	automatic := 0
	if len(p.Actions) > 0 {
		fmt.Println("Changes:")
		for _, a := range p.Actions {
			line := describeAction(a)
			switch {
			case auto == "all":
			case a.Disruptive:
				line += "   [needs git-mgr apply]"
			case a.Workgroup():
				line += "   [git-mgr apply or wg ensure]"
			case bySync(a):
				automatic++
			default:
				line += "   [needs git-mgr apply]"
			}
			fmt.Println(line)
		}
	}
	printDriftAndBlocked(p)
	if auto != "all" && len(p.Actions) > 0 {
		fmt.Printf("\nsync applies %d of %d on its own (reconcile.auto: %s); `git-mgr apply` makes them all.\n", automatic, len(p.Actions), auto)
	}
}

// printPending lists what a run left for `git-mgr apply`, and any drift or
// blocked changes.
func printPending(p *reconcile.Plan, applied func(reconcile.Action) bool) {
	var pending []reconcile.Action
	for _, a := range p.Actions {
		if !applied(a) {
			pending = append(pending, a)
		}
	}
	if len(pending) > 0 {
		fmt.Println("Waiting for `git-mgr apply`:")
		for _, a := range pending {
			fmt.Println(describeAction(a))
		}
	}
	printDriftAndBlocked(p)
}

func printDriftAndBlocked(p *reconcile.Plan) {
	if len(p.Drift) > 0 {
		fmt.Println("Changed by hand, left alone (reconcile.drift: overwrite restores them):")
		for _, d := range p.Drift {
			fmt.Printf("  ! %s %s: git-mgr set %s, now %s; config wants %s\n", d.Target, d.What, tildify(d.Recorded), tildify(d.Found), tildify(d.Wanted))
		}
	}
	if len(p.Blocked) > 0 {
		fmt.Println("Not changed:")
		for _, b := range p.Blocked {
			fmt.Printf("  x %s: %s\n", b.Target, tildify(b.Reason))
		}
	}
}

// resultStyle is how much printResults shows.
type resultStyle int

const (
	resultsQuiet   resultStyle = iota // failures only
	resultsEach                       // every action, for runs that didn't print the plan
	resultsSummary                    // failures, notes and a count, after the plan was printed
)

// printResults prints how applied actions went.
func printResults(results []reconcile.Result, style resultStyle) {
	done := 0
	for _, r := range results {
		line := describeAction(r.Action)
		switch {
		case errors.Is(r.Err, reconcile.ErrSkipped):
			if style != resultsQuiet {
				fmt.Println(line + "   skipped")
			}
		case r.Err != nil:
			fmt.Fprintln(os.Stderr, line+"   FAILED: "+tildify(r.Err.Error()))
		case r.Info != "":
			done++
			if style != resultsQuiet {
				fmt.Println(line + "   " + tildify(r.Info))
			}
		default:
			done++
			if style == resultsEach {
				fmt.Println(line)
			}
		}
	}
	if style == resultsSummary && len(results) > 0 {
		fmt.Printf("Applied %d of %d %s.\n", done, len(results), plural(len(results), "change", "changes"))
	}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func blockedErr(p *reconcile.Plan) error {
	return outcome(len(p.Blocked), len(p.Blocked), "changes could not be made")
}

func logResults(ctx *Context, results []reconcile.Result) {
	for _, r := range results {
		if r.Err == nil {
			logEvent(ctx, string(r.Action.Kind), r.Action.Target, strings.TrimSpace(r.Action.From+" -> "+r.Action.To))
		}
	}
}

func describeAction(a reconcile.Action) string {
	symbol := "~"
	switch a.Kind {
	case reconcile.KindClone, reconcile.KindAdopt, reconcile.KindCreateWorkgroup, reconcile.KindCreateMember:
		symbol = "+"
	case reconcile.KindRemoveMember, reconcile.KindRemoveWorkgroup, reconcile.KindForgetMember:
		symbol = "-"
	case reconcile.KindMove, reconcile.KindMoveMember:
		symbol = ">"
	}
	line := fmt.Sprintf("  %s %-16s %s", symbol, a.Kind, a.Target)
	if a.Field != "" {
		line += " " + a.Field
	}
	switch {
	case a.From != "" && a.To != "":
		line += ": " + tildify(a.From) + " -> " + tildify(a.To)
	case a.To != "":
		line += ": " + tildify(a.To)
	case a.From != "":
		line += ": " + tildify(a.From)
	}
	if a.Note != "" {
		line += " (" + a.Note + ")"
	}
	return line
}

// tildify prepares text for display: credentials in URLs hidden, paths under
// the home directory shortened.
func tildify(s string) string {
	s = repourl.Redact(s)
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return s
	}
	return strings.ReplaceAll(s, filepath.Clean(home)+string(filepath.Separator), "~"+string(filepath.Separator))
}
