package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	agents "github.com/ashtonian/gitten/internal/agent"
	"github.com/ashtonian/gitten/internal/config"
	"github.com/ashtonian/gitten/internal/discovery"
	"github.com/ashtonian/gitten/internal/gitops"
	"github.com/ashtonian/gitten/internal/repourl"
	"github.com/ashtonian/gitten/internal/state"
	"github.com/ashtonian/gitten/internal/workspace"
)

// WSRetireCmd retires agents: their runs stopped (with --stop), their
// copies' unique work archived and verified, the copies removed, and a
// tombstone and catalog entry kept for each.
type WSRetireCmd struct {
	Name    string        `arg:"" help:"Workspace name." predictor:"workspace"`
	Agents  []string      `arg:"" optional:"" help:"Agents to retire."`
	All     bool          `help:"Retire every agent of the workspace."`
	Stop    bool          `help:"Stop running agents first, instead of keeping their copies."`
	Timeout time.Duration `help:"With --stop, how long to wait for each run to end." default:"30s"`
	Force   bool          `help:"Discard the copies' unique work instead of archiving it."`
	Yes     bool          `help:"Skip confirmation."`
}

func (c *WSRetireCmd) Run(ctx *Context) error {
	cfg, err := ctx.LoadConfig()
	if err != nil {
		return err
	}
	st, err := ctx.LoadState()
	if err != nil {
		return err
	}
	ws := st.Workspaces[c.Name]
	if ws == nil {
		return fmt.Errorf("workspace %q is not created", c.Name)
	}
	var names []string
	switch {
	case c.All && len(c.Agents) > 0:
		return errors.New("give agents or --all, not both")
	case c.All:
		for _, r := range ws.Replicas {
			names = append(names, r.Agent)
		}
	case len(c.Agents) == 0:
		return errors.New("give the agents to retire, or --all")
	default:
		for _, a := range c.Agents {
			if ws.Replica(a) == nil {
				return fmt.Errorf("no agent %q in workspace %q", a, c.Name)
			}
		}
		names = c.Agents
	}
	if !c.Yes && ctx.JSON {
		return errNeedsYes("ws retire")
	}
	if !c.Yes {
		what := "archive their unique work and remove their copies"
		if c.Force {
			what = "remove their copies, discarding their unique work"
		}
		if !confirm(fmt.Sprintf("Retire %s of %q: %s?", strings.Join(names, ", "), c.Name, what)) {
			fmt.Println("Cancelled.")
			return nil
		}
	}
	if w := cfg.FindWorkspace(c.Name); w != nil {
		if wanted, err := cfg.WithDefaults(*w).AgentNames(); err == nil {
			for _, a := range names {
				if slices.Contains(wanted, a) {
					fmt.Fprintf(os.Stderr, "note: %s is still declared (agents: %d), so the next apply makes it again; lower agents in config to keep it retired.\n", a, len(wanted))
					break
				}
			}
		}
	}

	spec := workspace.FromState(cfg, st, ws)
	if spec.Shared() && !c.Stop {
		for _, a := range names {
			if err := workspace.CheckIdle(st.Dir(), c.Name, a); err != nil {
				return err
			}
		}
	}
	if err := st.Update(cfg.LockTimeout, func(st *state.State) error {
		if ws := st.Workspaces[c.Name]; ws != nil {
			for _, a := range names {
				if r := ws.Replica(a); r != nil {
					r.Retiring = time.Now()
				}
			}
		}
		return nil
	}); err != nil {
		return err
	}

	opts := workspace.RetireOptions{StateDir: st.Dir(), ArchiveDir: cfg.Backup.ArchiveDir, Stop: c.Stop, StopTimeout: c.Timeout, Discard: c.Force, Timeout: cfg.Sync.CloneTimeout}
	retired := map[string]workspace.Retired{}
	var doc retiredJSON
	kept, total := 0, 0
	for _, a := range names {
		if spec.Shared() {
			retired[a] = workspace.Retired{}
			continue
		}
		r := workspace.Retire(ctx.GoCtx(), spec, ws, a, nil, opts)
		retired[a] = r
		kept += len(r.Kept)
		total += len(r.Kept) + len(r.Removed)
		reportRetired(spec, r, ctx.Quiet)
		doc.add(spec, r)
	}
	if spec.Shared() {
		if c.Stop {
			for _, a := range names {
				if _, err := agentsStop(st.Dir(), c.Name, a, c.Timeout); err != nil {
					return err
				}
			}
		}
		err = st.Update(cfg.LockTimeout, func(st *state.State) error {
			if ws := st.Workspaces[c.Name]; ws != nil {
				for _, a := range names {
					ws.DropReplica(a)
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		for _, a := range names {
			workspace.ForgetRuns(st.Dir(), c.Name, a)
		}
	} else if err := recordRetired(cfg, st, c.Name, retired, false); err != nil {
		return err
	}
	logEvent(ctx, "ws_retire", c.Name, strings.Join(names, ","))
	if kept > 0 {
		return ctx.finish(doc, outcome(kept, total, "copies could not be retired and are still recorded; their agents show as retiring"))
	}
	if !ctx.Quiet {
		fmt.Printf("Retired %s of %q.\n", strings.Join(names, ", "), c.Name)
	}
	return ctx.finish(doc, nil)
}

// WSPublishCmd pushes agents' branches and opens a pull request per repo.
type WSPublishCmd struct {
	Name   string   `arg:"" help:"Workspace name." predictor:"workspace"`
	Agents []string `arg:"" optional:"" help:"Agents whose branches to publish; every agent if omitted."`
	PR     bool     `help:"Open a pull (or merge) request for each copy's branch, or find the open one."`
	Draft  bool     `help:"Open the requests as drafts."`
	Base   string   `help:"Branch the requests target (default: the repo's default branch)."`
	Title  string   `help:"Request title (default: the branch's last commit's subject)."`
	Body   string   `help:"Request body (default: the branch's commits, one per line)."`
}

func (c *WSPublishCmd) Run(ctx *Context) error {
	cfg, err := ctx.LoadConfig()
	if err != nil {
		return err
	}
	spec, st, err := operationalSpec(ctx, c.Name)
	if err != nil {
		return err
	}
	if spec.Shared() {
		return fmt.Errorf("workspace %q is shared: its agents have no branches of their own to publish", c.Name)
	}
	var targets []workspace.Located
	for _, l := range spec.Copies() {
		if len(c.Agents) == 0 || slices.Contains(c.Agents, l.Agent) {
			targets = append(targets, l)
		}
	}
	if len(targets) == 0 {
		return fmt.Errorf("no copies of %v in workspace %q", c.Agents, c.Name)
	}

	type published struct {
		l  workspace.Located
		pr string
	}
	var done []published
	// What publishing did to one copy, as --json prints it.
	type publishJSON struct {
		Copy   string `json:"copy"`
		Path   string `json:"path"`
		Branch string `json:"branch,omitempty"`
		Pushed bool   `json:"pushed"`
		PR     string `json:"pr,omitempty"`
		Error  string `json:"error,omitempty"`
	}
	doc := []publishJSON{}
	failed := 0
	for _, l := range targets {
		label := l.Label(spec)
		branch, err := gitops.Git(ctx.GoCtx(), l.Path, "symbolic-ref", "--short", "HEAD")
		if err != nil {
			failed++
			fmt.Fprintf(os.Stderr, "  %s: not on a branch; nothing to publish\n", label)
			doc = append(doc, publishJSON{Copy: label, Path: l.Path, Error: "not on a branch"})
			continue
		}
		if rs, err := gitops.GetStatus(ctx.GoCtx(), l.Path); err == nil && (rs.HasStaged || rs.HasUnstaged || rs.HasUntracked) {
			fmt.Fprintf(os.Stderr, "  %s: has uncommitted changes, which aren't published\n", label)
		}
		if _, err := gitops.Push(ctx.GoCtx(), l.Path, l.SSHKey, true); err != nil {
			failed++
			fmt.Fprintf(os.Stderr, "  %s: push failed: %v\n", label, err)
			doc = append(doc, publishJSON{Copy: label, Path: l.Path, Branch: branch, Error: "push failed: " + err.Error()})
			continue
		}
		p := published{l: l}
		if !c.PR {
			fmt.Printf("  %s: pushed %s\n", label, branch)
			done = append(done, p)
			doc = append(doc, publishJSON{Copy: label, Path: l.Path, Branch: branch, Pushed: true})
			continue
		}
		url, err := c.openPR(ctx.GoCtx(), cfg, st, l, branch)
		if err != nil {
			failed++
			fmt.Fprintf(os.Stderr, "  %s: pushed %s, but %v\n", label, branch, err)
			done = append(done, p)
			doc = append(doc, publishJSON{Copy: label, Path: l.Path, Branch: branch, Pushed: true, Error: err.Error()})
			continue
		}
		p.pr = url
		fmt.Printf("  %s: pushed %s; %s\n", label, branch, url)
		done = append(done, p)
		doc = append(doc, publishJSON{Copy: label, Path: l.Path, Branch: branch, Pushed: true, PR: url})
	}

	err = st.Update(cfg.LockTimeout, func(st *state.State) error {
		for _, p := range done {
			if cs := st.CopyAt(c.Name, p.l.Agent, p.l.Path); cs != nil {
				cs.Published = time.Now()
				if p.pr != "" {
					cs.PR = p.pr
				}
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	logEvent(ctx, "ws_publish", c.Name, fmt.Sprintf("published=%d failed=%d", len(done), failed))
	return ctx.finish(doc, outcome(failed, len(targets), "copies could not be published"))
}

// prTimeout bounds one gh or glab command.
const prTimeout = 60 * time.Second

// openPR opens the request for a copy's branch with gh or glab, under the
// token of the provider the repo comes from, or returns the open one.
func (c *WSPublishCmd) openPR(ctx context.Context, cfg *config.Config, st *state.State, l workspace.Located, branch string) (string, error) {
	id := copyIdentity(st, state.CopyState{Repo: l.Repo, URL: l.URL})
	if id.Host == "" {
		if parsed, err := gitops.ParseRemoteURL(ctx, l.Path); err == nil {
			id = *parsed
		}
	}
	if id.Host == "" {
		return "", errors.New("its origin isn't a URL gitten can open a request against")
	}
	base := c.Base
	if base == "" {
		head, err := gitops.Git(ctx, l.Path, "symbolic-ref", "--short", "refs/remotes/origin/HEAD")
		if err != nil {
			return "", errors.New("the repo has no default branch on record (origin/HEAD); give --base")
		}
		base = strings.TrimPrefix(head, "origin/")
	}
	title, body := c.Title, c.Body
	if title == "" {
		title, _ = gitops.Git(ctx, l.Path, "log", "-1", "--format=%s")
	}
	if body == "" {
		body, _ = gitops.Git(ctx, l.Path, "log", "--format=- %s", "origin/"+base+".."+branch)
	}

	kind, env, err := forge(cfg, st, l.Repo, id)
	if err != nil {
		return "", err
	}
	repo := id.FullName
	ctx, cancel := context.WithTimeout(ctx, prTimeout)
	defer cancel()
	run := func(name string, args ...string) (string, error) {
		path, err := exec.LookPath(name)
		if err != nil {
			return "", fmt.Errorf("%s isn't installed", name)
		}
		cmd := exec.CommandContext(ctx, path, args...)
		cmd.Dir, cmd.Env = l.Path, env
		out, err := cmd.CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	switch kind {
	case "github":
		if id.Host != "github.com" {
			repo = id.Host + "/" + repo
		}
		args := []string{"pr", "create", "--repo", repo, "--head", branch, "--base", base, "--title", title, "--body", body}
		if c.Draft {
			args = append(args, "--draft")
		}
		out, err := run("gh", args...)
		if err == nil {
			return lastURL(out), nil
		}
		if strings.Contains(out, "already exists") {
			if url := lastURL(out); url != "" {
				return url, nil
			}
			return run("gh", "pr", "view", branch, "--repo", repo, "--json", "url", "--jq", ".url")
		}
		return "", fmt.Errorf("gh pr create: %v: %s", err, out)
	case "gitlab":
		args := []string{"mr", "create", "--repo", repo, "--source-branch", branch, "--target-branch", base, "--title", title, "--description", body, "--yes"}
		if c.Draft {
			args = append(args, "--draft")
		}
		out, err := run("glab", args...)
		if err == nil {
			return lastURL(out), nil
		}
		if strings.Contains(out, "already exists") {
			view, verr := run("glab", "mr", "view", branch, "--repo", repo)
			if url := lastURL(view); verr == nil && url != "" {
				return url, nil
			}
		}
		return "", fmt.Errorf("glab mr create: %v: %s", err, out)
	default:
		return fmt.Sprintf("https://%s/%s/compare/%s...%s", id.Host, id.FullName, base, branch), nil
	}
}

// forgeTokenVars are the variables gh and glab take a token from. When
// publishing gives them the repo's provider's token, the caller's are left
// out, so none of them can stand in for it.
var forgeTokenVars = []string{
	"GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN",
	"GITLAB_TOKEN", "GITLAB_ACCESS_TOKEN", "OAUTH_TOKEN",
}

// forge names the kind of host a repo is on -- github, gitlab, or "" -- and
// the environment that gives gh or glab the token of the provider that acts
// for it (see config.ProviderFor), so the request is opened by the right
// account. Without a provider for the host, gh's or glab's own login is used,
// unless several accounts cover the host: it may not be the repo's.
func forge(cfg *config.Config, st *state.State, key string, id repourl.Identity) (string, []string, error) {
	rule := ""
	if repo := st.Repos[key]; repo != nil {
		rule = repo.DiscoveredBy
	}
	provider, err := cfg.ProviderFor(key, rule)
	if err != nil {
		return "", nil, err
	}
	kind := ""
	switch {
	case provider != nil:
		kind = config.NormalizeProviderType(provider.Type)
	case id.Host == "github.com":
		kind = "github"
	case id.Host == "gitlab.com":
		kind = "gitlab"
	}
	if provider == nil && kind != "" && cfg.AccountsOn(id.Host) > 1 {
		return "", nil, fmt.Errorf("%s: %d accounts cover %s and no provider acts for this repo's, so %s's own login may be another account's; add a provider for it", key, cfg.AccountsOn(id.Host), id.Host, map[string]string{"github": "gh", "gitlab": "glab"}[kind])
	}
	token := ""
	if provider != nil {
		if token, err = discovery.ResolveToken(*provider); err != nil {
			return "", nil, fmt.Errorf("%s: the token of provider %s, which acts for it: %w", key, provider.Name, err)
		}
	}
	env := config.Environ()
	if token != "" {
		env = slices.DeleteFunc(env, func(kv string) bool {
			name, _, _ := strings.Cut(kv, "=")
			return slices.Contains(forgeTokenVars, name)
		})
	}
	switch kind {
	case "github":
		// gh reads GH_TOKEN for github.com and GHE.com, GH_ENTERPRISE_TOKEN
		// for GitHub Enterprise Server.
		if token != "" {
			name := "GH_ENTERPRISE_TOKEN"
			if id.Host == "github.com" || strings.HasSuffix(id.Host, ".ghe.com") {
				name = "GH_TOKEN"
			}
			env = append(env, name+"="+token)
		}
		if id.Host != "github.com" {
			env = append(env, "GH_HOST="+id.Host)
		}
	case "gitlab":
		if token != "" {
			env = append(env, "GITLAB_TOKEN="+token)
		}
		env = append(env, "GITLAB_HOST="+id.Host)
	}
	return kind, env, nil
}

// lastURL is the last http(s) URL in text.
func lastURL(text string) string {
	fields := strings.Fields(text)
	for i := len(fields) - 1; i >= 0; i-- {
		if strings.HasPrefix(fields[i], "https://") || strings.HasPrefix(fields[i], "http://") {
			return strings.TrimRight(fields[i], ".,)")
		}
	}
	return ""
}

// agentsStop ends the agent's run, if it is running.
func agentsStop(stateDir, workspace, agent string, timeout time.Duration) (bool, error) {
	ended, err := agents.Stop(agents.LeasePath(stateDir, workspace, agent), timeout, false)
	if err == nil && !ended {
		err = fmt.Errorf("%s/%s is still running after %s; gitten ws stop --kill ends it", workspace, agent, timeout)
	}
	return ended, err
}
