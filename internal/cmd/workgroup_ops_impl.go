package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"text/tabwriter"

	"github.com/ashtonian/git-mgr/internal/gitops"
	"github.com/ashtonian/git-mgr/internal/state"
	"github.com/ashtonian/git-mgr/internal/workgroup"
)

// WGStatusCmd shows the per-member status of a workgroup.
type WGStatusCmd struct {
	Name string `arg:"" help:"Workgroup name."`
}

// WGCheckCmd asserts a workgroup is set up and in sync.
type WGCheckCmd struct {
	Name   string `arg:"" help:"Workgroup name."`
	Member string `help:"Only check this member."`
}

// WGFetchCmd fetches every member.
type WGFetchCmd struct {
	Name string `arg:"" help:"Workgroup name."`
}

// WGCommitCmd commits changes across members.
type WGCommitCmd struct {
	Name    string `arg:"" help:"Workgroup name."`
	Message string `short:"m" required:"" help:"Commit message."`
	AddAll  bool   `name:"add-all" help:"Also stage untracked files (git add -A)."`
	Member  string `help:"Only commit this member."`
}

// WGPullCmd pulls every member.
type WGPullCmd struct {
	Name   string `arg:"" help:"Workgroup name."`
	Rebase bool   `help:"Use --rebase instead of the default --ff-only."`
}

// WGPushCmd pushes every member.
type WGPushCmd struct {
	Name        string `arg:"" help:"Workgroup name."`
	SetUpstream bool   `name:"set-upstream" help:"Publish the branch as origin/<branch>."`
}

// WGExecCmd runs an arbitrary command in each member directory.
type WGExecCmd struct {
	Name    string   `arg:"" help:"Workgroup name."`
	Command []string `arg:"" help:"Command to run in each member directory (after --)."`
}

func (c *WGStatusCmd) Run(ctx *Context) error {
	spec, _, err := operationalSpec(ctx, c.Name)
	if err != nil {
		return err
	}
	h := workgroup.ComputeHealth(ctx.GoCtx(), spec)

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "MEMBER\tBRANCH\tSTATE\tUPSTREAM\tAHEAD/BEHIND\tNOTES")
	for _, m := range h.Members {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d/%d\t%s\n",
			m.Name, dashIfEmpty(m.Branch), memberState(m), dashIfEmpty(m.Upstream),
			m.Ahead, m.Behind, strings.Join(m.Issues, "; "))
	}
	tw.Flush()

	if h.OK() {
		fmt.Printf("\n%s: %d members, all in sync\n", c.Name, len(h.Members))
	} else {
		fmt.Printf("\n%s: %d/%d members need attention\n", c.Name, countFlagged(h), len(h.Members))
	}
	return nil
}

func (c *WGCheckCmd) Run(ctx *Context) error {
	spec, _, err := operationalSpec(ctx, c.Name)
	if err != nil {
		return err
	}
	if c.Member != "" {
		spec = filterMember(spec, c.Member)
		if len(spec.Members) == 0 {
			return fmt.Errorf("no member %q in workgroup %q", c.Member, c.Name)
		}
	}
	h := workgroup.ComputeHealth(ctx.GoCtx(), spec)
	for _, m := range h.Members {
		if len(m.Issues) == 0 {
			if !ctx.Quiet {
				fmt.Printf("  ok    %s (%s)\n", m.Name, dashIfEmpty(m.Branch))
			}
			continue
		}
		fmt.Printf("  FLAG  %s: %s\n", m.Name, strings.Join(m.Issues, "; "))
	}
	if !h.OK() {
		return fmt.Errorf("%s: %d member(s) need attention", c.Name, countFlagged(h))
	}
	if !ctx.Quiet {
		fmt.Printf("%s: all %d members in sync\n", c.Name, len(h.Members))
	}
	return nil
}

func (c *WGFetchCmd) Run(ctx *Context) error {
	cfg, err := ctx.LoadConfig()
	if err != nil {
		return err
	}
	spec, st, err := operationalSpec(ctx, c.Name)
	if err != nil {
		return err
	}
	// Worktree members share the source repo's object store and remotes, so one
	// fetch updates them all -- and avoids N concurrent fetches contending on
	// the same ref locks.
	if spec.Kind == "worktree" {
		src := spec.Source
		if src == "" && len(spec.Members) > 0 {
			src = spec.Members[0].Path
		}
		if err := gitops.Fetch(ctx.GoCtx(), src, ""); err != nil {
			return fmt.Errorf("%s: fetch failed: %w", c.Name, err)
		}
		if !ctx.Quiet {
			fmt.Printf("%s: fetched shared source\n", c.Name)
		}
		return nil
	}
	keyFor := wgSSHKeyFor(cfg, st)
	results := workgroup.ForEach(ctx.GoCtx(), spec, cfg.Concurrency,
		func(ctx context.Context, m workgroup.Member) (string, bool, error) {
			return "", false, gitops.Fetch(ctx, m.Path, keyFor(m))
		})
	return reportOps(c.Name, "fetched", results, ctx.Quiet)
}

func (c *WGCommitCmd) Run(ctx *Context) error {
	cfg, err := ctx.LoadConfig()
	if err != nil {
		return err
	}
	spec, _, err := operationalSpec(ctx, c.Name)
	if err != nil {
		return err
	}
	if c.Member != "" {
		spec = filterMember(spec, c.Member)
		if len(spec.Members) == 0 {
			return fmt.Errorf("no member %q in workgroup %q", c.Member, c.Name)
		}
	}
	results := workgroup.ForEach(ctx.GoCtx(), spec, cfg.Concurrency,
		func(ctx context.Context, m workgroup.Member) (string, bool, error) {
			committed, err := gitops.Commit(ctx, m.Path, c.Message, c.AddAll)
			if err != nil {
				return "", false, err
			}
			if !committed {
				return "nothing to commit", true, nil
			}
			return "committed", false, nil
		})
	return reportOps(c.Name, "committed", results, ctx.Quiet)
}

func (c *WGPullCmd) Run(ctx *Context) error {
	cfg, err := ctx.LoadConfig()
	if err != nil {
		return err
	}
	spec, st, err := operationalSpec(ctx, c.Name)
	if err != nil {
		return err
	}
	keyFor := wgSSHKeyFor(cfg, st)
	results := workgroup.ForEach(ctx.GoCtx(), spec, cfg.Concurrency,
		func(ctx context.Context, m workgroup.Member) (string, bool, error) {
			out, err := gitops.Pull(ctx, m.Path, keyFor(m), !c.Rebase, c.Rebase)
			return lastLine(out), false, err
		})
	return reportOps(c.Name, "pulled", results, ctx.Quiet)
}

func (c *WGPushCmd) Run(ctx *Context) error {
	cfg, err := ctx.LoadConfig()
	if err != nil {
		return err
	}
	spec, st, err := operationalSpec(ctx, c.Name)
	if err != nil {
		return err
	}
	keyFor := wgSSHKeyFor(cfg, st)
	results := workgroup.ForEach(ctx.GoCtx(), spec, cfg.Concurrency,
		func(ctx context.Context, m workgroup.Member) (string, bool, error) {
			out, err := gitops.Push(ctx, m.Path, keyFor(m), c.SetUpstream)
			return lastLine(out), false, err
		})
	return reportOps(c.Name, "pushed", results, ctx.Quiet)
}

func (c *WGExecCmd) Run(ctx *Context) error {
	if len(c.Command) == 0 {
		return fmt.Errorf("no command given (usage: workgroup exec <name> -- <command>)")
	}
	cfg, _ := ctx.LoadConfig()
	concurrency := 8
	if cfg != nil {
		concurrency = cfg.Concurrency
	}
	spec, _, err := operationalSpec(ctx, c.Name)
	if err != nil {
		return err
	}
	results := workgroup.ForEach(ctx.GoCtx(), spec, concurrency,
		func(ctx context.Context, m workgroup.Member) (string, bool, error) {
			cmd := exec.CommandContext(ctx, c.Command[0], c.Command[1:]...)
			cmd.Dir = m.Path
			out, err := cmd.CombinedOutput()
			return strings.TrimRight(string(out), "\n"), false, err
		})

	failed := 0
	for _, r := range results {
		fmt.Printf("=== %s ===\n", r.Name)
		if r.Output != "" {
			fmt.Println(r.Output)
		}
		if r.Err != nil {
			failed++
			fmt.Printf("(error: %v)\n", r.Err)
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d member(s) failed", failed)
	}
	return nil
}

// --- shared helpers ---

// operationalSpec resolves a created workgroup from state (the source of truth
// for what actually exists on disk).
func operationalSpec(ctx *Context, name string) (*workgroup.Spec, *state.State, error) {
	st, err := ctx.LoadState()
	if err != nil {
		return nil, nil, err
	}
	ws, ok := st.Workgroups[name]
	if !ok {
		return nil, nil, fmt.Errorf("workgroup %q is not created; run 'workgroup create %s' first", name, name)
	}
	return workgroup.FromState(ws), st, nil
}

func filterMember(spec *workgroup.Spec, name string) *workgroup.Spec {
	out := *spec
	out.Members = nil
	for _, m := range spec.Members {
		if m.Name == name {
			out.Members = append(out.Members, m)
		}
	}
	return &out
}

func reportOps(name, verb string, results []workgroup.OpResult, quiet bool) error {
	ok, skipped, failed := 0, 0, 0
	for _, r := range results {
		switch {
		case r.Err != nil:
			failed++
			fmt.Printf("  %s: ERROR %v\n", r.Name, r.Err)
		case r.Skipped:
			skipped++
			if !quiet {
				fmt.Printf("  %s: %s\n", r.Name, orDefault(r.Output, "skipped"))
			}
		default:
			ok++
			if !quiet {
				fmt.Printf("  %s: %s\n", r.Name, orDefault(r.Output, verb))
			}
		}
	}
	if !quiet {
		fmt.Printf("%s: %d %s, %d skipped, %d failed\n", name, ok, verb, skipped, failed)
	}
	if failed > 0 {
		return fmt.Errorf("%d member(s) failed", failed)
	}
	return nil
}

func memberState(m workgroup.MemberHealth) string {
	switch {
	case !m.Present:
		return "missing"
	case m.Stuck != "":
		return m.Stuck
	case m.Dirty:
		return "dirty"
	case m.Untracked:
		return "untracked"
	default:
		return "clean"
	}
}

func countFlagged(h *workgroup.Health) int {
	n := 0
	for _, m := range h.Members {
		if len(m.Issues) > 0 {
			n++
		}
	}
	return n
}

func dashIfEmpty(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	return lines[len(lines)-1]
}
