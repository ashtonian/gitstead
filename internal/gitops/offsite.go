package gitops

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Offsite is what of one working copy goes to a remote, and where. Every ref
// it writes is the copy's own: under Namespace, or the copy's visible WIP
// Branch, so a force-push never touches anyone else's refs.
type Offsite struct {
	Remote    string        // a remote name or URL
	Namespace string        // hidden ref prefix the copy owns, e.g. refs/git-mgr/<machine>/<copy>
	WIP       string        // "ref", "branch" or "off"
	Branch    string        // the copy's visible WIP branch, for WIP "branch"
	Unpushed  string        // "push", "bundle" or "off"
	Bundle    string        // the bundle file, for Unpushed "bundle"
	Keep      time.Duration // pushed WIP refs older than this are deleted; the current one stays
	SSHKey    string
	Now       func() time.Time
}

// OffsiteRecord is what pushes left offsite; the next push starts from it.
type OffsiteRecord struct {
	Remote    string
	Namespace string
	WIP       []string          // backup IDs at <Namespace>/wip/<id>
	Branch    string            // the commit at the visible WIP branch
	Heads     map[string]string // branch -> commit at <Namespace>/heads/<branch>
	Bundled   map[string]string // branch -> commit in the bundle
}

// Empty reports whether the record holds nothing pushed or bundled: nothing
// a later push would have to clean up.
func (r OffsiteRecord) Empty() bool {
	return len(r.WIP) == 0 && r.Branch == "" && len(r.Heads) == 0 && len(r.Bundled) == 0
}

// OffsitePlan is what one push does.
type OffsitePlan struct {
	// Push are refspecs: "+<commit>:<ref>" writes a ref, ":<ref>" deletes one.
	Push []string
	// Bundle is the branches the bundle must hold; nil leaves it as it is,
	// empty removes it.
	Bundle map[string]string
	Next   OffsiteRecord
}

// Empty reports whether the plan changes nothing.
func (p OffsitePlan) Empty() bool { return len(p.Push) == 0 && p.Bundle == nil }

// Latest is a copy's uncommitted state as its latest backup holds it.
type Latest struct {
	Snapshot string // the backup's commit; "" when the copy is clean
	BackupID string
}

// detachedHead names commits only HEAD reaches, in a copy on a detached HEAD.
const detachedHead = "HEAD"

// PlanOffsite works out what to push for the copy at repoPath, given what the
// last push left. latest is the copy's state from this run's backup; nil when
// no backup ran or it failed, which leaves the WIP refs as they are.
func PlanOffsite(ctx context.Context, repoPath string, latest *Latest, o Offsite, last OffsiteRecord) (OffsitePlan, error) {
	if last.Remote != o.Remote || last.Namespace != o.Namespace {
		// A new destination: what was pushed to the old one stays there. The
		// bundle is local, so it carries over.
		last = OffsiteRecord{Bundled: last.Bundled}
	}
	plan := OffsitePlan{Next: OffsiteRecord{Remote: o.Remote, Namespace: o.Namespace}}
	if latest == nil {
		plan.Next.WIP, plan.Next.Branch = last.WIP, last.Branch
	} else if err := o.planWIP(ctx, repoPath, *latest, last, &plan); err != nil {
		return plan, err
	}
	return plan, o.planUnpushed(ctx, repoPath, last, &plan)
}

// planWIP plans the copy's uncommitted work: hidden refs for the current
// backup and earlier ones younger than Keep, or the visible WIP branch.
func (o Offsite) planWIP(ctx context.Context, repoPath string, latest Latest, last OffsiteRecord, plan *OffsitePlan) error {
	now := time.Now
	if o.Now != nil {
		now = o.Now
	}
	current := ""
	if o.WIP == "ref" && latest.Snapshot != "" {
		current = latest.BackupID
	}
	for _, id := range last.WIP {
		at, err := time.Parse(backupIDLayout, id)
		if id == current || o.WIP == "ref" && err == nil && now().Sub(at) <= o.Keep {
			plan.Next.WIP = append(plan.Next.WIP, id)
			continue
		}
		plan.Push = append(plan.Push, ":"+o.wipRef(id))
	}
	if current != "" && !slices.Contains(last.WIP, current) {
		plan.Push = append(plan.Push, "+"+latest.Snapshot+":"+o.wipRef(current))
		plan.Next.WIP = append(plan.Next.WIP, current)
	}

	switch {
	case o.WIP == "branch" && latest.Snapshot != "":
		snap, err := readSnapshot(ctx, repoPath, latest.Snapshot)
		if err != nil {
			return err
		}
		wip, err := wipCommit(ctx, repoPath, snap)
		if err != nil {
			return err
		}
		plan.Next.Branch = wip
		if wip != last.Branch {
			plan.Push = append(plan.Push, "+"+wip+":"+o.Branch)
		}
	case last.Branch != "":
		plan.Push = append(plan.Push, ":"+o.Branch)
	}
	return nil
}

// planUnpushed plans the commits no remote has: saved under the namespace,
// or in the bundle, and dropped from wherever they were once a remote has
// them.
func (o Offsite) planUnpushed(ctx context.Context, repoPath string, last OffsiteRecord, plan *OffsitePlan) error {
	pushed, bundled := map[string]string{}, map[string]string{}
	if o.Unpushed == "push" || o.Unpushed == "bundle" {
		heads, err := unpushedBranches(ctx, repoPath)
		if err != nil {
			return err
		}
		if o.Unpushed == "push" {
			pushed = heads
		} else {
			bundled = heads
		}
	}
	for _, b := range slices.Sorted(maps.Keys(pushed)) {
		if last.Heads[b] != pushed[b] {
			plan.Push = append(plan.Push, "+"+pushed[b]+":"+o.headRef(b))
		}
	}
	for _, b := range slices.Sorted(maps.Keys(last.Heads)) {
		if _, ok := pushed[b]; !ok {
			plan.Push = append(plan.Push, ":"+o.headRef(b))
		}
	}
	if !maps.Equal(bundled, last.Bundled) {
		plan.Bundle = bundled
	}
	if len(pushed) > 0 {
		plan.Next.Heads = pushed
	}
	if len(bundled) > 0 {
		plan.Next.Bundled = bundled
	}
	return nil
}

// PushOffsite carries out the plan for the copy at repoPath and returns the
// new record. A part that fails keeps that part's old record, so it is
// retried next run.
func PushOffsite(ctx context.Context, repoPath string, latest *Latest, o Offsite, last OffsiteRecord) (OffsiteRecord, OffsitePlan, error) {
	plan, err := PlanOffsite(ctx, repoPath, latest, o, last)
	if err != nil {
		return last, plan, err
	}
	next := plan.Next
	var errs []error
	if len(plan.Push) > 0 {
		if err := o.push(ctx, repoPath, plan.Push); err != nil {
			errs = append(errs, fmt.Errorf("pushing to %s: %w", o.Remote, err))
			next.Remote, next.Namespace = last.Remote, last.Namespace
			next.WIP, next.Branch, next.Heads = last.WIP, last.Branch, last.Heads
		}
	}
	if plan.Bundle != nil {
		if err := writeBundle(ctx, repoPath, o.Bundle, plan.Bundle); err != nil {
			errs = append(errs, fmt.Errorf("bundling unpushed commits: %w", err))
			next.Bundled = last.Bundled
		}
	}
	return next, plan, errors.Join(errs...)
}

func (o Offsite) wipRef(id string) string      { return o.Namespace + "/wip/" + id }
func (o Offsite) headRef(branch string) string { return o.Namespace + "/heads/" + branch }

// push runs one push of every refspec. Servers differ on deleting a ref
// that is already gone -- some fail the whole push -- so deletions are
// checked against the remote first.
func (o Offsite) push(ctx context.Context, repoPath string, refspecs []string) error {
	if slices.ContainsFunc(refspecs, func(r string) bool { return strings.HasPrefix(r, ":") }) {
		have, err := o.remoteRefs(ctx, repoPath)
		if err != nil {
			return err
		}
		refspecs = slices.DeleteFunc(slices.Clone(refspecs), func(r string) bool {
			return strings.HasPrefix(r, ":") && !have[strings.TrimPrefix(r, ":")]
		})
		if len(refspecs) == 0 {
			return nil
		}
	}
	args := append([]string{"push", "--quiet", o.Remote}, refspecs...)
	_, err := GitWithSSHKey(ctx, repoPath, o.SSHKey, args...)
	return err
}

// remoteRefs lists the refs the copy owns on the remote.
func (o Offsite) remoteRefs(ctx context.Context, repoPath string) (map[string]bool, error) {
	patterns := []string{o.Namespace + "/*"}
	if o.Branch != "" {
		patterns = append(patterns, o.Branch)
	}
	out, err := GitWithSSHKey(ctx, repoPath, o.SSHKey, append([]string{"ls-remote", o.Remote}, patterns...)...)
	if err != nil {
		return nil, err
	}
	have := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		if _, ref, ok := strings.Cut(line, "\t"); ok {
			have[ref] = true
		}
	}
	return have, nil
}

// unpushedBranches returns the copy's branches holding commits that no
// remote-tracking ref reaches, with their tips. A linked worktree shares its
// branches with the repository, so only the branch it has checked out is its
// own; a detached HEAD with such commits is listed as "HEAD".
func unpushedBranches(ctx context.Context, repoPath string) (map[string]string, error) {
	gitDir, err := Git(ctx, repoPath, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return nil, err
	}
	common, err := Git(ctx, repoPath, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return nil, err
	}
	linked := filepath.Clean(gitDir) != filepath.Clean(common)

	tips := map[string]string{}
	if branch, err := Git(ctx, repoPath, "symbolic-ref", "-q", "--short", "HEAD"); err == nil && branch != "" {
		if tip, err := Git(ctx, repoPath, "rev-parse", "-q", "--verify", "HEAD^{commit}"); err == nil {
			tips[branch] = tip
		}
	} else if tip, err := Git(ctx, repoPath, "rev-parse", "-q", "--verify", "HEAD^{commit}"); err == nil {
		if n, err := Git(ctx, repoPath, "rev-list", "--count", tip, "--not", "--remotes", "--branches"); err == nil && n != "0" {
			tips[detachedHead] = tip
		}
	}
	if !linked {
		out, err := Git(ctx, repoPath, "for-each-ref", "--format=%(refname:strip=2) %(objectname)", "refs/heads")
		if err != nil {
			return nil, err
		}
		for _, line := range strings.Split(out, "\n") {
			if name, oid, ok := strings.Cut(line, " "); ok {
				tips[name] = oid
			}
		}
	}

	unpushed := map[string]string{}
	for name, tip := range tips {
		if name == detachedHead {
			unpushed[name] = tip
			continue
		}
		n, err := Git(ctx, repoPath, "rev-list", "--count", tip, "--not", "--remotes")
		if err != nil {
			return nil, err
		}
		if n != "0" {
			unpushed[name] = tip
		}
	}
	return unpushed, nil
}

// writeBundle writes a bundle of the branches' commits that no remote has to
// path, replacing it, or removes it when there are none.
func writeBundle(ctx context.Context, repoPath, path string, branches map[string]string) error {
	if len(branches) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".partial"
	args := []string{"bundle", "create", "--quiet", tmp}
	for _, b := range slices.Sorted(maps.Keys(branches)) {
		if b == detachedHead {
			args = append(args, "HEAD")
		} else {
			args = append(args, "refs/heads/"+b)
		}
	}
	args = append(args, "--not", "--remotes")
	if _, err := Git(ctx, repoPath, args...); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}
