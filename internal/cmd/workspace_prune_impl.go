package cmd

import (
	"fmt"
	"maps"
	"os"
	"slices"

	"github.com/ashtonian/git-mgr/internal/reconcile"
	"github.com/ashtonian/git-mgr/internal/state"
	"github.com/ashtonian/git-mgr/internal/workspace"
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

	reaped, failedRemovals := 0, 0
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
			}
			reaped += len(reap)
			continue
		}
		if !c.Yes && !confirm(fmt.Sprintf("Remove %d copies of %q (clean, nothing beyond upstream)?", len(reap), name)) {
			fmt.Println("Skipped.")
			continue
		}

		removed := map[string][]string{}
		for _, l := range reap {
			archived, rerr := workspace.RemoveCopy(ctx.GoCtx(), spec.Isolation, l.Copy, cfg.Backup.ArchiveDir, workspace.ArchiveName(l), false)
			if rerr != nil {
				fmt.Fprintf(os.Stderr, "  %s: kept: %v\n", l.Label(spec), rerr)
				failedRemovals++
				continue
			}
			if archived != "" {
				fmt.Printf("  %s: archived to %s\n", l.Label(spec), tildify(archived))
			}
			removed[l.Agent] = append(removed[l.Agent], l.Path)
			reaped++
		}
		err := st.Update(cfg.LockTimeout, func(st *state.State) error {
			ws := st.Workspaces[name]
			if ws == nil {
				return nil
			}
			for agent, paths := range removed {
				workspace.DropCopies(ws, agent, paths...)
			}
			if len(ws.Replicas) == 0 {
				delete(st.Workspaces, name)
			}
			return nil
		})
		if err != nil {
			return fmt.Errorf("recording %s's removed copies: %w", name, err)
		}
		env := reconcileEnv(cfg, st)
		for _, paths := range removed {
			for _, p := range paths {
				reconcile.RemoveEmptyParents(env, p)
			}
		}
	}

	if c.DryRun {
		fmt.Printf("%d copies can be removed.\n", reaped)
		return nil
	}
	fmt.Printf("Removed %d copies.\n", reaped)
	return outcome(failedRemovals, reaped+failedRemovals, "copies could not be removed")
}
