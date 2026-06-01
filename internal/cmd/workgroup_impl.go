package cmd

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/ashtonian/git-mgr/internal/config"
	"github.com/ashtonian/git-mgr/internal/gitops"
	"github.com/ashtonian/git-mgr/internal/state"
	"github.com/ashtonian/git-mgr/internal/workgroup"
)

// WorkgroupCmd groups the workgroup subcommands.
type WorkgroupCmd struct {
	Create WGCreateCmd `cmd:"" help:"Create a workgroup from flags (one-off) or a config declaration; materialise its members."`
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
	Backup WGBackupCmd `cmd:"" help:"Back up every member's uncommitted work (agent WIP safety net)."`
	Remove WGRemoveCmd `cmd:"" name:"rm" help:"Tear down a workgroup."`
}

// WGCreateCmd materialises a workgroup for the first time. With no flags it
// looks the name up in config (the original behavior); with --repo/--from-tag/
// --source/--count/--member it synthesizes the workgroup from the CLI as a
// one-off, which --save can persist to config. Clone is the default kind.
type WGCreateCmd struct {
	Name    string   `arg:"" help:"Workgroup name."`
	Kind    string   `help:"Kind: clone (independent repos, the default) or worktree (shared source)." enum:"clone,worktree" default:"clone"`
	Repo    []string `help:"Clone member: a repo URL or tracked key; repeatable. With --count, N clones of a single repo."`
	FromTag string   `help:"Clone members from tracked repos carrying this tag." name:"from-tag"`
	Source  string   `help:"Worktree source repo (tracked key or local path); implies --kind worktree."`
	Count   int      `help:"Number of members to create, named agent-1..N."`
	Member  []string `help:"Explicit member; repeatable. worktree: name[:branch]. clone: a repo URL/key. Overrides --count."`
	BaseRef string   `help:"Worktree fork point (default: source HEAD)." name:"base-ref"`
	Sync    bool     `help:"Track the underlying repo(s) so 'git-mgr sync' keeps them fetched."`
	Save    bool     `help:"Append the synthesized workgroup to the config file."`
	Force   bool     `help:"With --save, overwrite an existing config workgroup of the same name."`
	Open    bool     `help:"After creating, open a tmux session with one pane per member."`
	Cmd     string   `help:"With --open, run this command in each pane (e.g. 'claude')."`
	NoCheck bool     `name:"no-check" help:"With --open, skip the per-pane 'wg check'."`
}

// synthesized reports whether any workgroup-defining flag was given (vs. the
// legacy config-lookup path). --kind alone (it has a default) does not count.
func (c *WGCreateCmd) synthesized() bool {
	return c.Source != "" || c.Count > 0 || len(c.Member) > 0 || c.FromTag != "" || len(c.Repo) > 0
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
	Name string `arg:"" optional:"" help:"Workgroup to reconcile; all configured if omitted."`
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
	Name       string `arg:"" help:"Workgroup to back up."`
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
	backed := 0
	for _, m := range ws.Members {
		if c.Member != "" && m.Name != c.Member {
			continue
		}
		if !gitops.HasGitDir(m.Path) {
			continue
		}
		br, berr := gitops.Backup(ctx.GoCtx(), m.Path, bcfg)
		if berr != nil {
			fmt.Printf("  %s: error: %v\n", m.Name, berr)
			continue
		}
		if br.IsDirty {
			backed++
			if !ctx.Quiet {
				where := br.RefName
				if where == "" {
					where = "patch"
				}
				if br.PushedBranch != "" {
					where += " + " + br.PushedBranch
				}
				fmt.Printf("  %s: backed up (%s)\n", m.Name, where)
			}
		}
	}
	if !ctx.Quiet {
		fmt.Printf("Backed up %d dirty member(s) of %q\n", backed, c.Name)
	}
	return nil
}

// WGRemoveCmd tears down a workgroup.
type WGRemoveCmd struct {
	Name       string `arg:"" help:"Workgroup to remove."`
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
		open := &WGOpenCmd{Name: c.Name, Cmd: c.Cmd, NoCheck: c.NoCheck}
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
	// A workgroup already materialized in state can't be re-created -- but with
	// --save the intent may be to (re)persist its declaration to config, which is
	// still useful, so allow that path through (Ensure is idempotent).
	if _, exists := st.Workgroups[c.Name]; exists && !c.Save {
		return fmt.Errorf("workgroup %q already exists; use 'workgroup ensure %s' to reconcile", c.Name, c.Name)
	}

	// A synthesized workgroup is built from flags; otherwise look the name up in
	// config (the original behavior).
	var wg config.Workgroup
	var spec *workgroup.Spec
	if c.synthesized() {
		wg, err = c.toWorkgroup()
		if err != nil {
			return err
		}
		spec, err = workgroup.SpecFromWorkgroup(cfg, st, &wg)
	} else {
		spec, err = workgroup.FromConfig(cfg, st, c.Name)
	}
	if err != nil {
		return err
	}

	statuses, err := workgroup.Ensure(ctx.GoCtx(), spec, wgSSHKeyFor(cfg, st))
	if err != nil {
		return err
	}
	created, failed := printEnsure(statuses, ctx.Quiet)
	persistWorkgroup(st, spec)

	// --sync (or sync:true on a config workgroup) tracks the underlying repo(s)
	// so the normal sync loop keeps them fetched.
	wantSync := c.Sync || (!c.synthesized() && configWorkgroupSync(cfg, c.Name))
	if wantSync {
		n, skipped := trackWorkgroupRepos(st, spec)
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

	// --save persists the synthesized declaration to config.
	if c.Save {
		if !c.synthesized() {
			return fmt.Errorf("--save requires synthesizing flags (--repo/--from-tag/--source/--count/--member)")
		}
		if err := config.SaveWorkgroup(ctx.Config, wg, c.Force); err != nil {
			return err
		}
		if !ctx.Quiet {
			fmt.Printf("Saved workgroup %q to config.\n", c.Name)
		}
	}

	if !ctx.Quiet {
		fmt.Printf("Workgroup %q: %d members (%d created) at %s\n", c.Name, len(spec.Members), created, spec.Root)
	}
	if failed > 0 {
		return fmt.Errorf("%d member(s) failed", failed)
	}
	return nil
}

// configWorkgroupSync reports whether the named config workgroup has sync: true.
func configWorkgroupSync(cfg *config.Config, name string) bool {
	for i := range cfg.Workgroups {
		if cfg.Workgroups[i].Name == name {
			return cfg.Workgroups[i].Sync
		}
	}
	return false
}

// trackWorkgroupRepos registers the workgroup's underlying repo(s) as tracked
// (fetch-only) so `git-mgr sync` keeps them current. For worktree groups that is
// the shared source repo; for clone groups it is each member clone. Already
// tracked repos are left untouched. Returns how many new repos were added and
// the paths it skipped because their origin remote could not be parsed (no
// remote, or a non-SSH URL) -- those can't be synced and the caller warns.
func trackWorkgroupRepos(st *state.State, spec *workgroup.Spec) (added int, skipped []string) {
	track := func(localPath string) {
		if localPath == "" {
			return
		}
		for _, r := range st.Repos {
			if r.LocalPath == localPath {
				return // already tracked
			}
		}
		id, err := gitops.ParseRemoteURL(context.Background(), localPath)
		if err != nil {
			skipped = append(skipped, localPath)
			return
		}
		key := id.Host + "/" + id.FullName
		if _, ok := st.Repos[key]; ok {
			return
		}
		st.Repos[key] = &state.RepoState{
			CloneURL: id.SSHURL, Host: id.Host, Owner: id.Owner, Name: id.Name,
			FullName: id.FullName, Provider: id.Host, LocalPath: localPath,
			Cloned: true, ManuallyAdded: true, Status: "clean",
			Tags: []string{"workgroup:" + spec.Name}, FirstSeen: time.Now(),
		}
		added++
	}
	if spec.Kind == "worktree" {
		track(spec.Source)
	} else {
		for _, m := range spec.Members {
			track(m.Path)
		}
	}
	return added, skipped
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

	// Safety: never silently destroy uncommitted work. Unless --force, find any
	// dirty/untracked/mid-operation member (read-only) so the prompt warns, and
	// back each one up before the worktree is torn down.
	var unsafe []workgroup.MemberHealth
	if !c.Force {
		for _, m := range workgroup.ComputeHealth(ctx.GoCtx(), spec).Members {
			if m.Dirty || m.Untracked || m.Stuck != "" {
				unsafe = append(unsafe, m)
			}
		}
	}

	if !c.Yes {
		extra := ""
		if c.Delete {
			extra = " and delete files"
		}
		warn := ""
		if len(unsafe) > 0 {
			warn = fmt.Sprintf(" [%d member(s) have uncommitted work, will be backed up first]", len(unsafe))
		}
		fmt.Printf("Remove workgroup %q (%d members)%s%s? (y/n): ", c.Name, len(spec.Members), extra, warn)
		var answer string
		fmt.Scanln(&answer)
		if answer != "y" && answer != "Y" {
			fmt.Println("Cancelled.")
			return nil
		}
	}

	// Capture WIP before anything is removed; abort if a backup fails so work is
	// never lost (the user can pass --force to discard deliberately).
	if len(unsafe) > 0 {
		cfg, cerr := ctx.LoadConfig()
		if cerr != nil {
			return cerr
		}
		for _, m := range unsafe {
			repoCfg := cfg.ResolveForRepo(m.RepoKey)
			bcfg := backupConfigFor(cfg, repoCfg, m.RepoKey, "", c.PushBranch, "")
			br, berr := gitops.Backup(ctx.GoCtx(), m.Path, bcfg)
			if berr != nil {
				return fmt.Errorf("refusing to remove %q: backup of member %q failed (use --force to discard): %w", c.Name, m.Name, berr)
			}
			if !ctx.Quiet {
				where := br.RefName
				if where == "" {
					where = "patch"
				}
				if br.PushedBranch != "" {
					where += " + " + br.PushedBranch
				}
				fmt.Printf("  safety backup: %s (%s)\n", m.Name, where)
			}
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
