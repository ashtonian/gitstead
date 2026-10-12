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

	"github.com/ashtonian/git-mgr/internal/config"
	"github.com/ashtonian/git-mgr/internal/gitops"
	"github.com/ashtonian/git-mgr/internal/repourl"
	"github.com/ashtonian/git-mgr/internal/state"
)

// offsiteSpec is how one copy goes offsite. The copy's ID completes it when
// its job runs.
type offsiteSpec struct {
	policy     config.RemoteSyncConfig
	machine    string
	id         repourl.Identity // fills in a remote URL template
	sshKey     string
	archiveDir string
	last       gitops.OffsiteRecord
	dryRun     bool
}

// offsiteFor is the offsite spec for a copy under policy, or nil when there
// is nothing to push and nothing earlier runs left to clean up.
func offsiteFor(cfg *config.Config, policy config.RemoteSyncConfig, machine string, id repourl.Identity, sshKey string, last state.OffsiteStatus) *offsiteSpec {
	rec := offsiteRecord(last)
	if policy.WIP == "off" && policy.Unpushed == "off" && len(rec.WIP) == 0 && rec.Branch == "" && len(rec.Heads) == 0 && len(rec.Bundled) == 0 {
		return nil
	}
	return &offsiteSpec{policy: policy, machine: machine, id: id, sshKey: sshKey, archiveDir: cfg.Backup.ArchiveDir, last: rec}
}

// resolve fills in the copy's own namespace, branch and bundle.
func (s offsiteSpec) resolve(copyID string) (gitops.Offsite, error) {
	ns, err := config.OffsiteNamespace(s.policy.Namespace, s.machine, copyID)
	if err != nil {
		return gitops.Offsite{}, err
	}
	remote, err := config.OffsiteRemote(s.policy.Remote, s.id)
	if err != nil {
		return gitops.Offsite{}, err
	}
	return gitops.Offsite{
		Remote: remote, Namespace: ns, WIP: s.policy.WIP, Unpushed: s.policy.Unpushed, Keep: s.policy.Keep,
		Branch: "refs/heads/git-mgr/wip/" + s.machine + "/" + copyID,
		Bundle: filepath.Join(config.ExpandHome(s.archiveDir), "unpushed", copyID+".bundle"),
		SSHKey: s.sshKey,
	}, nil
}

// pushOffsite pushes the copy offsite, or plans it on a dry run. The WIP
// state comes from this run's backup; without one it is unknown and the WIP
// refs are left as they are, except on a dry run, which plans from the
// latest backup there is.
func (o *copyOutcome) pushOffsite(ctx context.Context, job copyJob) {
	spec := job.offsite
	var latest *gitops.Latest
	copyID := ""
	switch {
	case o.backup != nil && o.backupErr == nil:
		latest, copyID = &gitops.Latest{Snapshot: o.backup.Snapshot, BackupID: o.backup.ID}, o.backup.CopyID
	case spec.dryRun && job.backup != nil:
		if b, err := gitops.FindBackup(ctx, job.path, job.backup.PatchDir, "latest"); err == nil {
			latest = &gitops.Latest{Snapshot: b.Commit, BackupID: b.ID}
		}
	}
	if copyID == "" {
		var err error
		if copyID, err = gitops.CopyID(ctx, job.path); err != nil {
			o.offsiteErr = err
			return
		}
	}
	off, err := spec.resolve(copyID)
	if err != nil {
		o.offsiteErr = err
		return
	}
	if spec.dryRun {
		o.offsitePlan, o.offsiteErr = gitops.PlanOffsite(ctx, job.path, latest, off, spec.last)
		return
	}
	rec, plan, err := gitops.PushOffsite(ctx, job.path, latest, off, spec.last)
	o.offsite, o.offsitePlan, o.offsiteErr = &rec, plan, err
}

// recordOffsite stores what the offsite push left.
func (o copyOutcome) recordOffsite(s *state.OffsiteStatus) {
	if o.offsite == nil {
		return
	}
	r := o.offsite
	s.OffsiteRemote, s.OffsiteNamespace = r.Remote, r.Namespace
	s.OffsiteWIP, s.OffsiteBranch, s.OffsiteHeads, s.OffsiteBundled = r.WIP, r.Branch, r.Heads, r.Bundled
	s.LastOffsite, s.LastOffsiteError = o.at, ""
	if o.offsiteErr != nil {
		s.LastOffsiteError = o.offsiteErr.Error()
	}
}

func offsiteRecord(s state.OffsiteStatus) gitops.OffsiteRecord {
	return gitops.OffsiteRecord{
		Remote: s.OffsiteRemote, Namespace: s.OffsiteNamespace,
		WIP: s.OffsiteWIP, Branch: s.OffsiteBranch, Heads: s.OffsiteHeads, Bundled: s.OffsiteBundled,
	}
}

// offsiteWanted reports whether remote_sync is enabled anywhere: at the top,
// for an account or by an override.
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

// addMemberJobs appends build's job for each workgroup member the selection
// covers -- members of selected repos, or every member when nothing is
// selected -- skipping a working copy jobs already has: a member adopted
// from a tracked repo's directory is that repo, and two jobs on one copy at
// once would race on its refs. build returns nil to leave a member out.
func addMemberJobs(jobs []copyJob, st *state.State, sel selection, pass func(string, *state.RepoState) bool, build func(wgName string, m state.WorkgroupMemberState) *copyJob) []copyJob {
	queued := make(map[string]bool, len(jobs))
	for _, j := range jobs {
		queued[j.path] = true
	}
	all := sel.empty()
	for _, wgName := range slices.Sorted(maps.Keys(st.Workgroups)) {
		for _, m := range st.Workgroups[wgName].Members {
			if repo := st.Repos[m.RepoKey]; !all && (repo == nil || !pass(m.RepoKey, repo)) {
				continue
			}
			if queued[m.Path] || !gitops.HasGitDir(m.Path) {
				continue
			}
			if job := build(wgName, m); job != nil {
				queued[m.Path] = true
				jobs = append(jobs, *job)
			}
		}
	}
	return jobs
}

// memberRemoteSync is remote_sync for a member: its repo's.
func memberRemoteSync(cfg *config.Config, st *state.State, m state.WorkgroupMemberState) config.RemoteSyncConfig {
	rule := ""
	if repo := st.Repos[m.RepoKey]; repo != nil {
		rule = repo.DiscoveredBy
	}
	return cfg.RemoteSyncFor(m.RepoKey, rule)
}

// memberJob is the job for a workgroup member, under the policy of the repo
// it is a copy of, so overrides and accounts apply to members as to repos.
func memberJob(cfg *config.Config, st *state.State, wgName string, m state.WorkgroupMemberState, backup bool) copyJob {
	repo := st.Repos[m.RepoKey]
	rule := ""
	if repo != nil {
		rule = repo.DiscoveredBy
	}
	memberCfg := cfg.ResolveForRepo(m.RepoKey)
	job := copyJob{
		label: wgName + "/" + m.Name, workgroup: wgName, member: m.Name, path: m.Path,
		backupTimeout: memberCfg.Sync.BackupTimeout, sync: memberCfg.Sync,
		sshKey: cfg.ResolveSSHKey(m.RepoKey, rule),
	}
	if backup && memberCfg.Backup.Enabled {
		b := backupConfigFor(memberCfg)
		job.backup = &b
	}
	return job
}

// memberIdentity is the identity of the repo a member is a copy of.
func memberIdentity(st *state.State, m state.WorkgroupMemberState) repourl.Identity {
	if repo := st.Repos[m.RepoKey]; repo != nil {
		return identityOf(repo)
	}
	if id, err := repourl.Parse(m.RepoURL); err == nil {
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
	jobs = addMemberJobs(jobs, st, sel, pass, func(wgName string, m state.WorkgroupMemberState) *copyJob {
		job := memberJob(cfg, st, wgName, m, true)
		job.offsite = memberOffsite(cfg, st, machine, m, job.sshKey)
		return &job
	})
	jobs = slices.DeleteFunc(jobs, func(j copyJob) bool { return j.offsite == nil })
	if c.DryRun {
		for i := range jobs {
			jobs[i].offsite.dryRun = true
			jobs[i].backup = nil // a dry run writes no backup; it plans from the latest one
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
		if o.offsitePlan.Empty() {
			continue
		}
		pushed++
		if !ctx.Quiet || c.DryRun {
			fmt.Printf("  %s: %s\n", o.job.label, describeOffsite(o.offsitePlan, c.DryRun))
		}
	}
	if !ctx.Quiet {
		verb := "Updated"
		if c.DryRun {
			verb = "Would update"
		}
		fmt.Printf("%s offsite copies of %d of %d working copies\n", verb, pushed, len(outcomes))
	}
	return outcome(failed, len(outcomes), "working copies failed to go offsite")
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
	for _, key := range slices.Sorted(maps.Keys(st.Repos)) {
		repo := st.Repos[key]
		if !pass(key, repo) || !repo.Cloned {
			continue
		}
		policy := cfg.RemoteSyncFor(key, repo.DiscoveredBy)
		remote, err := config.OffsiteRemote(policy.Remote, identityOf(repo))
		if err != nil {
			return err
		}
		lctx, cancel := context.WithTimeout(ctx.GoCtx(), cfg.ResolveForRepo(key).Sync.TimeoutPerRepo)
		out, err := gitops.GitWithSSHKey(lctx, repo.LocalPath, cfg.ResolveSSHKey(key, repo.DiscoveredBy),
			"ls-remote", remote, "refs/git-mgr/*", "refs/heads/git-mgr/wip/*")
		cancel()
		if err != nil {
			failed++
			fmt.Fprintf(os.Stderr, "  %s: %v\n", key, err)
			continue
		}
		listed++
		var refs []string
		for _, line := range strings.Split(out, "\n") {
			if _, ref, ok := strings.Cut(line, "\t"); ok {
				refs = append(refs, ref)
			}
		}
		if len(refs) == 0 {
			continue
		}
		fmt.Printf("%s (%s):\n", key, remote)
		for _, ref := range refs {
			fmt.Printf("  %s\n", ref)
		}
	}
	if listed > 0 {
		fmt.Println(`
Restore from inside a clone of the repo:
  uncommitted work:   git fetch <remote> <.../wip/<id>> && git stash apply --index FETCH_HEAD
  a visible WIP ref:  git fetch <remote> <refs/heads/git-mgr/wip/...> && git cherry-pick --no-commit FETCH_HEAD
  unpushed commits:   git fetch <remote> <.../heads/<branch>>:refs/heads/<branch>-recovered`)
	}
	return outcome(failed, failed+listed, "repos' remotes could not be listed")
}
