package cmd

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/ashtonian/gitstead/internal/config"
	"github.com/ashtonian/gitstead/internal/gitops"
	"github.com/ashtonian/gitstead/internal/repourl"
	"github.com/ashtonian/gitstead/internal/state"
	"github.com/ashtonian/gitstead/internal/workspace"
)

// offsiteSpec is how one copy goes offsite, and each of its checked-out
// submodules as a working copy of its own. A copy's ID completes it when its
// job runs.
type offsiteSpec struct {
	policy     config.RemoteSyncConfig
	machine    string
	id         repourl.Identity // fills in a remote URL template
	sshKey     string
	archiveDir string
	last       gitops.OffsiteRecord
	// lastSubs is what earlier pushes left of each submodule, by path.
	lastSubs map[string]gitops.OffsiteRecord
	dryRun   bool
	// patchDir is where a dry run, which backs nothing up, finds the latest
	// patch backups to plan from.
	patchDir string
}

// offsiteFor is the offsite spec for a copy under policy, or nil when there
// is nothing to push and nothing earlier runs left to clean up.
func offsiteFor(cfg *config.Config, policy config.RemoteSyncConfig, machine string, id repourl.Identity, sshKey string, last state.OffsiteStatus) *offsiteSpec {
	spec := &offsiteSpec{policy: policy, machine: machine, id: id, sshKey: sshKey, archiveDir: cfg.Backup.ArchiveDir,
		last: offsiteRecord(last), lastSubs: map[string]gitops.OffsiteRecord{}}
	leftovers := !spec.last.Empty()
	for path, sub := range last.OffsiteSubmodules {
		spec.lastSubs[path] = offsiteRecord(sub)
		leftovers = leftovers || !spec.lastSubs[path].Empty()
	}
	if policy.WIP == "off" && policy.Unpushed == "off" && !leftovers {
		return nil
	}
	return spec
}

// resolve fills in the namespace, branch and bundle of the copy with copyID,
// a copy of repo id.
func (s offsiteSpec) resolve(id repourl.Identity, copyID string) (gitops.Offsite, error) {
	ns, err := config.OffsiteNamespace(s.policy.Namespace, s.machine, copyID)
	if err != nil {
		return gitops.Offsite{}, err
	}
	remote, err := config.OffsiteRemote(s.policy.Remote, id)
	if err != nil {
		return gitops.Offsite{}, err
	}
	return gitops.Offsite{
		Remote: remote, Namespace: ns, WIP: s.policy.WIP, Unpushed: s.policy.Unpushed, Keep: s.policy.Keep,
		Branch: "refs/heads/gitstead/wip/" + s.machine + "/" + copyID,
		Bundle: filepath.Join(config.ExpandHome(s.archiveDir), "unpushed", copyID+".bundle"),
		SSHKey: s.sshKey,
	}, nil
}

// offsitePush is what taking one working copy offsite did: the record it
// left (nil when it didn't run) and its plan, or the plan alone on a dry run.
type offsitePush struct {
	rec  *gitops.OffsiteRecord
	plan gitops.OffsitePlan
	err  error
}

// run pushes the working copy at path offsite under off, or plans it on a
// dry run.
func (s offsiteSpec) run(ctx context.Context, path string, latest *gitops.Latest, off gitops.Offsite, last gitops.OffsiteRecord) offsitePush {
	if s.dryRun {
		plan, err := gitops.PlanOffsite(ctx, path, latest, off, last)
		return offsitePush{plan: plan, err: err}
	}
	rec, plan, err := gitops.PushOffsite(ctx, path, latest, off, last)
	return offsitePush{rec: &rec, plan: plan, err: err}
}

// pushOffsite pushes the copy offsite, or plans it on a dry run, and then
// each of its checked-out submodules, to its own remote. The WIP state comes
// from this run's backup; without one it is unknown and the WIP refs are left
// as they are, except on a dry run, which plans from the latest backup there
// is.
func (o *copyOutcome) pushOffsite(ctx context.Context, job copyJob) {
	spec := job.offsite
	var latest *gitops.Latest
	var dryBackup *gitops.BackupInfo
	copyID := ""
	switch {
	case o.backup != nil && o.backupErr == nil:
		latest, copyID = &gitops.Latest{Snapshot: o.backup.Snapshot, BackupID: o.backup.ID}, o.backup.CopyID
	case spec.dryRun:
		if b, err := gitops.FindBackup(ctx, job.path, spec.patchDir, "latest"); err == nil {
			latest, dryBackup = &gitops.Latest{Snapshot: b.Snapshot(), BackupID: b.ID}, &b
		}
	}
	if copyID == "" {
		var err error
		if copyID, err = gitops.CopyID(ctx, job.path); err != nil {
			o.offsiteErr = err
			return
		}
	}
	off, err := spec.resolve(spec.id, copyID)
	if err != nil {
		o.offsiteErr = err
		return
	}
	top := spec.run(ctx, job.path, latest, off, spec.last)
	o.offsite, o.offsitePlan, o.offsiteErr = top.rec, top.plan, top.err
	o.pushSubmodulesOffsite(ctx, job, latest != nil, dryBackup)
}

// pushSubmodulesOffsite takes each checked-out submodule of the copy offsite
// as a working copy of its own: its uncommitted work, from the backup of it
// that went with the copy's, and its unpushed commits, to its own remote --
// remote_sync.remote named in it, or the template filled in with its origin
// -- under its own copy's namespace. known says whether the copy's WIP state
// is known (see pushOffsite); dryBackup is the backup a dry run plans from.
func (o *copyOutcome) pushSubmodulesOffsite(ctx context.Context, job copyJob, known bool, dryBackup *gitops.BackupInfo) {
	spec := job.offsite
	paths, err := gitops.Submodules(ctx, job.path)
	if err != nil {
		o.offsiteErr = errors.Join(o.offsiteErr, fmt.Errorf("listing submodules: %w", err))
		return
	}
	o.offsiteSubs = map[string]offsitePush{}
	for _, path := range paths {
		subPath := filepath.Join(job.path, path)
		var latest *gitops.Latest
		copyID := ""
		switch {
		case dryBackup != nil:
			sub, ok, err := dryBackup.Submodule(ctx, job.path, path)
			switch {
			case err != nil:
			case ok:
				latest = &gitops.Latest{Snapshot: sub.Snapshot(), BackupID: sub.ID}
			default:
				latest = &gitops.Latest{}
			}
		case known:
			latest = &gitops.Latest{}
			if r := o.backup.Submodules[path]; r != nil {
				latest, copyID = &gitops.Latest{Snapshot: r.Snapshot, BackupID: r.ID}, r.CopyID
			}
		}
		push := o.pushSubmoduleOffsite(ctx, spec, subPath, copyID, latest, spec.lastSubs[path])
		if push.err != nil {
			o.offsiteErr = errors.Join(o.offsiteErr, fmt.Errorf("submodule %s: %w", path, push.err))
		}
		o.offsiteSubs[path] = push
	}
}

func (o *copyOutcome) pushSubmoduleOffsite(ctx context.Context, spec *offsiteSpec, subPath, copyID string, latest *gitops.Latest, last gitops.OffsiteRecord) offsitePush {
	if copyID == "" {
		var err error
		if copyID, err = gitops.CopyID(ctx, subPath); err != nil {
			return offsitePush{err: err}
		}
	}
	var id repourl.Identity
	if parsed, err := gitops.ParseRemoteURL(ctx, subPath); err == nil {
		id = *parsed
	} else if config.IsRemoteURL(spec.policy.Remote) {
		return offsitePush{err: fmt.Errorf("remote_sync.remote is a URL template, which its origin can't fill in: %w", err)}
	}
	off, err := spec.resolve(id, copyID)
	if err != nil {
		return offsitePush{err: err}
	}
	return spec.run(ctx, subPath, latest, off, last)
}

// recordOffsite stores what the offsite push left: the copy's record, and
// each submodule's. A submodule that couldn't be pushed keeps its record; one
// gone from the copy is dropped, with what was pushed of it left where it is.
func (o copyOutcome) recordOffsite(s *state.OffsiteStatus) {
	if o.offsite == nil {
		return
	}
	setOffsite(s, *o.offsite, o.offsiteErr, o.at)
	if o.offsiteSubs == nil {
		return
	}
	subs := map[string]state.OffsiteStatus{}
	for path, push := range o.offsiteSubs {
		sub := s.OffsiteSubmodules[path]
		if push.rec != nil {
			setOffsite(&sub, *push.rec, push.err, o.at)
		} else {
			sub.LastOffsiteError = errString(push.err)
		}
		if !offsiteRecord(sub).Empty() || sub.LastOffsiteError != "" {
			subs[path] = sub
		}
	}
	s.OffsiteSubmodules = nil
	if len(subs) > 0 {
		s.OffsiteSubmodules = subs
	}
}

func setOffsite(s *state.OffsiteStatus, r gitops.OffsiteRecord, err error, at time.Time) {
	s.OffsiteRemote, s.OffsiteNamespace = r.Remote, r.Namespace
	s.OffsiteWIP, s.OffsiteBranch, s.OffsiteHeads, s.OffsiteBundled = r.WIP, r.Branch, r.Heads, r.Bundled
	s.LastOffsite, s.LastOffsiteError = at, errString(err)
}

func offsiteRecord(s state.OffsiteStatus) gitops.OffsiteRecord {
	return gitops.OffsiteRecord{
		Remote: s.OffsiteRemote, Namespace: s.OffsiteNamespace,
		WIP: s.OffsiteWIP, Branch: s.OffsiteBranch, Heads: s.OffsiteHeads, Bundled: s.OffsiteBundled,
	}
}

// offsitePlanned reports whether the copy's push, or that of one of its
// submodules, changed anything or would.
func (o copyOutcome) offsitePlanned() bool {
	if !o.offsitePlan.Empty() {
		return true
	}
	for _, sub := range o.offsiteSubs {
		if !sub.plan.Empty() {
			return true
		}
	}
	return false
}

// offsiteWanted reports whether remote_sync is enabled anywhere: at the top,
// for an account, by an override or for a workspace.
func offsiteWanted(cfg *config.Config) bool {
	if cfg.RemoteSync.Enabled {
		return true
	}
	for _, a := range cfg.Accounts {
		if a.RemoteSync != nil && a.RemoteSync.Enabled != nil && *a.RemoteSync.Enabled {
			return true
		}
	}
	for _, o := range cfg.Overrides {
		if o.RemoteSync != nil && o.RemoteSync.Enabled != nil && *o.RemoteSync.Enabled {
			return true
		}
	}
	for _, w := range cfg.Workspaces {
		if w.RemoteSync != nil && w.RemoteSync.Enabled != nil && *w.RemoteSync.Enabled {
			return true
		}
	}
	return false
}

// ensureMachine returns this machine's name in offsite refs, choosing it the
// first time: the host name, plus a random suffix so two machines that share
// a host name never share refs.
func ensureMachine(cfg *config.Config, st *state.State) (string, error) {
	if st.Machine != "" {
		return st.Machine, nil
	}
	host, _ := os.Hostname()
	suffix := make([]byte, 3)
	if _, err := rand.Read(suffix); err != nil {
		return "", err
	}
	name := machineSlug(host) + "-" + hex.EncodeToString(suffix)
	err := st.Update(cfg.LockTimeout, func(st *state.State) error {
		if st.Machine == "" {
			st.Machine = name
		}
		return nil
	})
	return st.Machine, err
}

var notSlug = regexp.MustCompile(`[^a-z0-9]+`)

// machineSlug turns a host name into a ref-safe word: "Ash's MacBook-Pro.local"
// becomes "ash-s-macbook-pro".
func machineSlug(host string) string {
	host, _, _ = strings.Cut(strings.ToLower(host), ".")
	slug := strings.Trim(notSlug.ReplaceAllString(host, "-"), "-")
	if len(slug) > 24 {
		slug = strings.TrimRight(slug[:24], "-")
	}
	if slug == "" {
		return "machine"
	}
	return slug
}

// identityOf is a tracked repo's identity, for a remote URL template.
func identityOf(r *state.RepoState) repourl.Identity {
	if r == nil {
		return repourl.Identity{}
	}
	return repourl.Identity{Host: r.Host, Owner: r.Owner, Name: r.Name, FullName: r.FullName}
}

// addCopyJobs appends build's job for each workspace copy the selection
// covers -- copies of selected repos, or every copy when nothing is selected
// -- skipping a working copy jobs already has: a copy adopted from a tracked
// repo's directory is that repo, and two jobs on one copy at once would race
// on its refs. build returns nil to leave a copy out.
func addCopyJobs(jobs []copyJob, st *state.State, sel selection, pass func(string, *state.RepoState) bool, build func(ws *state.WorkspaceState, agent string, c state.CopyState) *copyJob) []copyJob {
	queued := make(map[string]bool, len(jobs))
	for _, j := range jobs {
		queued[j.path] = true
	}
	all := sel.empty()
	for _, name := range slices.Sorted(maps.Keys(st.Workspaces)) {
		ws := st.Workspaces[name]
		for _, r := range ws.Replicas {
			for _, c := range r.Copies {
				if repo := st.Repos[c.Repo]; !all && (repo == nil || !pass(c.Repo, repo)) {
					continue
				}
				if queued[c.Path] || !gitops.HasGitDir(c.Path) {
					continue
				}
				if job := build(ws, r.Agent, c); job != nil {
					queued[c.Path] = true
					jobs = append(jobs, *job)
				}
			}
		}
	}
	return jobs
}

// copyRemoteSync is remote_sync for a workspace copy: its repo's, then its
// workspace's.
func copyRemoteSync(cfg *config.Config, st *state.State, workspace string, c state.CopyState) config.RemoteSyncConfig {
	rule := ""
	if repo := st.Repos[c.Repo]; repo != nil {
		rule = repo.DiscoveredBy
	}
	return cfg.RemoteSyncInWorkspace(c.Repo, rule, workspace)
}

// copyJobFor is the job for a workspace copy, under the policy of the repo
// it is a copy of, so overrides and accounts apply to copies as to repos.
func copyJobFor(cfg *config.Config, st *state.State, ws *state.WorkspaceState, agent string, c state.CopyState, backup bool) copyJob {
	rule := ""
	if repo := st.Repos[c.Repo]; repo != nil {
		rule = repo.DiscoveredBy
	}
	copyCfg := cfg.ResolveForRepo(c.Repo)
	job := copyJob{
		label: workspace.LabelOf(ws, agent, c.Path), workspace: ws.Name, agent: agent, path: c.Path,
		backupTimeout: copyCfg.Sync.BackupTimeout, sync: copyCfg.Sync,
		sshKey: cfg.ResolveSSHKey(c.Repo, rule),
	}
	if backup && copyCfg.Backup.Enabled {
		b := backupConfigFor(copyCfg)
		job.backup = &b
	}
	return job
}

// copyOffsite is a workspace copy's offsite spec, enabled or not.
func copyOffsite(cfg *config.Config, st *state.State, machine, workspace string, c state.CopyState, sshKey string) *offsiteSpec {
	return offsiteFor(cfg, copyRemoteSync(cfg, st, workspace, c), machine, copyIdentity(st, c), sshKey, c.OffsiteStatus)
}

// copyIdentity is the identity of the repo a copy is of.
func copyIdentity(st *state.State, c state.CopyState) repourl.Identity {
	if repo := st.Repos[c.Repo]; repo != nil {
		return identityOf(repo)
	}
	if id, err := repourl.Parse(c.URL); err == nil {
		return *id
	}
	return repourl.Identity{}
}

func (c *RemoteSyncCmd) Run(ctx *Context) error {
	cfg, err := ctx.LoadConfig()
	if err != nil {
		return err
	}
	st, err := ctx.LoadState()
	if err != nil {
		return err
	}
	sel := selection{c.Repos, c.Tag, c.Set}
	pass, err := sel.predicate(cfg)
	if err != nil {
		return err
	}
	if c.List {
		return listOffsite(ctx, cfg, st, pass)
	}
	machine, err := ensureMachine(cfg, st)
	if err != nil {
		return err
	}

	// On demand, remote_sync.enabled doesn't matter: it only decides whether
	// sync pushes on its own.
	var jobs []copyJob
	for _, key := range slices.Sorted(maps.Keys(st.Repos)) {
		repo := st.Repos[key]
		if !pass(key, repo) || !repo.Cloned {
			continue
		}
		repoCfg := cfg.ResolveForRepo(key)
		var backup *gitops.BackupConfig
		if repoCfg.Backup.Enabled {
			b := backupConfigFor(repoCfg)
			backup = &b
		}
		job := repoJob(cfg, key, repo, backup)
		job.offsite = offsiteFor(cfg, cfg.RemoteSyncFor(key, repo.DiscoveredBy), machine, identityOf(repo), job.sshKey, repo.OffsiteStatus)
		jobs = append(jobs, job)
	}
	jobs = addCopyJobs(jobs, st, sel, pass, func(ws *state.WorkspaceState, agent string, c state.CopyState) *copyJob {
		job := copyJobFor(cfg, st, ws, agent, c, true)
		job.offsite = copyOffsite(cfg, st, machine, ws.Name, c, job.sshKey)
		return &job
	})
	jobs = slices.DeleteFunc(jobs, func(j copyJob) bool { return j.offsite == nil })
	if c.DryRun {
		// A dry run writes no backup; it plans from the latest one.
		for i := range jobs {
			jobs[i].offsite.dryRun = true
			if jobs[i].backup != nil {
				jobs[i].offsite.patchDir = jobs[i].backup.PatchDir
			}
			jobs[i].backup = nil
		}
	}

	outcomes := runCopies(ctx.GoCtx(), ctx.Logger(), jobs, cfg.Concurrency)
	if !c.DryRun {
		if err := st.Update(cfg.LockTimeout, func(st *state.State) error {
			recordCopies(st, outcomes)
			return nil
		}); err != nil {
			return fmt.Errorf("recording what went offsite: %w", err)
		}
	}

	pushed, failed := 0, 0
	for _, o := range outcomes {
		if errs := o.failed(); len(errs) > 0 {
			failed++
			fmt.Fprintf(os.Stderr, "  %s: %v\n", o.job.label, errors.Join(errs...))
			continue
		}
		if !o.offsitePlanned() {
			continue
		}
		pushed++
		if !ctx.Quiet || c.DryRun {
			if !o.offsitePlan.Empty() {
				fmt.Printf("  %s: %s\n", o.job.label, describeOffsite(o.offsitePlan, c.DryRun))
			}
			for _, path := range slices.Sorted(maps.Keys(o.offsiteSubs)) {
				if plan := o.offsiteSubs[path].plan; !plan.Empty() {
					fmt.Printf("  %s, submodule %s: %s\n", o.job.label, path, describeOffsite(plan, c.DryRun))
				}
			}
		}
	}
	if !ctx.Quiet {
		verb := "Updated"
		if c.DryRun {
			verb = "Would update"
		}
		fmt.Printf("%s offsite copies of %d of %d working copies\n", verb, pushed, len(outcomes))
	}
	return ctx.finish(copiesJSON(outcomes), outcome(failed, len(outcomes), "working copies failed to go offsite"))
}

func describeOffsite(p gitops.OffsitePlan, dryRun bool) string {
	var wrote, deleted []string
	for _, r := range p.Push {
		src, dst, _ := strings.Cut(r, ":")
		if src == "" {
			deleted = append(deleted, dst)
		} else {
			wrote = append(wrote, dst)
		}
	}
	var parts []string
	if len(wrote) > 0 {
		parts = append(parts, "push "+strings.Join(wrote, ", "))
	}
	if len(deleted) > 0 {
		parts = append(parts, "delete "+strings.Join(deleted, ", "))
	}
	switch {
	case p.Bundle == nil:
	case len(p.Bundle) == 0:
		parts = append(parts, "remove the unpushed-commits bundle")
	default:
		parts = append(parts, fmt.Sprintf("bundle %s", strings.Join(slices.Sorted(maps.Keys(p.Bundle)), ", ")))
	}
	s := strings.Join(parts, "; ")
	if !dryRun {
		s = strings.NewReplacer("push ", "pushed ", "delete ", "deleted ", "remove ", "removed ", "bundle ", "bundled ").Replace(s)
	}
	return s
}

// listOffsite shows, for each selected repo, what every machine and copy left
// on its backup remote, and how to get it back.
func listOffsite(ctx *Context, cfg *config.Config, st *state.State, pass func(string, *state.RepoState) bool) error {
	failed, listed := 0, 0
	doc := map[string]remoteRefs{}
	for _, key := range slices.Sorted(maps.Keys(st.Repos)) {
		repo := st.Repos[key]
		if !pass(key, repo) || !repo.Cloned {
			continue
		}
		policy := cfg.RemoteSyncFor(key, repo.DiscoveredBy)
		lctx, cancel := context.WithTimeout(ctx.GoCtx(), cfg.ResolveForRepo(key).Sync.TimeoutPerRepo)
		refs, err := offsiteRefsOf(lctx, repo.LocalPath, policy.Remote, identityOf(repo), cfg.ResolveSSHKey(key, repo.DiscoveredBy))
		cancel()
		if err != nil {
			failed++
			fmt.Fprintf(os.Stderr, "  %s: %v\n", key, err)
			continue
		}
		listed++
		if len(refs.Refs) == 0 && len(refs.Submodules) == 0 {
			continue
		}
		doc[key] = refs
		printOffsiteRefs(key, refs)
	}
	if listed > 0 {
		fmt.Println(`
Restore from inside a clone of the repo (a submodule's, from inside it):
  uncommitted work:   git fetch <remote> <.../wip/<id>> && git stash apply --index FETCH_HEAD
  a visible WIP ref:  git fetch <remote> <refs/heads/gitstead/wip/...> && git cherry-pick --no-commit FETCH_HEAD
  unpushed commits:   git fetch <remote> <.../heads/<branch>>:refs/heads/<branch>-recovered`)
	}
	return ctx.finish(doc, outcome(failed, failed+listed, "repos' remotes could not be listed"))
}

// remoteRefs are the offsite refs on a working copy's backup remote, and on
// each of its submodules'.
type remoteRefs struct {
	Remote     string                `json:"remote"`
	Refs       []string              `json:"refs,omitempty"`
	Submodules map[string]remoteRefs `json:"submodules,omitempty"`
}

// offsiteRefsOf lists the offsite refs on the backup remote of the working
// copy at path, a copy of repo id -- remoteTmpl names it, or is its URL
// template -- and on each of its checked-out submodules' remotes.
func offsiteRefsOf(ctx context.Context, path, remoteTmpl string, id repourl.Identity, sshKey string) (remoteRefs, error) {
	refs, err := offsiteRefsAt(ctx, path, remoteTmpl, id, sshKey)
	if err != nil {
		return refs, err
	}
	subs, err := gitops.Submodules(ctx, path)
	if err != nil {
		return refs, fmt.Errorf("listing submodules: %w", err)
	}
	for _, sub := range subs {
		subPath := filepath.Join(path, sub)
		var subID repourl.Identity
		if parsed, err := gitops.ParseRemoteURL(ctx, subPath); err == nil {
			subID = *parsed
		} else if config.IsRemoteURL(remoteTmpl) {
			return refs, fmt.Errorf("submodule %s: remote_sync.remote is a URL template, which its origin can't fill in: %w", sub, err)
		}
		subRefs, err := offsiteRefsAt(ctx, subPath, remoteTmpl, subID, sshKey)
		if err != nil {
			return refs, fmt.Errorf("submodule %s: %w", sub, err)
		}
		if len(subRefs.Refs) > 0 {
			if refs.Submodules == nil {
				refs.Submodules = map[string]remoteRefs{}
			}
			refs.Submodules[sub] = subRefs
		}
	}
	return refs, nil
}

// offsiteRefsAt lists the offsite refs on the backup remote of the working
// copy at path alone.
func offsiteRefsAt(ctx context.Context, path, remoteTmpl string, id repourl.Identity, sshKey string) (remoteRefs, error) {
	remote, err := config.OffsiteRemote(remoteTmpl, id)
	if err != nil {
		return remoteRefs{}, err
	}
	out, err := gitops.GitWithSSHKey(ctx, path, sshKey, "ls-remote", remote, "refs/gitstead/*", "refs/heads/gitstead/wip/*")
	if err != nil {
		return remoteRefs{}, err
	}
	refs := remoteRefs{Remote: remote}
	for _, line := range strings.Split(out, "\n") {
		if _, ref, ok := strings.Cut(line, "\t"); ok {
			refs.Refs = append(refs.Refs, ref)
		}
	}
	return refs, nil
}

func printOffsiteRefs(key string, refs remoteRefs) {
	if len(refs.Refs) > 0 {
		fmt.Printf("%s (%s):\n", key, refs.Remote)
		for _, ref := range refs.Refs {
			fmt.Printf("  %s\n", ref)
		}
	}
	for _, sub := range slices.Sorted(maps.Keys(refs.Submodules)) {
		fmt.Printf("%s, submodule %s (%s):\n", key, sub, refs.Submodules[sub].Remote)
		for _, ref := range refs.Submodules[sub].Refs {
			fmt.Printf("  %s\n", ref)
		}
	}
}
