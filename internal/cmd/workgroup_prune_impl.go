package cmd

import (
	"fmt"
	"os"
	"sort"

	"github.com/ashtonian/git-mgr/internal/state"
	"github.com/ashtonian/git-mgr/internal/workgroup"
)

// WGPruneCmd reaps workgroup members that hold no unique work, so a fleet's
// finished/idle agents self-clean without anyone hand-removing them.
type WGPruneCmd struct {
	Name   string `arg:"" optional:"" help:"Workgroup to prune (omit with --all)." predictor:"workgroup"`
	All    bool   `help:"Prune every created workgroup."`
	DryRun bool   `help:"Show what would be reaped without removing." name:"dry-run"`
	Yes    bool   `help:"Skip confirmation."`
}

// reapable reports whether a member can be safely removed: present, clean (no
// dirty/untracked/in-progress work), and carrying no commits beyond its upstream
// (ahead==0 with an upstream to compare against). This is the safe-teardown guard
// applied per member -- it never reaps work that isn't already saved upstream.
func reapable(mh workgroup.MemberHealth) bool {
	return mh.Present && !mh.Dirty && !mh.Untracked && mh.Stuck == "" && mh.Upstream != "" && mh.Ahead == 0
}

func (c *WGPruneCmd) Run(ctx *Context) error {
	if c.Name == "" && !c.All {
		return fmt.Errorf("specify a workgroup name or --all")
	}

	var st *state.State
	var err error
	if c.DryRun {
		st, err = ctx.LoadState()
	} else {
		var unlock func()
		st, unlock, err = lockedState(ctx)
		if err == nil {
			defer unlock()
		}
	}
	if err != nil {
		return err
	}

	var names []string
	if c.All {
		for n := range st.Workgroups {
			names = append(names, n)
		}
		sort.Strings(names)
	} else {
		names = []string{c.Name}
	}

	cfg, err := ctx.LoadConfig()
	if err != nil {
		return err
	}
	archiveDir := cfg.Backup.ArchiveDir

	reaped, failedRemovals := 0, 0
	for _, name := range names {
		ws, ok := st.Workgroups[name]
		if !ok {
			fmt.Printf("%s: not a created workgroup, skipping\n", name)
			continue
		}
		spec := workgroup.FromState(ws)
		var reap []workgroup.Member
		for _, mh := range workgroup.ComputeHealth(ctx.GoCtx(), spec).Members {
			if !reapable(mh) {
				continue
			}
			// Health is a summary; deletion needs the full inspection, and
			// anything it cannot vouch for is kept.
			if work, lost, werr := workgroup.UniqueWorkOf(ctx.GoCtx(), spec, mh.Member); werr != nil || lost {
				if werr == nil {
					fmt.Printf("  keeping %s/%s: %s\n", name, mh.Member.Name, work)
				}
				continue
			}
			reap = append(reap, mh.Member)
		}
		if len(reap) == 0 {
			continue
		}

		if c.DryRun {
			for _, m := range reap {
				fmt.Printf("  would reap %s/%s\n", name, m.Name)
			}
			reaped += len(reap)
			continue
		}

		if !c.Yes {
			fmt.Printf("Reap %d member(s) of %q (clean, nothing beyond upstream) and delete their dirs? (y/n): ", len(reap), name)
			var answer string
			fmt.Scanln(&answer)
			if answer != "y" && answer != "Y" {
				fmt.Println("Skipped.")
				continue
			}
		}

		var removed []workgroup.Member
		for _, m := range reap {
			archived, rerr := workgroup.RemoveMember(ctx.GoCtx(), spec, m, archiveDir, false)
			if rerr != nil {
				fmt.Fprintf(os.Stderr, "  %s/%s: kept: %v\n", name, m.Name, rerr)
				failedRemovals++
				continue
			}
			if archived != "" {
				fmt.Printf("  %s/%s: archived to %s\n", name, m.Name, archived)
			}
			removed = append(removed, m)
		}
		ws.Members = dropMembers(ws.Members, removed)
		if len(ws.Members) == 0 {
			workgroup.RemoveEmptyRoot(spec)
			delete(st.Workgroups, name)
		}
		reaped += len(removed)
	}

	if !c.DryRun && reaped > 0 {
		if err := st.Save(); err != nil {
			return fmt.Errorf("saving state: %w", err)
		}
	}
	if c.DryRun {
		fmt.Printf("%d member(s) reapable.\n", reaped)
		return nil
	}
	fmt.Printf("Reaped %d member(s).\n", reaped)
	return outcome(failedRemovals, reaped+failedRemovals, "members could not be removed")
}

// dropMembers returns members with the named reaped members removed.
func dropMembers(members []state.WorkgroupMemberState, reaped []workgroup.Member) []state.WorkgroupMemberState {
	gone := map[string]bool{}
	for _, m := range reaped {
		gone[m.Name] = true
	}
	var keep []state.WorkgroupMemberState
	for _, m := range members {
		if !gone[m.Name] {
			keep = append(keep, m)
		}
	}
	return keep
}
