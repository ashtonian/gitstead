package cmd

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/ashtonian/git-mgr/internal/config"
	"github.com/ashtonian/git-mgr/internal/gitops"
	"github.com/ashtonian/git-mgr/internal/reconcile"
	"github.com/ashtonian/git-mgr/internal/state"
	"github.com/ashtonian/git-mgr/internal/workgroup"
)

// WorkgroupCmd groups the workgroup subcommands.
type WorkgroupCmd struct {
	Create   WGCreateCmd   `cmd:"" help:"Create a workgroup from flags (one-off) or a config declaration; materialise its members."`
	Ensure   WGEnsureCmd   `cmd:"" help:"Reconcile workgroup(s) on disk to match config (idempotent)."`
	Adopt    WGAdoptCmd    `cmd:"" help:"Adopt an existing directory of repos as a workgroup."`
	List     WGListCmd     `cmd:"" name:"ls" help:"List workgroups."`
	Status   WGStatusCmd   `cmd:"" help:"Show per-member status of a workgroup."`
	Overview WGOverviewCmd `cmd:"" help:"Member health across all workgroups (fleet supervisor view)."`
	Check    WGCheckCmd    `cmd:"" help:"Assert a workgroup is set up and in sync (non-zero exit on drift)."`
	Fetch    WGFetchCmd    `cmd:"" help:"Fetch all members."`
	Commit   WGCommitCmd   `cmd:"" help:"Commit changes across all members."`
	Pull     WGPullCmd     `cmd:"" help:"Pull all members (--ff-only by default)."`
	Push     WGPushCmd     `cmd:"" help:"Push all members."`
	Exec     WGExecCmd     `cmd:"" help:"Run a command in each member directory."`
	Open     WGOpenCmd     `cmd:"" aliases:"tmux" help:"Open/attach a tmux session with one pane per member."`
	Backup   WGBackupCmd   `cmd:"" help:"Back up every member's uncommitted work (agent WIP safety net)."`
	Recover  WGRecoverCmd  `cmd:"" help:"Recover a member's backed-up WIP (print/--into/--apply)."`
	Prune    WGPruneCmd    `cmd:"" help:"Reap members with no unique work (clean + merged); never touches dirty WIP."`
	Remove   WGRemoveCmd   `cmd:"" name:"rm" help:"Tear down a workgroup."`
}

// WGCreateCmd materialises a workgroup for the first time. With no flags it
// looks the name up in config (the original behavior); with --repo/--from-tag/
// --source/--count/--member it synthesizes the workgroup from the CLI as a
// one-off, which --save can persist to config. Clone is the default kind.
type WGCreateCmd struct {
	Name    string   `arg:"" help:"Workgroup name." predictor:"workgroup"`
	Kind    string   `help:"Kind: clone (independent repos, the default) or worktree (shared source)." enum:"clone,worktree" default:"clone"`
	Repo    []string `help:"Clone member: a repo URL or tracked key; repeatable. With --count, N clones of a single repo."`
	FromTag string   `help:"Clone members from tracked repos carrying this tag." name:"from-tag"`
	FromSet string   `help:"Clone members from tracked repos in this named set." name:"from-set"`
	Source  string   `help:"Worktree source repo (tracked key or local path); implies --kind worktree."`
	Count   int      `help:"Number of members to create, named agent-1..N."`
	Member  []string `help:"Explicit member; repeatable. worktree: name[:branch]. clone: a repo URL/key. Overrides --count."`
	BaseRef string   `help:"Worktree fork point (default: source HEAD)." name:"base-ref"`
	Sync    bool     `help:"Track the underlying repo(s) so 'git-mgr sync' keeps them fetched."`
	Save    bool     `help:"Append the synthesized workgroup to the config file."`
	Force   bool     `help:"With --save, overwrite an existing config workgroup of the same name."`
	Open    bool     `help:"After creating, open a tmux session with one pane per member."`
	Cmd     string   `help:"With --open, run this command in each pane (e.g. 'claude')."`
	Agent   string   `help:"With --open, run the named agent command from config 'agents:'."`
	NoCheck bool     `name:"no-check" help:"With --open, skip the per-pane 'wg check'."`
}

// synthesized reports whether any workgroup-defining flag was given (vs. the
// legacy config-lookup path). --kind alone (it has a default) does not count.
func (c *WGCreateCmd) synthesized() bool {
	return c.Source != "" || c.Count > 0 || len(c.Member) > 0 || c.FromTag != "" || len(c.Repo) > 0 || c.FromSet != ""
}

// toWorkgroup builds a config.Workgroup from the CLI flags. Clone is the default
// kind; --source selects worktree (it is worktree-specific). It mirrors the
// rules config.Validate enforces, so a synthesized group is indistinguishable
// from a declared one.
func (c *WGCreateCmd) toWorkgroup() (config.Workgroup, error) {
	wg := config.Workgroup{Name: c.Name, BaseRef: c.BaseRef, FromTag: c.FromTag, Sync: c.Sync}

	kind := c.Kind
	if c.Source != "" {
		kind = "worktree"
	}

	switch kind {
	case "worktree":
		if c.Source == "" {
			return config.Workgroup{}, fmt.Errorf("worktree workgroup needs --source")
		}
		wg.Kind = "worktree"
		wg.Source = c.Source
		members, err := worktreeMembers(c.Name, c.Count, c.Member)
		if err != nil {
			return config.Workgroup{}, err
		}
		wg.Members = members
	case "clone":
		wg.Kind = "clone"
		members, err := cloneMembers(c.Repo, c.Member, c.Count, c.FromTag)
		if err != nil {
			return config.Workgroup{}, err
		}
		wg.Members = members
	default:
		return config.Workgroup{}, fmt.Errorf("unknown --kind %q (want clone or worktree)", kind)
	}
	return wg, nil
}

// cloneMembers builds clone members from --repo/--member (repo URLs or tracked
// keys) and --from-tag. With a single repo and --count N it yields an N-clone
// agent fleet (agent-1..N) of that one repo; otherwise one member per repo.
func cloneMembers(repos, members []string, count int, fromTag string) ([]config.WorkgroupMember, error) {
	all := append(append([]string{}, repos...), members...)
	if fromTag != "" {
		if len(all) > 0 {
			return nil, fmt.Errorf("--from-tag cannot be combined with --repo/--member")
		}
		return nil, nil // members are selected from the tag at resolve time
	}
	if len(all) == 0 {
		return nil, fmt.Errorf("clone workgroup needs --repo/--member or --from-tag")
	}
	if count > 1 && len(all) == 1 {
		var ms []config.WorkgroupMember
		for i := 1; i <= count; i++ {
			ms = append(ms, config.WorkgroupMember{Name: fmt.Sprintf("agent-%d", i), Repo: all[0]})
		}
		return ms, nil
	}
	var ms []config.WorkgroupMember
	for _, r := range all {
		ms = append(ms, config.WorkgroupMember{Repo: r})
	}
	return ms, nil
}

// worktreeMembers expands --count / --member into worktree members. Explicit
// --member entries (name[:branch]) win over --count; otherwise --count N yields
// members agent-1..agent-N. Default branches are namespaced by the workgroup
// name (<wgName>/agent-N) so two workgroups off the same source repo don't
// collide on branch names; an explicit branch (name:branch) is used as-is.
func worktreeMembers(wgName string, count int, explicit []string) ([]config.WorkgroupMember, error) {
	var members []config.WorkgroupMember
	if len(explicit) > 0 {
		for _, m := range explicit {
			name, branch, hasBranch := strings.Cut(m, ":")
			if !hasBranch {
				branch = wgName + "/" + name
			}
			members = append(members, config.WorkgroupMember{Name: name, Branch: branch})
		}
		return members, nil
	}
	if count < 1 {
		return nil, fmt.Errorf("worktree workgroup needs --count >= 1 or at least one --member")
	}
	for i := 1; i <= count; i++ {
		name := fmt.Sprintf("agent-%d", i)
		members = append(members, config.WorkgroupMember{Name: name, Branch: wgName + "/" + name})
	}
	return members, nil
}

// WGEnsureCmd reconciles configured workgroups to disk.
type WGEnsureCmd struct {
	Name string `arg:"" optional:"" help:"Workgroup to reconcile; all configured if omitted." predictor:"workgroup"`
}

// WGAdoptCmd registers an existing directory of repos as a workgroup.
type WGAdoptCmd struct {
	Name string `arg:"" help:"Name to register the workgroup under."`
	Dir  string `arg:"" help:"Directory whose git subdirectories become members."`
}

// WGListCmd lists workgroups.
type WGListCmd struct{}

// WGBackupCmd backs up every member's working tree so agent WIP is preserved.
type WGBackupCmd struct {
	Name       string `arg:"" help:"Workgroup to back up." predictor:"workgroup"`
	Member     string `help:"Only back up this member." default:""`
	PushBranch bool   `help:"Also push WIP to a recoverable remote branch (git-mgr/wip/<branch>)." name:"push-branch"`
}

func (c *WGBackupCmd) Run(ctx *Context) error {
	cfg, err := ctx.LoadConfig()
	if err != nil {
		return err
	}
	st, err := ctx.LoadState()
	if err != nil {
		return err
	}
	ws, ok := st.Workgroups[c.Name]
	if !ok {
		return fmt.Errorf("workgroup %q not found in state; run 'workgroup ensure %s' first", c.Name, c.Name)
	}

	bcfg := backupConfigFor(cfg, cfg.ResolveForRepo(""), "", "", c.PushBranch, "")
	saved, failed := 0, 0
	for _, m := range ws.Members {
		if c.Member != "" && m.Name != c.Member {
			continue
		}
		if !gitops.HasGitDir(m.Path) {
			continue
		}
		br, berr := gitops.Backup(ctx.GoCtx(), m.Path, bcfg)
		if berr != nil {
			failed++
			fmt.Fprintf(os.Stderr, "  %s: %v\n", m.Name, berr)
		}
		if br.Outcome == gitops.BackupSaved {
			saved++
		}
		if br.Dirty() && (!ctx.Quiet || len(br.Skipped) > 0) {
			fmt.Printf("  %s: %s\n", m.Name, describeBackup(br))
		}
	}
	if !ctx.Quiet {
		fmt.Printf("Saved %d new backup(s) for %q\n", saved, c.Name)
	}
	if failed > 0 {
		return fmt.Errorf("%d member(s) of %q failed to back up", failed, c.Name)
	}
	return nil
}

// WGRemoveCmd tears down a workgroup.
type WGRemoveCmd struct {
	Name       string `arg:"" help:"Workgroup to remove." predictor:"workgroup"`
	Delete     bool   `help:"Also delete member directories from disk."`
	Yes        bool   `help:"Skip confirmation."`
	Force      bool   `help:"Discard uncommitted member work without backing it up first."`
	PushBranch bool   `help:"Also push members' WIP to a recoverable remote branch during the safety backup." name:"push-branch"`
}

func (c *WGCreateCmd) Run(ctx *Context) error {
	// materialize holds the state lock; let it return (releasing the lock) before
	// opening, so the tmux attach never runs while the lock is held.
	if err := c.materialize(ctx); err != nil {
		return err
	}
	if c.Open {
		open := &WGOpenCmd{Name: c.Name, Cmd: c.Cmd, Agent: c.Agent, NoCheck: c.NoCheck}
		return open.Run(ctx)
	}
	return nil
}

func (c *WGCreateCmd) materialize(ctx *Context) error {
	cfg, err := ctx.LoadConfig()
	if err != nil {
		return err
	}
	st, unlock, err := lockedState(ctx)
	if err != nil {
		return err
	}
	defer unlock()

	// --from-set expands to clone members from a named set's tracked repos.
	if c.FromSet != "" {
		keys, serr := selectRepos(cfg, st, selection{set: c.FromSet})
		if serr != nil {
			return serr
		}
		if len(keys) == 0 {
			return fmt.Errorf("set %q matched no tracked repos", c.FromSet)
		}
		c.Repo = append(c.Repo, keys...)
	}

	// A workgroup already materialized in state can't be re-created -- but with
	// --save the intent may be to (re)persist its declaration to config, which is
	// still useful, so allow that path through (Ensure is idempotent).
	if _, exists := st.Workgroups[c.Name]; exists && !c.Save {
		return fmt.Errorf("workgroup %q already exists; use 'workgroup ensure %s' to reconcile", c.Name, c.Name)
	}

	// A workgroup declared in config is created like any other change config
	// asks for; one synthesized from flags is materialized here.
	if !c.synthesized() {
		if c.Save {
			return fmt.Errorf("--save requires synthesizing flags (--repo/--from-tag/--source/--count/--member)")
		}
		if _, err := workgroup.FromConfig(cfg, st, c.Name); err != nil {
			return err
		}
		return ensureWorkgroups(ctx, cfg, st, c.Name, true)
	}
	wg, err := c.toWorkgroup()
	if err != nil {
		return err
	}
	spec, err := workgroup.SpecFromWorkgroup(cfg, st, &wg)
	if err != nil {
		return err
	}

	statuses, err := workgroup.Ensure(ctx.GoCtx(), spec, memberSSHKey(cfg, st))
	if err != nil {
		return err
	}
	created, failed := printEnsure(statuses, ctx.Quiet)
	// --save turns the flags into a config declaration, which then owns it.
	persistWorkgroup(ctx, st, spec, c.Save)

	// --sync tracks the underlying repo(s) so the normal sync loop keeps them
	// fetched.
	if c.Sync {
		n, skipped := workgroup.TrackRepos(ctx.GoCtx(), st, spec)
		if n > 0 && !ctx.Quiet {
			fmt.Printf("Tracking %d repo(s) for sync.\n", n)
		}
		for _, p := range skipped {
			fmt.Fprintf(os.Stderr, "warning: not tracking %s for sync (no parseable SSH remote)\n", p)
		}
	}

	if err := st.Save(); err != nil {
		return err
	}
	logEvent(ctx, "wg_create", c.Name, spec.Kind)

	// --save persists the synthesized declaration to config.
	if c.Save {
		if err := config.SaveWorkgroup(ctx.Config, wg, c.Force); err != nil {
			return err
		}
		cfg.Workgroups = append(slices.DeleteFunc(cfg.Workgroups, func(w config.Workgroup) bool { return w.Name == wg.Name }), wg)
		if !ctx.Quiet {
			fmt.Printf("Saved workgroup %q to config.\n", c.Name)
		}
	}

	if !ctx.Quiet {
		fmt.Printf("Workgroup %q: %d members (%d created) at %s\n", c.Name, len(spec.Members), created, spec.Root)
	}
	// Give the members their accounts' managed git config.
	ensureErr := ensureWorkgroups(ctx, cfg, st, c.Name, false)
	if failed > 0 {
		return errors.Join(fmt.Errorf("%d member(s) failed", failed), ensureErr)
	}
	return ensureErr
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
	if c.Name != "" && st.Workgroups[c.Name] == nil && !slices.ContainsFunc(cfg.Workgroups, func(w config.Workgroup) bool { return w.Name == c.Name }) {
		return fmt.Errorf("workgroup %q is neither declared in config nor recorded", c.Name)
	}
	return ensureWorkgroups(ctx, cfg, st, c.Name, true)
}

// ensureWorkgroups creates what is missing of the named workgroup, or of all
// of them: workgroups declared in config, members added to a declaration, and
// members gone from disk. Moves and removals wait for `git-mgr apply`.
func ensureWorkgroups(ctx *Context, cfg *config.Config, st *state.State, name string, sayIfNothing bool) error {
	scope := reconcile.Scope{Workgroups: true}
	if name != "" {
		scope.Only = func(n string) bool { return n == name }
	}
	plan := reconcile.Compute(ctx.GoCtx(), cfg, st, scope)
	if plan.Empty() {
		if sayIfNothing && !ctx.Quiet {
			fmt.Println("Workgroups match config.")
		}
		return nil
	}
	return errors.Join(applyAuto(ctx, cfg, st, plan, reconcile.Safe), blockedErr(plan))
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
	persistWorkgroup(ctx, st, spec, false)
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
		action := "forget"
		if c.Delete {
			action = "delete"
			if c.Force {
				action = "delete, discarding unique work in,"
			}
		}
		fmt.Printf("Remove workgroup %q: %s %d members? (y/n): ", c.Name, action, len(spec.Members))
		var answer string
		fmt.Scanln(&answer)
		if answer != "y" && answer != "Y" {
			fmt.Println("Cancelled.")
			return nil
		}
	}

	if !c.Delete {
		delete(st.Workgroups, c.Name)
		logEvent(ctx, "wg_remove", c.Name, "forgot; files kept")
		return st.Save()
	}

	cfg, err := ctx.LoadConfig()
	if err != nil {
		return err
	}
	var removed []workgroup.Member
	for _, m := range spec.Members {
		if c.PushBranch && !c.Force {
			repoCfg := cfg.ResolveForRepo(m.RepoKey)
			bcfg := backupConfigFor(cfg, repoCfg, m.RepoKey, "", c.PushBranch, "")
			if _, berr := gitops.Backup(ctx.GoCtx(), m.Path, bcfg); berr != nil {
				fmt.Fprintf(os.Stderr, "  %s: kept, pushing backup branch failed: %v\n", m.Name, berr)
				continue
			}
		}
		archived, rerr := workgroup.RemoveMember(ctx.GoCtx(), spec, m, cfg.Backup.ArchiveDir, c.Force)
		if rerr != nil {
			fmt.Fprintf(os.Stderr, "  %s: kept: %v\n", m.Name, rerr)
			continue
		}
		removed = append(removed, m)
		if !ctx.Quiet {
			if archived != "" {
				fmt.Printf("  %s: removed, unique work archived to %s\n", m.Name, archived)
			} else {
				fmt.Printf("  %s: removed\n", m.Name)
			}
		}
	}
	if kept := len(spec.Members) - len(removed); kept > 0 {
		if ws, ok := st.Workgroups[c.Name]; ok {
			ws.Members = dropMembers(ws.Members, removed)
		}
		if serr := st.Save(); serr != nil {
			return serr
		}
		return fmt.Errorf("workgroup %q: %d of %d members could not be removed and are still tracked", c.Name, kept, len(spec.Members))
	}
	workgroup.RemoveEmptyRoot(spec)
	delete(st.Workgroups, c.Name)
	logEvent(ctx, "wg_remove", c.Name, "")
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

// persistWorkgroup writes the spec into state, preserving the original
// CreatedAt. declared says whether config now declares it.
func persistWorkgroup(ctx *Context, st *state.State, spec *workgroup.Spec, declared bool) {
	ws := spec.ToState(time.Now())
	ws.Declared = declared
	for i := range ws.Members {
		ws.Members[i].CopyID = gitops.ReadCopyID(ctx.GoCtx(), ws.Members[i].Path)
	}
	if prev, ok := st.Workgroups[spec.Name]; ok {
		ws.CreatedAt = prev.CreatedAt
	}
	st.Workgroups[spec.Name] = ws
}

// memberSSHKey returns the SSH-key resolver for cloning workgroup members.
func memberSSHKey(cfg *config.Config, st *state.State) func(workgroup.Member) string {
	return func(m workgroup.Member) string { return reconcile.MemberSSHKey(cfg, st, m) }
}
