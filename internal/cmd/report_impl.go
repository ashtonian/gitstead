package cmd

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/ashtonian/gitten/internal/oplog"
)

// Run summarizes operation-log activity over a window: how many clones, backups,
// removes, and workspace ops happened, plus aggregate sync totals.
func (c *ReportCmd) Run(ctx *Context) error {
	since := time.Now().Add(-c.Since)
	evs, err := oplog.Read(eventsPath(ctx), since)
	if err != nil {
		return err
	}
	if len(evs) == 0 {
		fmt.Printf("No gitten activity in the last %s.\n", c.Since)
	}

	counts := map[string]int{}
	syncRuns, fetched, backedUp, failed := 0, 0, 0, 0
	for _, e := range evs {
		counts[e.Action]++
		if e.Action == "sync" {
			syncRuns++
			for _, kv := range strings.Fields(e.Detail) {
				k, v, _ := strings.Cut(kv, "=")
				n, _ := strconv.Atoi(v)
				switch k {
				case "fetched":
					fetched += n
				case "backed_up":
					backedUp += n
				case "failed":
					failed += n
				}
			}
		}
	}

	if ctx.JSON {
		return ctx.Emit(struct {
			Since   string         `json:"since"`
			Events  int            `json:"events"`
			Actions map[string]int `json:"actions"`
			Syncs   int            `json:"syncs"`
			Fetched int            `json:"fetched"`
			Backups int            `json:"backed_up"`
			Failed  int            `json:"failed"`
		}{c.Since.String(), len(evs), counts, syncRuns, fetched, backedUp, failed})
	}
	if len(evs) == 0 {
		return nil
	}
	fmt.Printf("Activity in the last %s (%d events):\n", c.Since, len(evs))
	fmt.Printf("  clones:           %d\n", counts["clone"])
	fmt.Printf("  adopts:           %d\n", counts["adopt"])
	fmt.Printf("  backups:          %d\n", counts["backup"])
	fmt.Printf("  removes:          %d\n", counts["remove"])
	fmt.Printf("  workspaces +/-:   %d / %d\n", counts["ws_create"]+counts["create-workspace"], counts["ws_remove"]+counts["remove-workspace"])
	fmt.Printf("  sync runs:        %d (fetched %d, backed up %d, failed %d)\n", syncRuns, fetched, backedUp, failed)
	return nil
}

// Run prints the most recent raw operation-log events.
func (c *LogCmd) Run(ctx *Context) error {
	evs, err := oplog.Read(eventsPath(ctx), time.Time{})
	if err != nil {
		return err
	}
	if c.Lines > 0 && len(evs) > c.Lines {
		evs = evs[len(evs)-c.Lines:]
	}
	if ctx.JSON {
		if evs == nil {
			evs = []oplog.Event{}
		}
		return ctx.Emit(evs)
	}
	for _, e := range evs {
		fmt.Printf("%s  %-9s  %s  %s\n", e.Time.Format("2006-01-02 15:04:05"), e.Action, e.Repo, e.Detail)
	}
	return nil
}
