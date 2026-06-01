package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/ashtonian/git-mgr/internal/config"
	"github.com/ashtonian/git-mgr/internal/gitops"
	"github.com/ashtonian/git-mgr/internal/state"
	"github.com/ashtonian/git-mgr/internal/workgroup"
)

// WGStatusCmd shows the per-member status of a workgroup.
type WGStatusCmd struct {
	Name string `arg:"" help:"Workgroup name."`
	JSON bool   `help:"Output JSON."`
}

// WGRecoverCmd recovers a member's backed-up WIP, reusing the restore machinery.
type WGRecoverCmd struct {
	Name   string `arg:"" help:"Workgroup name."`
	Member string `help:"Only recover this member." default:""`
	Into   string `help:"Clone + apply into this scratch dir (requires --member)." default:""`
	Apply  bool   `help:"Apply the backup onto the member's worktree (refuses dirty without --force)."`
	Force  bool   `help:"With --apply, apply onto a dirty worktree."`
}

func (c *WGRecoverCmd) Run(ctx *Context) error {
	cfg, err := ctx.LoadConfig()
	if err != nil {
		return err
	}
	spec, _, err := operationalSpec(ctx, c.Name)
	if err != nil {
		return err
	}
	patchDir := cfg.ResolveForRepo("").Backup.PatchDir

	members := spec.Members
	if c.Member != "" {
		members = nil
		for _, m := range spec.Members {
			if m.Name == c.Member {
				members = append(members, m)
			}
		}
		if len(members) == 0 {
			return fmt.Errorf("no member %q in workgroup %q", c.Member, c.Name)
		}
	}
	if c.Into != "" && len(members) != 1 {
		return fmt.Errorf("--into requires a single member (use --member)")
	}

	found := 0
	for _, m := range members {
		label := c.Name + "/" + m.Name
		if !gitops.HasGitDir(m.Path) {
			fmt.Printf("  %s: not present (run 'wg ensure %s' to recreate, then recover)\n", m.Name, c.Name)
			continue
		}
		id := latestBackupID(ctx, m.Path, patchDir)
		if id == "" {
			fmt.Printf("  %s: no local backup found\n", m.Name)
			continue
		}
		a := locateBackup(ctx, m.Path, patchDir, id)
		if !a.any() {
			fmt.Printf("  %s: no backup\n", m.Name)
			continue
		}
		found++
		switch {
		case c.Into != "":
			if err := restoreBackupInto(ctx, m.Path, config.ExpandHome(c.Into), label, a); err != nil {
				return err
			}
		case c.Apply:
			if err := applyBackupToTree(ctx, m.Path, label, c.Force, a); err != nil {
				fmt.Printf("  %s: %v\n", m.Name, err)
			}
		default:
			printRestoreSteps(m.Path, label, a)
		}
	}
	if found == 0 {
		fmt.Println("No local backups found. WIP pushed via --push-branch is on the remote:")
		fmt.Println("  git -C <member> fetch <remote> git-mgr/wip/<branch> && git -C <member> stash apply FETCH_HEAD")
	}
	return nil
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

	if c.JSON {
		views := make([]memberStatusJSON, 0, len(h.Members))
		for _, m := range h.Members {
			views = append(views, memberHealthView(c.Name, m))
		}
		return printIndentedJSON(views)
	}

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

// memberStatusJSON is the stable JSON shape for a member's observed health,
// shared by `wg status --json` and `wg overview --json`.
type memberStatusJSON struct {
	Workgroup string   `json:"workgroup,omitempty"`
	Member    string   `json:"member"`
	Path      string   `json:"path"`
	Branch    string   `json:"branch,omitempty"`
	State     string   `json:"state"` // missing|clean|dirty|untracked|rebase|merge|cherry-pick
	Present   bool     `json:"present"`
	Dirty     bool     `json:"dirty"`
	Untracked bool     `json:"untracked"`
	Locked    bool     `json:"locked"`
	Stuck     string   `json:"stuck,omitempty"`
	Upstream  string   `json:"upstream,omitempty"`
	Ahead     int      `json:"ahead"`
	Behind    int      `json:"behind"`
	Issues    []string `json:"issues,omitempty"`
}

func memberHealthView(wgName string, m workgroup.MemberHealth) memberStatusJSON {
	return memberStatusJSON{
		Workgroup: wgName,
		Member:    m.Name,
		Path:      m.Path,
		Branch:    m.Branch,
		State:     memberState(m),
		Present:   m.Present,
		Dirty:     m.Dirty,
		Untracked: m.Untracked,
		Locked:    m.Locked,
		Stuck:     m.Stuck,
		Upstream:  m.Upstream,
		Ahead:     m.Ahead,
		Behind:    m.Behind,
		Issues:    m.Issues,
	}
}

func printIndentedJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// WGOverviewCmd shows member health across ALL created workgroups -- the
// supervisor view for a fleet of agents (who is dirty / ahead / stuck / done).
type WGOverviewCmd struct {
	JSON bool `help:"Output JSON."`
}

func (c *WGOverviewCmd) Run(ctx *Context) error {
	st, err := ctx.LoadState()
	if err != nil {
		return err
	}
	if len(st.Workgroups) == 0 {
		fmt.Println("No workgroups created. See 'git-mgr wg create --help'.")
		return nil
	}

	names := make([]string, 0, len(st.Workgroups))
	for n := range st.Workgroups {
		names = append(names, n)
	}
	sort.Strings(names)

	type row struct {
		wg string
		m  workgroup.MemberHealth
	}
	var rows []row
	for _, n := range names {
		spec := workgroup.FromState(st.Workgroups[n])
		for _, m := range workgroup.ComputeHealth(ctx.GoCtx(), spec).Members {
			rows = append(rows, row{n, m})
		}
	}

	if c.JSON {
		views := make([]memberStatusJSON, 0, len(rows))
		for _, r := range rows {
			views = append(views, memberHealthView(r.wg, r.m))
		}
		return printIndentedJSON(views)
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "WORKGROUP\tMEMBER\tBRANCH\tSTATE\tAHEAD/BEHIND\tNOTES")
	counts := map[string]int{}
	for _, r := range rows {
		s := memberState(r.m)
		counts[s]++
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d/%d\t%s\n",
			r.wg, r.m.Name, dashIfEmpty(r.m.Branch), s, r.m.Ahead, r.m.Behind, strings.Join(r.m.Issues, "; "))
	}
	tw.Flush()

	var parts []string
	for _, s := range []string{"clean", "dirty", "untracked", "rebase", "merge", "cherry-pick", "missing"} {
		if counts[s] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", counts[s], s))
		}
	}
	fmt.Printf("\n%d members across %d workgroups: %s\n", len(rows), len(names), strings.Join(parts, ", "))
	return nil
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
