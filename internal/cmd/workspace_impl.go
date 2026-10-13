package cmd

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/ashtonian/git-mgr/internal/config"
	"github.com/ashtonian/git-mgr/internal/gitops"
	"github.com/ashtonian/git-mgr/internal/reconcile"
	"github.com/ashtonian/git-mgr/internal/repourl"
	"github.com/ashtonian/git-mgr/internal/state"
	"github.com/ashtonian/git-mgr/internal/workspace"
)

// WorkspaceCmd groups the workspace subcommands.
type WorkspaceCmd struct {
	Create   WSCreateCmd   `cmd:"" help:"Create a workspace from flags, or one declared in config, and make its agents' copies."`
	Ensure   WSEnsureCmd   `cmd:"" help:"Make what is missing of one workspace or all of them (idempotent)."`
	Adopt    WSAdoptCmd    `cmd:"" help:"Adopt a directory of existing checkouts as a workspace, one agent per checkout."`
	List     WSListCmd     `cmd:"" name:"ls" help:"List workspaces."`
	Status   WSStatusCmd   `cmd:"" help:"Show the status of a workspace's copies."`
	Overview WSOverviewCmd `cmd:"" help:"Copy health across every workspace."`
	Check    WSCheckCmd    `cmd:"" help:"Check a workspace is set up and up to date (non-zero exit when not)."`
	Fetch    WSFetchCmd    `cmd:"" help:"Fetch each repo once and bring every copy up to date from it."`
	Commit   WSCommitCmd   `cmd:"" help:"Commit changes in every copy."`
	Pull     WSPullCmd     `cmd:"" help:"Pull every copy (--ff-only by default)."`
	Push     WSPushCmd     `cmd:"" help:"Push every copy."`
	Exec     WSExecCmd     `cmd:"" help:"Run a command in each copy (after --)."`
	Open     WSOpenCmd     `cmd:"" help:"Open the workspace's agents with a launcher: tmux, iTerm, Muxy or a plugin."`
	Run      WSRunCmd      `cmd:"" help:"Run an agent in its replica, in this terminal: what each pane of 'ws open' runs."`
	Env      WSEnvCmd      `cmd:"" help:"Print an agent's environment -- its ports, cache and workspace env -- as shell exports."`
	Waiting  WSWaitingCmd  `cmd:"" help:"Tell git-mgr an agent is waiting for you: for agents' own notification hooks."`
	Stop     WSStopCmd     `cmd:"" help:"End agents' runs, as closing their terminals would."`
	Publish  WSPublishCmd  `cmd:"" help:"Push agents' branches, and with --pr open a pull request per repo."`
	Retire   WSRetireCmd   `cmd:"" help:"Retire agents: archive their copies' unique work, remove the copies, keep tombstones."`
	Backup   WSBackupCmd   `cmd:"" help:"Back up every copy's uncommitted work."`
	Recover  WSRecoverCmd  `cmd:"" help:"Recover a copy's backed-up work (print, --into or --apply)."`
	Prune    WSPruneCmd    `cmd:"" help:"Remove copies with no unique work (clean, nothing unpushed); never touches work in progress."`
	Remove   WSRemoveCmd   `cmd:"" name:"rm" help:"Forget a workspace, or with --delete remove its copies."`
}

// WSCreateCmd creates a workspace. With no repos, set or path it creates the
// one config declares; otherwise it creates one from the flags, which --save
// writes to config.
type WSCreateCmd struct {
	Name      string   `arg:"" help:"Workspace name." predictor:"workspace"`
	Repo      []string `help:"A repo: a tracked key, a glob over them, a local path or a URL; repeatable. Untracked paths are adopted, URLs cloned."`
	Set       string   `help:"The repos of a named set."`
	Path      string   `help:"A folder every agent shares, instead of repos."`
	Agents    int      `help:"Number of agents, each with a copy of every repo (default workspace_defaults.agents)."`
	Isolation string   `help:"How copies are made: worktree, clone, reference-clone or shared (default workspace_defaults.isolation)." enum:",worktree,clone,reference-clone,shared" default:""`
	Branch    string   `help:"Branch template for new copies (default workspace_defaults.branch)."`
	BaseRef   string   `help:"Where new branches start (default: the upstream's default branch)." name:"base-ref"`
	Root      string   `help:"Directory holding the workspace (default workspace_defaults.root)."`
	Save      bool     `help:"Write the workspace to the config file, which then declares it."`
	Force     bool     `help:"With --save, replace a workspace of the same name in config."`
	Open      bool     `help:"After creating, open its agents with a launcher (see ws open)."`
	Agent     string   `help:"With --open, the agent profile each agent runs."`
	Cmd       string   `help:"With --open, a command each agent's terminal runs instead of an agent profile."`
	Launcher  string   `help:"With --open, the launcher: tmux, iterm, muxy, auto or a plugin."`
}

func (c *WSCreateCmd) flags() bool {
	return len(c.Repo) > 0 || c.Set != "" || c.Path != ""
}

func (c *WSCreateCmd) workspace() config.Workspace {
	return config.Workspace{
		Name: c.Name, Repos: c.Repo, Set: c.Set, Path: c.Path, Agents: c.Agents,
		Isolation: c.Isolation, Branch: c.Branch, BaseRef: c.BaseRef, Root: c.Root,
	}
}

func (c *WSCreateCmd) Run(ctx *Context) error {
	// Creating takes the state lock change by change; it is released before
	// tmux takes over the terminal.
	if ctx.JSON {
		if c.Open {
			return errNoJSON("ws create --open")
		}
		return ctx.finishChanges(c.create(ctx))
	}
	if err := c.create(ctx); err != nil {
		return err
	}
	if c.Open {
		return (&WSOpenCmd{Name: c.Name, NoEnsure: true, launchFlags: launchFlags{Launcher: c.Launcher, Agent: c.Agent, Cmd: c.Cmd}}).Run(ctx)
	}
	return nil
}

func (c *WSCreateCmd) create(ctx *Context) error {
	cfg, err := ctx.LoadConfig()
	if err != nil {
		return err
	}
	st, err := ctx.LoadState()
	if err != nil {
		return err
	}
	declared := cfg.FindWorkspace(c.Name) != nil
	if !c.flags() {
		if c.Save {
			return fmt.Errorf("--save needs the workspace's --repo, --set or --path")
		}
		if !declared {
			return fmt.Errorf("workspace %q is not declared in config; give its --repo, --set or --path", c.Name)
		}
		return ensureWorkspaces(ctx, cfg, st, c.Name, nil, true)
	}

	w := c.workspace()
	if err := cfg.CheckWorkspace(w); err != nil {
		return err
	}
	if c.Save {
		if err := config.SaveWorkspace(ctx.ConfigPath(), w, c.Force); err != nil {
			return err
		}
		if cfg, err = ctx.ReloadConfig(); err != nil {
			return err
		}
		if !ctx.Quiet {
			fmt.Printf("Saved workspace %q to config.\n", c.Name)
		}
		logEvent(ctx, "ws_create", c.Name, "saved")
		return ensureWorkspaces(ctx, cfg, st, c.Name, nil, true)
	}
	if declared {
		return fmt.Errorf("workspace %q is declared in config; edit it there and run 'git-mgr ws ensure %s', or --save --force to replace it", c.Name, c.Name)
	}
	if st.Workspaces[c.Name] != nil {
		return fmt.Errorf("workspace %q already exists; 'git-mgr ws ensure %s' makes what is missing of it", c.Name, c.Name)
	}
	logEvent(ctx, "ws_create", c.Name, "")
	return ensureWorkspaces(ctx, cfg, st, c.Name, []config.Workspace{w}, true)
}

// WSEnsureCmd makes what is missing of workspaces.
type WSEnsureCmd struct {
	Name string `arg:"" optional:"" help:"Workspace to ensure; every one if omitted." predictor:"workspace"`
}

func (c *WSEnsureCmd) Run(ctx *Context) error {
	cfg, err := ctx.LoadConfig()
	if err != nil {
		return err
	}
	st, err := ctx.LoadState()
	if err != nil {
		return err
	}
	if c.Name != "" && st.Workspaces[c.Name] == nil && cfg.FindWorkspace(c.Name) == nil {
		return fmt.Errorf("workspace %q is neither declared in config nor recorded", c.Name)
	}
	return ctx.finishChanges(ensureWorkspaces(ctx, cfg, st, c.Name, nil, true))
}

// ensureWorkspaces makes what is missing of the named workspace, or of all of
// them: workspaces declared in config or given in extra, agents and repos
// added to them, copies gone from disk, and the repos they name that aren't
// tracked yet. Moves and removals wait for `git-mgr apply`.
func ensureWorkspaces(ctx *Context, cfg *config.Config, st *state.State, name string, extra []config.Workspace, sayIfNothing bool) error {
	scope := workspace.Scope{Extra: extra}
	if name != "" {
		scope.Only = func(n string) bool { return n == name }
	}
	plan := workspace.Compute(ctx.GoCtx(), cfg, st, scope)
	if plan.Empty() {
		if sayIfNothing && !ctx.Quiet {
			fmt.Println("Workspaces match config.")
		}
		return nil
	}
	results := workspace.Apply(ctx.GoCtx(), cfg, reconcileEnv(cfg, st), plan, scope, reconcile.Safe)
	reportApplied(ctx, plan, results, reconcile.Safe)
	return changesOutcome(results, plan)
}

// WSAdoptCmd records an existing directory of checkouts as a workspace.
type WSAdoptCmd struct {
	Name string `arg:"" help:"Name to record the workspace under."`
	Dir  string `arg:"" help:"Directory whose git subdirectories become agents' copies." type:"path"`
}

func (c *WSAdoptCmd) Run(ctx *Context) error {
	cfg, err := ctx.LoadConfig()
	if err != nil {
		return err
	}
	if err := cfg.CheckWorkspace(config.Workspace{Name: c.Name, Path: c.Dir}); err != nil {
		return err
	}
	st, err := ctx.LoadState()
	if err != nil {
		return err
	}
	ws, err := scanWorkspace(ctx, st, c.Name, c.Dir)
	if err != nil {
		return err
	}
	err = st.Update(cfg.LockTimeout, func(st *state.State) error {
		if st.Workspaces[c.Name] != nil {
			return fmt.Errorf("workspace %q already exists", c.Name)
		}
		st.Workspaces[c.Name] = ws
		return nil
	})
	if err != nil {
		return err
	}
	logEvent(ctx, "ws_create", c.Name, "adopted "+ws.Dir)
	fmt.Printf("Adopted %q: %d agents under %s (%s)\n", c.Name, len(ws.Replicas), ws.Dir, ws.Isolation)
	for _, r := range ws.Replicas {
		repo := r.Copies[0].Repo
		if repo == "" {
			repo = "untracked repo"
		}
		fmt.Printf("  %s: %s\n", r.Agent, repo)
	}
	return ctx.finish(ws, nil)
}

// scanWorkspace builds a workspace from the git checkouts directly in dir:
// one agent per checkout, named after its folder. They are worktrees when
// every one is a linked worktree, else clones.
func scanWorkspace(ctx *Context, st *state.State, name, dir string) (*state.WorkspaceState, error) {
	abs, err := filepath.Abs(config.ExpandHome(dir))
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return nil, fmt.Errorf("scanning %s: %w", abs, err)
	}
	ws := &state.WorkspaceState{Name: name, Dir: abs, Isolation: config.IsolationWorktree, CreatedAt: time.Now()}
	for _, e := range entries {
		p := filepath.Join(abs, e.Name())
		if !e.IsDir() || gitops.IsStaging(e.Name()) || !gitops.HasGitDir(p) {
			continue
		}
		dirs := gitops.ResolveGitDirs(ctx.GoCtx(), p)
		if dirs.GitDir == dirs.CommonDir {
			ws.Isolation = config.IsolationClone
		}
		cs := state.CopyState{Path: p, CopyID: gitops.ReadCopyID(ctx.GoCtx(), p)}
		if id, err := gitops.ParseRemoteURL(ctx.GoCtx(), p); err == nil && !repourl.HasCredentials(id.URL) {
			if st.Repos[id.Key()] != nil {
				cs.Repo = id.Key()
			}
			cs.URL = id.URL
		}
		if b, err := gitops.Git(ctx.GoCtx(), p, "symbolic-ref", "--short", "-q", "HEAD"); err == nil {
			cs.Branch = b
		}
		ws.Replicas = append(ws.Replicas, state.ReplicaState{Agent: e.Name(), Path: p, Copies: []state.CopyState{cs}})
	}
	if len(ws.Replicas) == 0 {
		return nil, fmt.Errorf("no git repositories found under %s", abs)
	}
	if ws.Isolation == config.IsolationWorktree {
		for i := range ws.Replicas {
			ws.Replicas[i].Copies[0].URL = ""
		}
	}
	return ws, nil
}

// WSListCmd lists workspaces.
type WSListCmd struct{}

func (c *WSListCmd) Run(ctx *Context) error {
	st, err := ctx.LoadState()
	if err != nil {
		return err
	}
	cfg, _ := ctx.LoadConfig() // best-effort: list declared-but-not-created too

	names := map[string]bool{}
	for n := range st.Workspaces {
		names[n] = true
	}
	if cfg != nil {
		for _, w := range cfg.Workspaces {
			names[w.Name] = true
		}
	}
	// A workspace, as ws ls --json lists it.
	type listed struct {
		Name      string `json:"name"`
		Isolation string `json:"isolation"`
		Agents    int    `json:"agents"`
		Copies    int    `json:"copies"`
		// Status is created, declared (not created yet) or removed from config.
		Status string `json:"status"`
		Dir    string `json:"dir"`
	}
	rows := []listed{}
	for _, n := range slices.Sorted(maps.Keys(names)) {
		if ws, ok := st.Workspaces[n]; ok {
			copies := 0
			for _, r := range ws.Replicas {
				copies += len(r.Copies)
			}
			status := "created"
			if cfg != nil && cfg.FindWorkspace(n) == nil && ws.Declared {
				status = "removed from config"
			}
			rows = append(rows, listed{n, ws.Isolation, len(ws.Replicas), copies, status, ws.Dir})
			continue
		}
		w := cfg.WithDefaults(*cfg.FindWorkspace(n))
		rows = append(rows, listed{n, w.Isolation, w.Agents, 0, "declared", w.Dir()})
	}
	if ctx.JSON {
		return ctx.Emit(rows)
	}
	if len(rows) == 0 {
		fmt.Println("No workspaces. Declare one in config, or create one with 'git-mgr ws create <name> --repo <repo>'.")
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "WORKSPACE\tISOLATION\tAGENTS\tCOPIES\tSTATUS\tDIR")
	for _, r := range rows {
		copies := strconv.Itoa(r.Copies)
		if r.Status == "declared" {
			copies = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%d\t%s\t%s\t%s\n", r.Name, r.Isolation, r.Agents, copies, r.Status, tildify(r.Dir))
	}
	return tw.Flush()
}

// WSRemoveCmd forgets a workspace or removes its copies.
type WSRemoveCmd struct {
	Name    string        `arg:"" help:"Workspace to remove." predictor:"workspace"`
	Delete  bool          `help:"Also retire each copy: archive its unique work, then remove it."`
	Yes     bool          `help:"Skip confirmation."`
	Force   bool          `help:"Discard copies' unique work instead of archiving it."`
	Offsite bool          `help:"Push each copy's WIP and unpushed commits offsite (remote_sync) before deleting it; a copy that can't be pushed is kept."`
	Stop    bool          `help:"Stop running agents first, instead of keeping their copies."`
	Timeout time.Duration `help:"With --stop, how long to wait for each run to end." default:"30s"`
}

func (c *WSRemoveCmd) Run(ctx *Context) error {
	cfg, err := ctx.LoadConfig()
	if err != nil {
		return err
	}
	// Unlocked through the prompt and the removals; recording them is one
	// locked write at the end.
	st, err := ctx.LoadState()
	if err != nil {
		return err
	}
	ws, ok := st.Workspaces[c.Name]
	if !ok {
		return fmt.Errorf("workspace %q is not created", c.Name)
	}
	spec := workspace.FromState(cfg, st, ws)
	copies := spec.Copies()

	if !c.Yes && ctx.JSON {
		return errNeedsYes("ws rm")
	}
	if !c.Yes {
		action := "forget"
		if c.Delete {
			action = "delete"
			if c.Force {
				action = "delete, discarding unique work in,"
			}
		}
		if !confirm(fmt.Sprintf("Remove workspace %q: %s its %d copies?", c.Name, action, len(copies))) {
			fmt.Println("Cancelled.")
			return nil
		}
	}
	if cfg.FindWorkspace(c.Name) != nil && !ctx.Quiet {
		fmt.Fprintf(os.Stderr, "note: %q is still declared in config, so the next apply makes it again; remove it there too.\n", c.Name)
	}

	if !c.Delete {
		err := st.Update(cfg.LockTimeout, func(st *state.State) error {
			delete(st.Workspaces, c.Name)
			return nil
		})
		if err == nil {
			for _, r := range ws.Replicas {
				workspace.ForgetRuns(st.Dir(), c.Name, r.Agent)
			}
			logEvent(ctx, "ws_remove", c.Name, "forgot; files kept")
		}
		return ctx.finish(map[string]any{"workspace": c.Name, "forgotten": err == nil}, err)
	}

	machine := ""
	if c.Offsite && !c.Force {
		if machine, err = ensureMachine(cfg, st); err != nil {
			return err
		}
	}
	opts := workspace.RetireOptions{StateDir: st.Dir(), ArchiveDir: cfg.Backup.ArchiveDir, Stop: c.Stop, StopTimeout: c.Timeout, Discard: c.Force}
	retired := map[string]workspace.Retired{}
	var doc retiredJSON
	kept := 0
	for _, r := range ws.Replicas {
		var paths []string
		for _, l := range workspace.OfAgent(copies, r.Agent) {
			if c.Offsite && !c.Force && gitops.HasGitDir(l.Path) {
				if cs := st.CopyAt(c.Name, l.Agent, l.Path); cs != nil {
					job := copyJobFor(cfg, st, ws, l.Agent, *cs, true)
					job.offsite = copyOffsite(cfg, st, machine, c.Name, *cs, job.sshKey)
					if errs := runCopies(ctx.GoCtx(), ctx.Logger(), []copyJob{job}, 1)[0].failed(); len(errs) > 0 {
						kept++
						fmt.Fprintf(os.Stderr, "  %s: kept, it couldn't be taken offsite: %v\n", l.Label(spec), errors.Join(errs...))
						doc.Kept = append(doc.Kept, targetError{l.Label(spec), "couldn't be taken offsite: " + errors.Join(errs...).Error()})
						continue
					}
				}
			}
			paths = append(paths, l.Path)
		}
		if len(paths) == 0 {
			continue
		}
		res := workspace.Retire(ctx.GoCtx(), spec, ws, r.Agent, paths, opts)
		retired[r.Agent] = res
		kept += len(res.Kept)
		reportRetired(spec, res, ctx.Quiet)
		doc.add(spec, res)
	}
	if err := recordRetired(cfg, st, c.Name, retired, kept == 0); err != nil {
		return err
	}
	if doc.Removed == nil {
		doc.Removed = []copyRefJSON{}
	}
	if kept > 0 {
		return ctx.finish(doc, outcome(kept, len(copies), "copies of %q could not be removed and are still recorded", c.Name))
	}
	if err := workspace.RemoveCache(spec.Settings.CacheDir); err != nil {
		ctx.Logger().Warn("workspace removed, but not its agents' cache", "workspace", c.Name, "error", err)
	}
	logEvent(ctx, "ws_remove", c.Name, "")
	return ctx.finish(doc, nil)
}

// reportRetired prints what a retirement removed and kept.
func reportRetired(spec *workspace.Spec, r workspace.Retired, quiet bool) {
	archived := map[string]string{}
	for _, a := range r.Archives {
		archived[a.Source] = a.Path
	}
	for _, l := range r.Removed {
		if quiet {
			continue
		}
		if a := archived[l.Path]; a != "" {
			fmt.Printf("  %s: removed, unique work archived to %s\n", l.Label(spec), tildify(a))
		} else {
			fmt.Printf("  %s: removed\n", l.Label(spec))
		}
	}
	for _, k := range r.Kept {
		fmt.Fprintf(os.Stderr, "  %s: kept: %v\n", k.Label(spec), k.Err)
	}
}

// recordRetired drops the retired copies from state with their tombstones
// and archives, forgetting the workspace when forget is set and nothing of
// it is left, then tidies the directories and leases they left.
func recordRetired(cfg *config.Config, st *state.State, name string, retired map[string]workspace.Retired, forget bool) error {
	err := st.Update(cfg.LockTimeout, func(st *state.State) error {
		ws := st.Workspaces[name]
		if ws == nil {
			return nil
		}
		for agent, r := range retired {
			workspace.DropCopies(ws, agent, r.Paths()...)
			st.Bury(r.Tombstones, r.Archives)
		}
		if forget && (len(ws.Replicas) == 0 || ws.Isolation == config.IsolationShared) {
			delete(st.Workspaces, name)
		}
		return nil
	})
	if err != nil {
		return err
	}
	env := reconcileEnv(cfg, st)
	for agent, r := range retired {
		for _, p := range r.Paths() {
			reconcile.RemoveEmptyParents(env, p)
		}
		if ws := st.Workspaces[name]; ws == nil || ws.Replica(agent) == nil {
			workspace.ForgetRuns(st.Dir(), name, agent)
		}
	}
	return nil
}

// operationalSpec returns a created workspace as state records it, the
// source of truth for what exists on disk.
func operationalSpec(ctx *Context, name string) (*workspace.Spec, *state.State, error) {
	st, err := ctx.LoadState()
	if err != nil {
		return nil, nil, err
	}
	ws, ok := st.Workspaces[name]
	if !ok {
		return nil, nil, fmt.Errorf("workspace %q is not created; run 'git-mgr ws create %s' first", name, name)
	}
	cfg, _ := ctx.LoadConfig()
	return workspace.FromState(cfg, st, ws), st, nil
}
