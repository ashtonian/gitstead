package cmd

import (
	"fmt"
	"os"
	"sort"
	"text/tabwriter"
	"time"

	"github.com/ashtonian/git-mgr/internal/config"
	"github.com/ashtonian/git-mgr/internal/state"
	"github.com/ashtonian/git-mgr/internal/workgroup"
)

// WorkgroupCmd groups the workgroup subcommands.
type WorkgroupCmd struct {
	Create WGCreateCmd `cmd:"" help:"Materialise a configured workgroup (create its members)."`
	Ensure WGEnsureCmd `cmd:"" help:"Reconcile workgroup(s) on disk to match config (idempotent)."`
	Adopt  WGAdoptCmd  `cmd:"" help:"Adopt an existing directory of repos as a workgroup."`
	List   WGListCmd   `cmd:"" name:"ls" help:"List workgroups."`
	Status WGStatusCmd `cmd:"" help:"Show per-member status of a workgroup."`
	Check  WGCheckCmd  `cmd:"" help:"Assert a workgroup is set up and in sync (non-zero exit on drift)."`
	Fetch  WGFetchCmd  `cmd:"" help:"Fetch all members."`
	Commit WGCommitCmd `cmd:"" help:"Commit changes across all members."`
	Pull   WGPullCmd   `cmd:"" help:"Pull all members (--ff-only by default)."`
	Push   WGPushCmd   `cmd:"" help:"Push all members."`
	Exec   WGExecCmd   `cmd:"" help:"Run a command in each member directory."`
	Open   WGOpenCmd   `cmd:"" aliases:"tmux" help:"Open/attach a tmux session with one pane per member."`
	Remove WGRemoveCmd `cmd:"" name:"rm" help:"Tear down a workgroup."`
}

// WGCreateCmd materialises a configured workgroup for the first time.
type WGCreateCmd struct {
	Name string `arg:"" help:"Workgroup name (must be declared in config)."`
}

// WGEnsureCmd reconciles configured workgroups to disk.
type WGEnsureCmd struct {
	Name string `arg:"" optional:"" help:"Workgroup to reconcile; all configured if omitted."`
}

// WGAdoptCmd registers an existing directory of repos as a workgroup.
type WGAdoptCmd struct {
	Name string `arg:"" help:"Name to register the workgroup under."`
	Dir  string `arg:"" help:"Directory whose git subdirectories become members."`
}

// WGListCmd lists workgroups.
type WGListCmd struct{}

// WGRemoveCmd tears down a workgroup.
type WGRemoveCmd struct {
	Name   string `arg:"" help:"Workgroup to remove."`
	Delete bool   `help:"Also delete member directories from disk."`
	Yes    bool   `help:"Skip confirmation."`
}

func (c *WGCreateCmd) Run(ctx *Context) error {
	cfg, err := ctx.LoadConfig()
	if err != nil {
		return err
	}
	st, unlock, err := lockedState(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	if _, exists := st.Workgroups[c.Name]; exists {
		return fmt.Errorf("workgroup %q already exists; use 'workgroup ensure %s' to reconcile", c.Name, c.Name)
	}
	spec, err := workgroup.FromConfig(cfg, st, c.Name)
	if err != nil {
		return err
	}
	statuses, err := workgroup.Ensure(ctx.GoCtx(), spec, wgSSHKeyFor(cfg, st))
	if err != nil {
		return err
	}
	created, failed := printEnsure(statuses, ctx.Quiet)
	persistWorkgroup(st, spec)
	if err := st.Save(); err != nil {
		return err
	}
	if !ctx.Quiet {
		fmt.Printf("Workgroup %q: %d members (%d created) at %s\n", c.Name, len(spec.Members), created, spec.Root)
	}
	if failed > 0 {
		return fmt.Errorf("%d member(s) failed", failed)
	}
	return nil
}

func (c *WGEnsureCmd) Run(ctx *Context) error {
	cfg, err := ctx.LoadConfig()
	if err != nil {
		return err
	}
	st, unlock, err := lockedState(ctx)
	if err != nil {
		return err
	}
	defer unlock()

	var names []string
	if c.Name != "" {
		names = []string{c.Name}
	} else {
		for _, wg := range cfg.Workgroups {
			names = append(names, wg.Name)
		}
		sort.Strings(names)
	}
	if len(names) == 0 {
		fmt.Println("No workgroups configured.")
		return nil
	}

	totalFailed := 0
	for _, name := range names {
		spec, err := workgroup.FromConfig(cfg, st, name)
		if err != nil {
			return err
		}
		if !ctx.Quiet {
			fmt.Printf("%s:\n", name)
		}
		statuses, err := workgroup.Ensure(ctx.GoCtx(), spec, wgSSHKeyFor(cfg, st))
		if err != nil {
			return err
		}
		_, failed := printEnsure(statuses, ctx.Quiet)
		totalFailed += failed
		persistWorkgroup(st, spec)
	}
	if err := st.Save(); err != nil {
		return err
	}
	if totalFailed > 0 {
		return fmt.Errorf("%d member(s) failed", totalFailed)
	}
	return nil
}

func (c *WGAdoptCmd) Run(ctx *Context) error {
	st, unlock, err := lockedState(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	if _, exists := st.Workgroups[c.Name]; exists {
		return fmt.Errorf("workgroup %q already exists", c.Name)
	}
	spec, err := workgroup.ScanAdopt(c.Name, c.Dir)
	if err != nil {
		return err
	}
	persistWorkgroup(st, spec)
	if err := st.Save(); err != nil {
		return err
	}
	fmt.Printf("Adopted %q: %d members under %s\n", c.Name, len(spec.Members), spec.Root)
	for _, m := range spec.Members {
		fmt.Printf("  %s\n", m.Name)
	}
	return nil
}

func (c *WGListCmd) Run(ctx *Context) error {
	st, err := ctx.LoadState()
	if err != nil {
		return err
	}
	cfg, _ := ctx.LoadConfig() // best-effort: list configured-but-not-created too

	names := map[string]bool{}
	for n := range st.Workgroups {
		names[n] = true
	}
	if cfg != nil {
		for _, wg := range cfg.Workgroups {
			names[wg.Name] = true
		}
	}
	if len(names) == 0 {
		fmt.Println("No workgroups. Declare one in config and run 'workgroup create <name>', or 'workgroup adopt'.")
		return nil
	}

	sorted := make([]string, 0, len(names))
	for n := range names {
		sorted = append(sorted, n)
	}
	sort.Strings(sorted)

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "WORKGROUP\tKIND\tMEMBERS\tSTATUS\tROOT")
	for _, n := range sorted {
		kind, members, root, status := "-", 0, "-", "configured"
		if ws, ok := st.Workgroups[n]; ok {
			kind, members, root, status = ws.Kind, len(ws.Members), ws.Root, "created"
		} else if cfg != nil {
			for _, wg := range cfg.Workgroups {
				if wg.Name == n {
					kind = wg.Kind
				}
			}
		}
		fmt.Fprintf(tw, "%s\t%s\t%d\t%s\t%s\n", n, kind, members, status, root)
	}
	return tw.Flush()
}

func (c *WGRemoveCmd) Run(ctx *Context) error {
	st, unlock, err := lockedState(ctx)
	if err != nil {
		return err
	}
	defer unlock()

	var spec *workgroup.Spec
	if ws, ok := st.Workgroups[c.Name]; ok {
		spec = workgroup.FromState(ws)
	} else {
		cfg, cerr := ctx.LoadConfig()
		if cerr != nil {
			return cerr
		}
		s, serr := workgroup.FromConfig(cfg, st, c.Name)
		if serr != nil {
			return fmt.Errorf("workgroup %q not found in state or config", c.Name)
		}
		spec = s
	}

	if !c.Yes {
		extra := ""
		if c.Delete {
			extra = " and delete files"
		}
		fmt.Printf("Remove workgroup %q (%d members)%s? (y/n): ", c.Name, len(spec.Members), extra)
		var answer string
		fmt.Scanln(&answer)
		if answer != "y" && answer != "Y" {
			fmt.Println("Cancelled.")
			return nil
		}
	}

	for _, a := range workgroup.Remove(ctx.GoCtx(), spec, c.Delete) {
		fmt.Printf("  %s\n", a)
	}
	delete(st.Workgroups, c.Name)
	return st.Save()
}

// --- helpers ---

// printEnsure reports per-member ensure results and returns counts.
func printEnsure(statuses []workgroup.MemberStatus, quiet bool) (created, failed int) {
	for _, s := range statuses {
		switch {
		case s.Err != nil:
			failed++
			fmt.Printf("  %s: ERROR %v\n", s.Name, s.Err)
		case s.Created:
			created++
			if !quiet {
				fmt.Printf("  %s: created %s\n", s.Name, s.Path)
			}
		default:
			if !quiet {
				fmt.Printf("  %s: ok (exists)\n", s.Name)
			}
		}
	}
	return created, failed
}

// lockedState loads the state file, takes the same exclusive flock that sync
// uses, and re-reads the file under the lock so a mutation cannot clobber a
// concurrent writer's changes. The returned func releases the lock.
func lockedState(ctx *Context) (*state.State, func(), error) {
	st, err := ctx.LoadState()
	if err != nil {
		return nil, nil, err
	}
	if err := st.AcquireLock(); err != nil {
		return nil, nil, fmt.Errorf("another git-mgr instance is running; try again")
	}
	if err := st.Reload(); err != nil {
		st.ReleaseLock()
		return nil, nil, err
	}
	return st, st.ReleaseLock, nil
}

// persistWorkgroup writes the spec into state, preserving the original CreatedAt.
func persistWorkgroup(st *state.State, spec *workgroup.Spec) {
	ws := spec.ToState(time.Now())
	if prev, ok := st.Workgroups[spec.Name]; ok {
		ws.CreatedAt = prev.CreatedAt
	}
	st.Workgroups[spec.Name] = ws
}

// wgSSHKeyFor returns an SSH-key resolver for clone members that originate from
// a tracked repo (worktree members are local and need no key).
func wgSSHKeyFor(cfg *config.Config, st *state.State) func(workgroup.Member) string {
	return func(m workgroup.Member) string {
		if m.RepoKey == "" {
			return ""
		}
		discoveredBy := ""
		if r, ok := st.Repos[m.RepoKey]; ok {
			discoveredBy = r.DiscoveredBy
		}
		return cfg.ResolveSSHKey(m.RepoKey, discoveredBy)
	}
}
