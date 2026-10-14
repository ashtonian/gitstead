package cmd

import (
	"fmt"
	"maps"
	"slices"

	"github.com/ashtonian/gitten/internal/workspace"
)

// WSPruneCmd removes copies that hold no unique work, so finished or idle
// agents' copies clean themselves up.
type WSPruneCmd struct {
	Name   string `arg:"" optional:"" help:"Workspace to prune (omit with --all)." predictor:"workspace"`
	All    bool   `help:"Prune every workspace."`
	DryRun bool   `help:"Show what would be removed without removing it." name:"dry-run"`
	Yes    bool   `help:"Skip confirmation."`
}

// reapable reports whether a copy can be removed without losing anything:
// present, clean, nothing under way, and nothing beyond an upstream it has.
func reapable(h workspace.CopyHealth) bool {
	return h.Present && !h.Dirty && !h.Untracked && h.Stuck == "" && h.Upstream != "" && h.Ahead == 0
}

func (c *WSPruneCmd) Run(ctx *Context) error {
	if c.Name == "" && !c.All {
		return fmt.Errorf("give a workspace name or --all")
	}
	cfg, err := ctx.LoadConfig()
	if err != nil {
		return err
	}
	// Unlocked through inspection, prompts and removals; each workspace's
	// removals are recorded with one locked write.
	st, err := ctx.LoadState()
	if err != nil {
		return err
	}
	names := []string{c.Name}
	if c.All {
		names = slices.Sorted(maps.Keys(st.Workspaces))
	}

	if !c.Yes && !c.DryRun && ctx.JSON {
		return errNeedsYes("ws prune")
	}
	reaped, failedRemovals := 0, 0
	var doc retiredJSON
	var would []copyRefJSON
	for _, name := range names {
		ws, ok := st.Workspaces[name]
		if !ok {
			fmt.Printf("%s: not a created workspace, skipping\n", name)
			continue
		}
		spec := workspace.FromState(cfg, st, ws)
		if spec.Shared() {
			continue
		}
		var reap []workspace.Located
		for _, h := range workspace.Health(ctx.GoCtx(), spec, spec.Copies()) {
			if !reapable(h) {
				continue
			}
			// Health is a summary; deletion needs the full inspection, and
			// anything it can't vouch for is kept.
			if work, lost, werr := workspace.UniqueWorkOf(ctx.GoCtx(), spec.Isolation, h.Copy); werr != nil || lost {
				if werr == nil {
					fmt.Printf("  keeping %s: %s\n", h.Label(spec), work)
				}
				continue
			}
			reap = append(reap, h.Located)
		}
		if len(reap) == 0 {
			continue
		}
		if c.DryRun {
			for _, l := range reap {
				fmt.Printf("  would remove %s\n", l.Label(spec))
				would = append(would, copyRefJSON{l.Label(spec), l.Path})
			}
			reaped += len(reap)
			continue
		}
		if !c.Yes && !confirm(fmt.Sprintf("Remove %d copies of %q (clean, nothing beyond upstream)?", len(reap), name)) {
			fmt.Println("Skipped.")
			continue
		}

		byAgent := map[string][]string{}
		for _, l := range reap {
			byAgent[l.Agent] = append(byAgent[l.Agent], l.Path)
		}
		retired := map[string]workspace.Retired{}
		opts := workspace.RetireOptions{StateDir: st.Dir(), ArchiveDir: cfg.Backup.ArchiveDir, Timeout: cfg.Sync.CloneTimeout}
		for _, agent := range slices.Sorted(maps.Keys(byAgent)) {
			r := workspace.Retire(ctx.GoCtx(), spec, ws, agent, byAgent[agent], opts)
			retired[agent] = r
			reaped += len(r.Removed)
			failedRemovals += len(r.Kept)
			reportRetired(spec, r, ctx.Quiet)
			doc.add(spec, r)
		}
		if err := recordRetired(cfg, st, name, retired, true); err != nil {
			return fmt.Errorf("recording %s's removed copies: %w", name, err)
		}
	}

	if c.DryRun {
		fmt.Printf("%d copies can be removed.\n", reaped)
		if would == nil {
			would = []copyRefJSON{}
		}
		return ctx.finish(map[string][]copyRefJSON{"would_remove": would}, nil)
	}
	fmt.Printf("Removed %d copies.\n", reaped)
	if doc.Removed == nil {
		doc.Removed = []copyRefJSON{}
	}
	return ctx.finish(doc, outcome(failedRemovals, reaped+failedRemovals, "copies could not be removed"))
}
