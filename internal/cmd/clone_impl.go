package cmd

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/ashtonian/git-mgr/internal/config"
	"github.com/ashtonian/git-mgr/internal/reconcile"
	"github.com/ashtonian/git-mgr/internal/repourl"
	"github.com/ashtonian/git-mgr/internal/state"
)

func (c *CloneCmd) Run(ctx *Context) error {
	cfg, err := ctx.LoadConfig()
	if err != nil {
		return err
	}
	st, err := ctx.LoadState()
	if err != nil {
		return err
	}

	if err := repourl.CheckNoCredentials(c.URL); err != nil {
		return err
	}
	id, err := repourl.Parse(c.URL)
	if err != nil {
		return fmt.Errorf("parsing URL: %w", err)
	}
	if _, exists := st.Repos[id.Key()]; exists {
		return fmt.Errorf("repo %s is already tracked", id.Key())
	}

	dest := c.Path
	if dest == "" {
		if dest, err = reconcile.TemplatePath(cfg, id.Key(), id.Host, id.Owner, id.Name, id.FullName); err != nil {
			return err
		}
	} else if dest, err = filepath.Abs(config.ExpandHome(dest)); err != nil {
		return err
	}
	return ctx.finishChanges(addByHand(ctx, cfg, st, reconcile.HandAdded(cfg, id, dest, splitTags(c.Tag)), false))
}

// addByHand plans tracking one repo added by hand -- cloning it, or adopting
// the clone already at its path -- and applies the plan unless dryRun. A repo
// it tracks goes in the shared manifest, if config names one, so every
// machine reading it has the repo too.
func addByHand(ctx *Context, cfg *config.Config, st *state.State, d reconcile.Repo, dryRun bool) error {
	desired := map[string]reconcile.Repo{d.Key: d}
	seen := reconcile.Observe(ctx.GoCtx(), reconcile.RepoPaths(desired, st), 1)
	plan := reconcile.PlanRepos(desired, st, seen, false)
	if dryRun {
		printPlan(plan, "all")
		return blockedErr(plan)
	}
	printDriftAndBlocked(plan)
	results := plan.Apply(ctx.GoCtx(), reconcileEnv(cfg, st), reconcile.All)
	ctx.noteChanges(plan, results)
	printResults(results, resultsEach)
	return errors.Join(changesOutcome(results, plan), recordInManifest(cfg, st, d.Key))
}

func splitTags(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}
