package cmd

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/ashtonian/gitten/internal/gitops"
	"github.com/ashtonian/gitten/internal/state"
	"golang.org/x/sync/errgroup"
)

// liveRepo is a repo's working-tree state inspected live (off disk, no network).
type liveRepo struct {
	Key      string `json:"key"`
	State    string `json:"state"` // clean|dirty|conflict|missing|error
	Branch   string `json:"branch,omitempty"`
	Upstream string `json:"upstream,omitempty"`
	Ahead    int    `json:"ahead"`
	Behind   int    `json:"behind"`
	Tags     string `json:"tags,omitempty"`
}

// runLive inspects every matching repo's working tree concurrently (dirty state
// + ahead/behind vs upstream) instead of rendering the cached state. It is
// read-only and makes no network calls, so it answers "what needs attention
// right now?" without a sync.
func (c *StatusCmd) runLive(ctx *Context, st *state.State, pass func(string, *state.RepoState) bool) error {
	type cand struct {
		key  string
		repo *state.RepoState
	}
	var cands []cand
	for key, repo := range st.Repos {
		if !pass(key, repo) || !repo.Cloned {
			continue
		}
		cands = append(cands, cand{key, repo})
	}

	concurrency := 8
	if cfg, err := ctx.LoadConfig(); err == nil && cfg.Concurrency > 0 {
		concurrency = cfg.Concurrency
	}

	rows := make([]liveRepo, len(cands))
	g, gctx := errgroup.WithContext(ctx.GoCtx())
	sem := make(chan struct{}, concurrency)
	for i, cd := range cands {
		i, cd := i, cd
		g.Go(func() error {
			sem <- struct{}{}
			defer func() { <-sem }()
			row := liveRepo{Key: cd.key, Tags: strings.Join(cd.repo.Tags, ",")}
			switch {
			case !gitops.HasGitDir(cd.repo.LocalPath):
				row.State = "missing"
			default:
				rs, err := gitops.GetStatus(gctx, cd.repo.LocalPath)
				if err != nil {
					row.State = "error"
					break
				}
				row.State = rs.StatusString()
				row.Branch = rs.CurrentBranch
				if up := gitops.Upstream(gctx, cd.repo.LocalPath); up != "" {
					row.Upstream = up
					if a, b, aerr := gitops.AheadBehind(gctx, cd.repo.LocalPath, up); aerr == nil {
						row.Ahead, row.Behind = a, b
					}
				}
			}
			rows[i] = row
			return nil
		})
	}
	_ = g.Wait()

	// Apply state-based filters on the live result (not the cached status).
	filtered := rows[:0:0]
	for _, r := range rows {
		if c.Dirty && r.State != "dirty" {
			continue
		}
		if c.Errors && r.State != "error" && r.State != "missing" {
			continue
		}
		filtered = append(filtered, r)
	}
	sort.Slice(filtered, func(i, j int) bool { return filtered[i].Key < filtered[j].Key })

	if ctx.JSON {
		return ctx.Emit(filtered)
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "REPO\tSTATE\tBRANCH\tAHEAD/BEHIND\tUPSTREAM\tTAGS")
	counts := map[string]int{}
	for _, r := range filtered {
		counts[r.State]++
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d/%d\t%s\t%s\n",
			r.Key, r.State, dashIfEmpty(r.Branch), r.Ahead, r.Behind, dashIfEmpty(r.Upstream), r.Tags)
	}
	tw.Flush()

	var parts []string
	for _, s := range []string{"dirty", "conflict", "missing", "error", "clean"} {
		if counts[s] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", counts[s], s))
		}
	}
	fmt.Printf("\n%d repos", len(filtered))
	if len(parts) > 0 {
		fmt.Printf(" (%s)", strings.Join(parts, ", "))
	}
	fmt.Println()
	return nil
}
