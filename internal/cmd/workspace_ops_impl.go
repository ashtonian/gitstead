package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/ashtonian/git-mgr/internal/config"
	"github.com/ashtonian/git-mgr/internal/gitops"
	"github.com/ashtonian/git-mgr/internal/state"
	"github.com/ashtonian/git-mgr/internal/workspace"
)

// WSStatusCmd shows the status of a workspace's copies.
type WSStatusCmd struct {
	Name string `arg:"" help:"Workspace name." predictor:"workspace"`
	JSON bool   `help:"Output JSON."`
}

func (c *WSStatusCmd) Run(ctx *Context) error {
	spec, st, err := operationalSpec(ctx, c.Name)
	if err != nil {
		return err
	}
	hs := workspace.Health(ctx.GoCtx(), spec, spec.Targets())
	runs := agentRuns(st, c.Name)
	if c.JSON {
		views := healthViews(spec, hs)
		for i := range views {
			views[i].addRun(runs)
		}
		return printIndentedJSON(views)
	}
	printAgents(st, c.Name, runs)
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "COPY\tBRANCH\tSTATE\tUPSTREAM\tAHEAD/BEHIND\tNOTES")
	for _, h := range hs {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d/%d\t%s\n",
			shortLabel(spec, h.Located), dashIfEmpty(h.Branch), h.State(), dashIfEmpty(h.Upstream),
			h.Ahead, h.Behind, strings.Join(h.Issues, "; "))
	}
	tw.Flush()
	if workspace.OK(hs) {
		fmt.Printf("\n%s: %d %s, all up to date\n", c.Name, len(hs), plural(len(hs), "copy", "copies"))
	} else {
		fmt.Printf("\n%s: %d of %d copies need attention\n", c.Name, workspace.Flagged(hs), len(hs))
	}
	return nil
}

// WSOverviewCmd shows copy health across every workspace: who is dirty,
// ahead, stuck or done.
type WSOverviewCmd struct {
	JSON bool `help:"Output JSON."`
}

func (c *WSOverviewCmd) Run(ctx *Context) error {
	st, err := ctx.LoadState()
	if err != nil {
		return err
	}
	if len(st.Workspaces) == 0 {
		fmt.Println("No workspaces created. See 'git-mgr ws create --help'.")
		return nil
	}
	cfg, _ := ctx.LoadConfig()
	type row struct {
		spec *workspace.Spec
		h    workspace.CopyHealth
		runs map[string]agentRun
	}
	var rows []row
	for _, n := range slices.Sorted(maps.Keys(st.Workspaces)) {
		spec := workspace.FromState(cfg, st, st.Workspaces[n])
		runs := agentRuns(st, n)
		for _, h := range workspace.Health(ctx.GoCtx(), spec, spec.Targets()) {
			rows = append(rows, row{spec, h, runs})
		}
	}
	if c.JSON {
		views := make([]copyStatusJSON, 0, len(rows))
		for _, r := range rows {
			v := healthView(r.spec, r.h)
			v.addRun(r.runs)
			views = append(views, v)
		}
		return printIndentedJSON(views)
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "COPY\tAGENT\tBRANCH\tSTATE\tAHEAD/BEHIND\tNOTES")
	counts := map[string]int{}
	for _, r := range rows {
		s := r.h.State()
		counts[s]++
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d/%d\t%s\n", r.h.Label(r.spec), dashIfEmpty(r.runs[r.h.Agent].state), dashIfEmpty(r.h.Branch), s, r.h.Ahead, r.h.Behind, strings.Join(r.h.Issues, "; "))
	}
	tw.Flush()
	var parts []string
	for _, s := range []string{"clean", "dirty", "untracked", "rebase", "merge", "cherry-pick", "missing"} {
		if counts[s] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", counts[s], s))
		}
	}
	fmt.Printf("\n%d copies across %d workspaces: %s\n", len(rows), len(st.Workspaces), strings.Join(parts, ", "))
	return nil
}

// WSCheckCmd checks a workspace is set up and up to date.
type WSCheckCmd struct {
	Name  string `arg:"" help:"Workspace name." predictor:"workspace"`
	Agent string `help:"Only check this agent's copies."`
}

func (c *WSCheckCmd) Run(ctx *Context) error {
	spec, _, err := operationalSpec(ctx, c.Name)
	if err != nil {
		return err
	}
	targets, err := agentTargets(spec, c.Agent)
	if err != nil {
		return err
	}
	hs := workspace.Health(ctx.GoCtx(), spec, targets)
	for _, h := range hs {
		if len(h.Issues) == 0 {
			if !ctx.Quiet {
				fmt.Printf("  ok    %s (%s)\n", shortLabel(spec, h.Located), dashIfEmpty(h.Branch))
			}
			continue
		}
		fmt.Printf("  FLAG  %s: %s\n", shortLabel(spec, h.Located), strings.Join(h.Issues, "; "))
	}
	if !workspace.OK(hs) {
		return fmt.Errorf("%s: %d copies need attention", c.Name, workspace.Flagged(hs))
	}
	if !ctx.Quiet {
		fmt.Printf("%s: all %d copies up to date\n", c.Name, len(hs))
	}
	return nil
}

// WSFetchCmd fetches each of a workspace's repos once and brings every copy
// up to date from it.
type WSFetchCmd struct {
	Name string `arg:"" help:"Workspace name." predictor:"workspace"`
}

func (c *WSFetchCmd) Run(ctx *Context) error {
	cfg, err := ctx.LoadConfig()
	if err != nil {
		return err
	}
	spec, st, err := operationalSpec(ctx, c.Name)
	if err != nil {
		return err
	}
	results := fetchCopies(ctx.GoCtx(), cfg, st, spec, spec.Targets())
	return reportOps(spec, "fetched", results, ctx.Quiet)
}

// fetchCopies fetches the repos targets are copies of, each once -- worktrees
// share their repo's fetch -- then brings each clone up to date from its
// repo's clone. A copy of a repo git-mgr can't find fetches on its own.
func fetchCopies(ctx context.Context, cfg *config.Config, st *state.State, spec *workspace.Spec, targets []workspace.Located) []workspace.OpResult {
	sources := map[string]error{}
	for _, t := range targets {
		if src := workspace.SourceOf(ctx, t.Copy); src != "" {
			sources[src] = nil
		}
	}
	for src := range sources {
		key, rule := "", ""
		for k, r := range st.Repos {
			if r.LocalPath == src {
				key, rule = k, r.DiscoveredBy
			}
		}
		sources[src] = gitops.Fetch(ctx, src, cfg.ResolveSSHKey(key, rule))
	}
	return workspace.ForEach(ctx, targets, cfg.Concurrency, func(ctx context.Context, t workspace.Located) (string, bool, error) {
		src := workspace.SourceOf(ctx, t.Copy)
		switch {
		case src == "":
			return "", false, gitops.Fetch(ctx, t.Path, t.SSHKey)
		case sources[src] != nil:
			return "", false, fmt.Errorf("fetching its repo at %s: %w", src, sources[src])
		case spec.Isolation == config.IsolationWorktree || spec.Shared():
			return "", false, nil
		default:
			return "", false, gitops.RefreshFromSource(ctx, t.Path, src)
		}
	})
}

// WSCommitCmd commits changes in every copy.
type WSCommitCmd struct {
	Name    string `arg:"" help:"Workspace name." predictor:"workspace"`
	Message string `short:"m" required:"" help:"Commit message."`
	AddAll  bool   `name:"add-all" help:"Also stage untracked files (git add -A)."`
	Agent   string `help:"Only commit this agent's copies."`
}

func (c *WSCommitCmd) Run(ctx *Context) error {
	cfg, err := ctx.LoadConfig()
	if err != nil {
		return err
	}
	spec, _, err := operationalSpec(ctx, c.Name)
	if err != nil {
		return err
	}
	targets, err := agentTargets(spec, c.Agent)
	if err != nil {
		return err
	}
	results := workspace.ForEach(ctx.GoCtx(), targets, cfg.Concurrency, func(ctx context.Context, t workspace.Located) (string, bool, error) {
		committed, err := gitops.Commit(ctx, t.Path, c.Message, c.AddAll)
		if err != nil {
			return "", false, err
		}
		if !committed {
			return "nothing to commit", true, nil
		}
		return "committed", false, nil
	})
	return reportOps(spec, "committed", results, ctx.Quiet)
}

// WSPullCmd pulls every copy.
type WSPullCmd struct {
	Name        string `arg:"" help:"Workspace name." predictor:"workspace"`
	Rebase      bool   `help:"Use --rebase instead of the default --ff-only."`
	BackupFirst bool   `help:"Back up copies with uncommitted work before pulling." default:"true" name:"backup-first"`
}

func (c *WSPullCmd) Run(ctx *Context) error {
	cfg, err := ctx.LoadConfig()
	if err != nil {
		return err
	}
	spec, _, err := operationalSpec(ctx, c.Name)
	if err != nil {
		return err
	}
	targets := spec.Targets()
	var unsaved map[string]error
	if c.BackupFirst {
		unsaved = backupDirtyCopies(ctx, cfg, spec, targets)
	}
	results := workspace.ForEach(ctx.GoCtx(), targets, cfg.Concurrency, func(ctx context.Context, t workspace.Located) (string, bool, error) {
		if err := unsaved[t.Path]; err != nil {
			return "", false, fmt.Errorf("not pulled, safety backup failed: %w", err)
		}
		out, err := gitops.Pull(ctx, t.Path, t.SSHKey, !c.Rebase, c.Rebase)
		return lastLine(out), false, err
	})
	return reportOps(spec, "pulled", results, ctx.Quiet)
}

// backupDirtyCopies backs up every copy with uncommitted work before an
// operation that changes it, so a bad pull never loses it. It returns the
// copies, by path, whose backup failed, which the operation must leave alone.
func backupDirtyCopies(ctx *Context, cfg *config.Config, spec *workspace.Spec, targets []workspace.Located) map[string]error {
	failed := map[string]error{}
	for _, t := range targets {
		if !gitops.HasGitDir(t.Path) {
			continue
		}
		br, err := gitops.Backup(ctx.GoCtx(), t.Path, backupConfigFor(cfg.ResolveForRepo(t.Repo)))
		if err != nil {
			failed[t.Path] = err
			continue
		}
		if br.Dirty() && !ctx.Quiet {
			fmt.Printf("  safety backup: %s (%s)\n", shortLabel(spec, t), describeBackup(br))
		}
	}
	return failed
}

// WSPushCmd pushes every copy.
type WSPushCmd struct {
	Name        string `arg:"" help:"Workspace name." predictor:"workspace"`
	SetUpstream bool   `name:"set-upstream" help:"Publish each branch as origin/<branch>."`
}

func (c *WSPushCmd) Run(ctx *Context) error {
	cfg, err := ctx.LoadConfig()
	if err != nil {
		return err
	}
	spec, _, err := operationalSpec(ctx, c.Name)
	if err != nil {
		return err
	}
	results := workspace.ForEach(ctx.GoCtx(), spec.Targets(), cfg.Concurrency, func(ctx context.Context, t workspace.Located) (string, bool, error) {
		out, err := gitops.Push(ctx, t.Path, t.SSHKey, c.SetUpstream)
		return lastLine(out), false, err
	})
	return reportOps(spec, "pushed", results, ctx.Quiet)
}

// WSExecCmd runs a command in each copy.
type WSExecCmd struct {
	Name    string   `arg:"" help:"Workspace name." predictor:"workspace"`
	Agent   string   `help:"Only run in this agent's copies."`
	Command []string `arg:"" help:"Command to run in each copy (after --)."`
}

func (c *WSExecCmd) Run(ctx *Context) error {
	if len(c.Command) == 0 {
		return fmt.Errorf("no command given (usage: git-mgr ws exec <name> -- <command>)")
	}
	cfg, err := ctx.LoadConfig()
	if err != nil {
		return err
	}
	spec, _, err := operationalSpec(ctx, c.Name)
	if err != nil {
		return err
	}
	targets, err := agentTargets(spec, c.Agent)
	if err != nil {
		return err
	}
	results := workspace.ForEach(ctx.GoCtx(), targets, cfg.Concurrency, func(ctx context.Context, t workspace.Located) (string, bool, error) {
		cmd := exec.CommandContext(ctx, c.Command[0], c.Command[1:]...)
		cmd.Dir = t.Path
		out, err := cmd.CombinedOutput()
		return strings.TrimRight(string(out), "\n"), false, err
	})
	failed := 0
	for _, r := range results {
		fmt.Printf("=== %s ===\n", shortLabel(spec, r.Located))
		if r.Output != "" {
			fmt.Println(r.Output)
		}
		if r.Err != nil {
			failed++
			fmt.Printf("(error: %v)\n", r.Err)
		}
	}
	return outcome(failed, len(results), "copies failed")
}

// WSBackupCmd backs up every copy's uncommitted work.
type WSBackupCmd struct {
	Name    string `arg:"" help:"Workspace to back up." predictor:"workspace"`
	Agent   string `help:"Only back up this agent's copies."`
	Offsite bool   `help:"Also push each copy's WIP and unpushed commits offsite (remote_sync), even when remote_sync.enabled is off."`
}

func (c *WSBackupCmd) Run(ctx *Context) error {
	cfg, err := ctx.LoadConfig()
	if err != nil {
		return err
	}
	st, err := ctx.LoadState()
	if err != nil {
		return err
	}
	ws, ok := st.Workspaces[c.Name]
	if !ok {
		return fmt.Errorf("workspace %q is not created; run 'git-mgr ws create %s' first", c.Name, c.Name)
	}
	machine := ""
	if c.Offsite {
		if machine, err = ensureMachine(cfg, st); err != nil {
			return err
		}
	}

	var jobs []copyJob
	for _, r := range ws.Replicas {
		if c.Agent != "" && r.Agent != c.Agent {
			continue
		}
		for _, cs := range r.Copies {
			if !gitops.HasGitDir(cs.Path) {
				continue
			}
			job := copyJobFor(cfg, st, ws, r.Agent, cs, true)
			if c.Offsite {
				job.offsite = copyOffsite(cfg, st, machine, c.Name, cs, job.sshKey)
			}
			if job.backup != nil || job.offsite != nil {
				jobs = append(jobs, job)
			}
		}
	}
	outcomes := runCopies(ctx.GoCtx(), ctx.Logger(), jobs, cfg.Concurrency)
	if err := st.Update(cfg.LockTimeout, func(st *state.State) error {
		recordCopies(st, outcomes)
		return nil
	}); err != nil {
		return fmt.Errorf("recording backups: %w", err)
	}

	saved, failed := 0, 0
	for _, o := range outcomes {
		if errs := o.failed(); len(errs) > 0 {
			failed++
			fmt.Fprintf(os.Stderr, "  %s: %v\n", o.job.label, errors.Join(errs...))
		}
		if br := o.backup; br != nil {
			if br.Outcome == gitops.BackupSaved {
				saved++
			}
			if br.Dirty() && (!ctx.Quiet || len(br.Skipped) > 0) {
				fmt.Printf("  %s: %s\n", o.job.label, describeBackup(br))
			}
		}
		if !o.offsitePlan.Empty() && !ctx.Quiet {
			fmt.Printf("  %s: %s\n", o.job.label, describeOffsite(o.offsitePlan, false))
		}
	}
	if !ctx.Quiet {
		fmt.Printf("Saved %d new backup(s) for %q\n", saved, c.Name)
	}
	return outcome(failed, len(jobs), "copies of %q failed to back up", c.Name)
}

// WSRecoverCmd recovers a copy's backed-up work with the restore machinery.
type WSRecoverCmd struct {
	Name  string `arg:"" help:"Workspace name." predictor:"workspace"`
	Agent string `help:"Only recover this agent's copies."`
	Into  string `help:"Clone and apply into this scratch dir (needs a single copy: --agent, in a one-repo workspace)." default:""`
	Apply bool   `help:"Apply the backup onto the copy's working tree (refuses a dirty one without --force)."`
	Force bool   `help:"With --apply, apply onto a dirty working tree."`
}

func (c *WSRecoverCmd) Run(ctx *Context) error {
	cfg, err := ctx.LoadConfig()
	if err != nil {
		return err
	}
	spec, _, err := operationalSpec(ctx, c.Name)
	if err != nil {
		return err
	}
	targets, err := agentTargets(spec, c.Agent)
	if err != nil {
		return err
	}
	if c.Into != "" && len(targets) != 1 {
		return fmt.Errorf("--into needs a single copy (use --agent)")
	}

	found, failed, attempted := 0, 0, 0
	for _, t := range targets {
		label := t.Label(spec)
		if !gitops.HasGitDir(t.Path) {
			fmt.Printf("  %s: not present (run 'git-mgr ws ensure %s' to make it again, then recover)\n", label, c.Name)
			continue
		}
		patchDir := cfg.ResolveForRepo(t.Repo).Backup.PatchDir
		b, err := gitops.FindBackup(ctx.GoCtx(), t.Path, patchDir, "latest")
		if errors.Is(err, gitops.ErrNoBackup) {
			fmt.Printf("  %s: no local backup found\n", label)
			continue
		}
		attempted++
		if err != nil {
			failed++
			fmt.Fprintf(os.Stderr, "  %s: %v\n", label, err)
			continue
		}
		found++
		if err := restoreBackup(ctx, t.Path, label, b, c.Into, c.Apply, c.Force); err != nil {
			failed++
			fmt.Fprintf(os.Stderr, "  %s: %v\n", label, err)
		}
	}
	if found == 0 {
		fmt.Println("No local backups found. `git-mgr remote-sync --list` shows what went offsite, and how to restore it.")
	}
	return outcome(failed, attempted, "copies could not be recovered")
}

// --- shared helpers ---

// agentTargets returns the workspace's targets, only the agent's when agent
// is set.
func agentTargets(spec *workspace.Spec, agent string) ([]workspace.Located, error) {
	targets := spec.Targets()
	if agent == "" {
		return targets, nil
	}
	if spec.Shared() {
		return nil, fmt.Errorf("workspace %q is shared: its agents have no copies of their own", spec.Name)
	}
	targets = workspace.OfAgent(targets, agent)
	if len(targets) == 0 {
		return nil, fmt.Errorf("no agent %q in workspace %q", agent, spec.Name)
	}
	return targets, nil
}

// shortLabel names a copy within its workspace: agent[/folder], or the
// shared folder.
func shortLabel(spec *workspace.Spec, l workspace.Located) string {
	if l.Agent == "" {
		return tildify(l.Path)
	}
	return strings.TrimPrefix(l.Label(spec), spec.Name+"/")
}

func reportOps(spec *workspace.Spec, verb string, results []workspace.OpResult, quiet bool) error {
	ok, skipped, failed := 0, 0, 0
	for _, r := range results {
		label := shortLabel(spec, r.Located)
		switch {
		case r.Err != nil:
			failed++
			fmt.Printf("  %s: ERROR %v\n", label, r.Err)
		case r.Skipped:
			skipped++
			if !quiet {
				fmt.Printf("  %s: %s\n", label, orDefault(r.Output, "skipped"))
			}
		default:
			ok++
			if !quiet {
				fmt.Printf("  %s: %s\n", label, orDefault(r.Output, verb))
			}
		}
	}
	if !quiet {
		fmt.Printf("%s: %d %s, %d skipped, %d failed\n", spec.Name, ok, verb, skipped, failed)
	}
	return outcome(failed, len(results), "copies failed")
}

// copyStatusJSON is the stable JSON shape of a copy's health, shared by
// `ws status --json` and `ws overview --json`.
type copyStatusJSON struct {
	Workspace string `json:"workspace"`
	Agent     string `json:"agent,omitempty"`
	// AgentState is the agent's lifecycle state: ready, running, stopping,
	// stopped or missing; Run its latest run.
	AgentState string          `json:"agent_state,omitempty"`
	Run        *state.RunState `json:"run,omitempty"`
	Repo       string          `json:"repo,omitempty"`
	Path       string          `json:"path"`
	Branch     string          `json:"branch,omitempty"`
	State      string          `json:"state"` // missing|clean|dirty|untracked|rebase|merge|cherry-pick
	Present    bool            `json:"present"`
	Dirty      bool            `json:"dirty"`
	Untracked  bool            `json:"untracked"`
	Locked     bool            `json:"locked"`
	Stuck      string          `json:"stuck,omitempty"`
	Upstream   string          `json:"upstream,omitempty"`
	Ahead      int             `json:"ahead"`
	Behind     int             `json:"behind"`
	Issues     []string        `json:"issues,omitempty"`
}

// agentRun is an agent's lifecycle state and latest run.
type agentRun struct {
	state string
	run   *state.RunState
}

// agentRuns returns the state of each agent of a recorded workspace.
func agentRuns(st *state.State, name string) map[string]agentRun {
	out := map[string]agentRun{}
	ws := st.Workspaces[name]
	if ws == nil {
		return out
	}
	for i := range ws.Replicas {
		r := &ws.Replicas[i]
		out[r.Agent] = agentRun{workspace.AgentState(st.Dir(), name, r), r.Run}
	}
	return out
}

func (v *copyStatusJSON) addRun(runs map[string]agentRun) {
	if ar, ok := runs[v.Agent]; ok {
		v.AgentState, v.Run = ar.state, ar.run
	}
}

// printAgents lists a workspace's agents and their runs.
func printAgents(st *state.State, name string, runs map[string]agentRun) {
	ws := st.Workspaces[name]
	if ws == nil || len(ws.Replicas) == 0 {
		return
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "AGENT\tSTATE\tPROFILE\tLAST RUN")
	for _, r := range ws.Replicas {
		ar := runs[r.Agent]
		profile, last := "-", "-"
		if run := ar.run; run != nil {
			profile = dashIfEmpty(run.Profile)
			switch ar.state {
			case workspace.AgentRunning, workspace.AgentStopping:
				last = fmt.Sprintf("since %s (pid %d)", run.Started.Local().Format(time.DateTime), run.PID)
			default:
				if !run.Ended.IsZero() {
					last = fmt.Sprintf("exit %d at %s", run.ExitCode, run.Ended.Local().Format(time.DateTime))
				} else {
					last = fmt.Sprintf("started %s, end not recorded", run.Started.Local().Format(time.DateTime))
				}
			}
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", r.Agent, ar.state, profile, last)
	}
	tw.Flush()
	fmt.Println()
}

func healthViews(spec *workspace.Spec, hs []workspace.CopyHealth) []copyStatusJSON {
	views := make([]copyStatusJSON, 0, len(hs))
	for _, h := range hs {
		views = append(views, healthView(spec, h))
	}
	return views
}

func healthView(spec *workspace.Spec, h workspace.CopyHealth) copyStatusJSON {
	return copyStatusJSON{
		Workspace: spec.Name, Agent: h.Agent, Repo: h.Repo, Path: h.Path, Branch: h.Branch,
		State: h.State(), Present: h.Present, Dirty: h.Dirty, Untracked: h.Untracked, Locked: h.Locked,
		Stuck: h.Stuck, Upstream: h.Upstream, Ahead: h.Ahead, Behind: h.Behind, Issues: h.Issues,
	}
}

func printIndentedJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
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
