package cmd

import (
	"fmt"
	"maps"
	"os"
	"slices"

	"golang.org/x/sync/errgroup"

	"github.com/ashtonian/gitstead/internal/gitops"
	"github.com/ashtonian/gitstead/internal/state"
	"github.com/ashtonian/gitstead/internal/workspace"
)

// MaintainCmd gives tracked clones and workspace copies the upkeep sync
// gives them under maintenance.enabled, now, whether or not it is on.
type MaintainCmd struct {
	Repos  string `help:"Filter to repos matching glob, and their workspace copies." default:"" predictor:"repo"`
	Tag    string `help:"Filter by tag." default:"" predictor:"tag"`
	Set    string `help:"Filter by named set (from config 'sets:')." default:"" predictor:"set"`
	DryRun bool   `help:"List the temporary packs that would be removed, and change nothing." name:"dry-run"`
}

func (c *MaintainCmd) Run(ctx *Context) error {
	cfg, err := ctx.LoadConfig()
	if err != nil {
		return err
	}
	st, err := ctx.LoadState()
	if err != nil {
		return err
	}
	sel := selection{c.Repos, c.Tag, c.Set}
	pass, err := sel.predicate(cfg)
	if err != nil {
		return err
	}

	var jobs []copyJob
	for _, key := range slices.Sorted(maps.Keys(st.Repos)) {
		repo := st.Repos[key]
		if pass(key, repo) && repo.Cloned && gitops.HasGitDir(repo.LocalPath) {
			jobs = append(jobs, copyJob{label: key, repoKey: key, path: repo.LocalPath})
		}
	}
	jobs = addCopyJobs(jobs, st, sel, pass, func(ws *state.WorkspaceState, agent string, cs state.CopyState) *copyJob {
		if !ownsObjects(ws) {
			return nil
		}
		return &copyJob{label: workspace.LabelOf(ws, agent, cs.Path), workspace: ws.Name, agent: agent, path: cs.Path}
	})

	outcomes := make([]copyOutcome, len(jobs))
	var g errgroup.Group
	g.SetLimit(max(cfg.Concurrency, 1))
	for i, job := range jobs {
		g.Go(func() error {
			o := copyOutcome{job: job}
			o.upkeep, o.upkeepSkipped, o.upkeepErr = upkeep(ctx.GoCtx(), job.path, cfg.Maintenance, c.DryRun)
			outcomes[i] = o
			return nil
		})
	}
	_ = g.Wait()

	verb := "removed"
	if c.DryRun {
		verb = "would remove"
	}
	var bytes int64
	maintained, skipped, failed := 0, 0, 0
	for _, o := range outcomes {
		switch {
		case o.upkeepErr != nil:
			failed++
			fmt.Fprintf(os.Stderr, "  %s: %v\n", o.job.label, o.upkeepErr)
		case o.upkeepSkipped != "":
			skipped++
			if !ctx.Quiet {
				fmt.Printf("  %s: skipped, %s\n", o.job.label, o.upkeepSkipped)
			}
		case o.upkeep.Maintained:
			maintained++
		}
		if len(o.upkeep.Removed) == 0 {
			continue
		}
		bytes += o.upkeep.Bytes
		if !c.DryRun {
			logEvent(ctx, "maintain", o.job.repoKey, fmt.Sprintf("path=%s temp_packs=%d bytes=%d", o.job.path, len(o.upkeep.Removed), o.upkeep.Bytes))
		}
		if !ctx.Quiet {
			fmt.Printf("  %s: %s %d temporary packs, %s\n", o.job.label, verb, len(o.upkeep.Removed), gitops.FormatBytes(o.upkeep.Bytes))
		}
	}
	if !ctx.Quiet {
		if c.DryRun {
			fmt.Printf("Would remove %s of temporary packs from %d copies.\n", gitops.FormatBytes(bytes), len(jobs))
		} else {
			fmt.Printf("Upkeep: %d copies, %s of temporary packs removed, %d maintained, %d skipped (busy), %d failed.\n",
				len(jobs), gitops.FormatBytes(bytes), maintained, skipped, failed)
		}
	}
	return ctx.finish(copiesJSON(outcomes), outcome(failed, len(jobs), "copies failed upkeep"))
}
