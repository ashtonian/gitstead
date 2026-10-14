package gitops_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ashtonian/gitstead/internal/gitops"
)

const ns = "refs/gitstead/laptop/0123456789abcdef"

func offsite(mods ...func(*gitops.Offsite)) gitops.Offsite {
	o := gitops.Offsite{
		Remote: "origin", Namespace: ns, WIP: "ref", Unpushed: "push", Keep: 24 * time.Hour,
		Branch: "refs/heads/gitstead/wip/laptop/0123456789abcdef",
		Now:    func() time.Time { return time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC) },
	}
	for _, m := range mods {
		m(&o)
	}
	return o
}

// remoteRefs lists the remote's refs under prefix as name -> commit.
func remoteRefs(t *testing.T, remote, prefix string) map[string]string {
	t.Helper()
	refs := map[string]string{}
	for _, line := range strings.Split(mustGit(t, remote, "for-each-ref", "--format=%(refname) %(objectname)", prefix), "\n") {
		if name, oid, ok := strings.Cut(line, " "); ok {
			refs[name] = oid
		}
	}
	return refs
}

func backupNow(t *testing.T, clone string, at time.Time) *gitops.BackupResult {
	t.Helper()
	res, err := gitops.Backup(context.Background(), clone, gitops.NewBackupConfig(
		gitops.WithStrategy("stash-ref"), gitops.WithClock(func() time.Time { return at })))
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func latest(b *gitops.BackupResult) *gitops.Latest {
	return &gitops.Latest{Snapshot: b.Snapshot, BackupID: b.ID}
}

func TestOffsite_WIPRefs(t *testing.T) {
	ctx := context.Background()
	remote, clone := remoteAndClone(t)
	day1 := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	writeFile(t, filepath.Join(clone, "a.txt"), []byte("wip 1\n"))
	b1 := backupNow(t, clone, day1)

	o := offsite(func(o *gitops.Offsite) { o.Unpushed = "off" })
	rec, _, err := gitops.PushOffsite(ctx, clone, latest(b1), o, gitops.OffsiteRecord{})
	if err != nil {
		t.Fatal(err)
	}
	if got := remoteRefs(t, remote, "refs/gitstead/"); got[ns+"/wip/"+b1.ID] != b1.Snapshot || len(got) != 1 {
		t.Fatalf("remote refs after the first push: %v", got)
	}
	if heads := remoteRefs(t, remote, "refs/heads/"); len(heads) != 1 {
		t.Errorf("WIP must stay out of branches: %v", heads)
	}

	// Nothing changed: nothing to push.
	if plan, err := gitops.PlanOffsite(ctx, clone, latest(b1), o, rec); err != nil || !plan.Empty() {
		t.Errorf("an unchanged copy planned %+v, %v", plan, err)
	}

	// A new backup is added; the first, older than keep, is deleted.
	writeFile(t, filepath.Join(clone, "a.txt"), []byte("wip 2\n"))
	b2 := backupNow(t, clone, day1.Add(48*time.Hour))
	rec, _, err = gitops.PushOffsite(ctx, clone, latest(b2), o, rec)
	if err != nil {
		t.Fatal(err)
	}
	if got := remoteRefs(t, remote, "refs/gitstead/"); len(got) != 1 || got[ns+"/wip/"+b2.ID] != b2.Snapshot {
		t.Errorf("remote refs after the second push: %v", got)
	}

	// Without this run's backup the WIP state is unknown: its refs stay.
	if plan, err := gitops.PlanOffsite(ctx, clone, nil, offsite(func(o *gitops.Offsite) { o.Keep = 0; o.Unpushed = "off" }), rec); err != nil || !plan.Empty() {
		t.Errorf("with no backup this run, planned %+v, %v", plan, err)
	}

	// A ref someone else deleted doesn't fail the next run's cleanup.
	mustGit(t, remote, "update-ref", "-d", ns+"/wip/"+b2.ID)
	if _, _, err := gitops.PushOffsite(ctx, clone, &gitops.Latest{}, offsite(func(o *gitops.Offsite) { o.Keep = 0; o.Unpushed = "off" }), rec); err != nil {
		t.Errorf("cleaning up a ref the remote no longer has: %v", err)
	}
}

func TestOffsite_UnpushedCommits(t *testing.T) {
	ctx := context.Background()
	remote, clone := remoteAndClone(t)
	mustGit(t, clone, "switch", "-q", "-c", "feature")
	commitFile(t, clone, "f.txt", "feature work\n")
	tip := mustGit(t, clone, "rev-parse", "HEAD")

	o := offsite(func(o *gitops.Offsite) { o.WIP = "off" })
	rec, _, err := gitops.PushOffsite(ctx, clone, &gitops.Latest{}, o, gitops.OffsiteRecord{})
	if err != nil {
		t.Fatal(err)
	}
	if got := remoteRefs(t, remote, "refs/gitstead/"); got[ns+"/heads/feature"] != tip || len(got) != 1 {
		t.Fatalf("unpushed commits on the remote: %v (want feature at %s, and main left out: it has nothing unpushed)", got, tip)
	}
	if _, ok := remoteRefs(t, remote, "refs/heads/")["refs/heads/feature"]; ok {
		t.Fatal("the branch itself must not be pushed")
	}

	// Once the branch is pushed for real, its saved copy goes.
	mustGit(t, clone, "push", "-q", "origin", "feature")
	if _, _, err := gitops.PushOffsite(ctx, clone, &gitops.Latest{}, o, rec); err != nil {
		t.Fatal(err)
	}
	if got := remoteRefs(t, remote, "refs/gitstead/"); len(got) != 0 {
		t.Errorf("refs left after the branch was pushed: %v", got)
	}
}

func TestOffsite_Bundle(t *testing.T) {
	ctx := context.Background()
	remote, clone := remoteAndClone(t)
	commitFile(t, clone, "b.txt", "local only\n")
	bundle := filepath.Join(t.TempDir(), "unpushed", "copy.bundle")

	o := offsite(func(o *gitops.Offsite) { o.WIP, o.Unpushed, o.Bundle = "off", "bundle", bundle })
	rec, _, err := gitops.PushOffsite(ctx, clone, &gitops.Latest{}, o, gitops.OffsiteRecord{})
	if err != nil {
		t.Fatal(err)
	}
	if out := mustGit(t, clone, "bundle", "list-heads", bundle); !strings.Contains(out, "refs/heads/main") {
		t.Errorf("bundle heads: %s", out)
	}
	if got := remoteRefs(t, remote, "refs/gitstead/"); len(got) != 0 {
		t.Errorf("bundle mode pushed %v", got)
	}

	// Pushed for real: the bundle is no longer needed.
	mustGit(t, clone, "push", "-q", "origin", "main")
	if _, _, err := gitops.PushOffsite(ctx, clone, &gitops.Latest{}, o, rec); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(bundle); !os.IsNotExist(err) {
		t.Error("the bundle outlived the commits it protected")
	}
}

// Each copy writes only its own refs: two clones of one repo on one branch
// don't overwrite each other's WIP.
func TestOffsite_CopiesDontCollide(t *testing.T) {
	ctx := context.Background()
	remote, clone := remoteAndClone(t)
	other := filepath.Join(t.TempDir(), "other")
	mustGit(t, filepath.Dir(other), "clone", "-q", remote, other)

	for i, c := range []string{clone, other} {
		writeFile(t, filepath.Join(c, "a.txt"), []byte("agent "+string(rune('1'+i))+"\n"))
		b := backupNow(t, c, time.Date(2026, 10, 1, 11, 0, 0, 0, time.UTC))
		copyID, err := gitops.CopyID(ctx, c)
		if err != nil {
			t.Fatal(err)
		}
		o := offsite(func(o *gitops.Offsite) {
			o.WIP, o.Namespace, o.Branch = "branch", "refs/gitstead/laptop/"+copyID, "refs/heads/gitstead/wip/laptop/"+copyID
		})
		if _, _, err := gitops.PushOffsite(ctx, c, latest(b), o, gitops.OffsiteRecord{}); err != nil {
			t.Fatal(err)
		}
	}
	if got := remoteRefs(t, remote, "refs/heads/gitstead/wip/"); len(got) != 2 {
		t.Errorf("visible WIP branches: %v; want one per copy", got)
	}
}

// The visible WIP branch holds the tracked changes on top of their base:
// no index commit, no untracked files.
func TestOffsite_WIPBranch(t *testing.T) {
	ctx := context.Background()
	remote, clone := remoteAndClone(t)
	writeFile(t, filepath.Join(clone, "a.txt"), []byte("wip\n"))
	writeFile(t, filepath.Join(clone, "local-only.txt"), []byte("untracked\n"))
	b := backupNow(t, clone, time.Date(2026, 10, 1, 11, 0, 0, 0, time.UTC))

	o := offsite(func(o *gitops.Offsite) { o.WIP, o.Unpushed = "branch", "off" })
	rec, _, err := gitops.PushOffsite(ctx, clone, latest(b), o, gitops.OffsiteRecord{})
	if err != nil {
		t.Fatal(err)
	}
	pushed := mustGit(t, remote, "rev-parse", o.Branch)
	if got := mustGit(t, remote, "show", pushed+":a.txt"); got != "wip" {
		t.Errorf("pushed a.txt = %q", got)
	}
	if _, err := gitops.Git(ctx, remote, "show", pushed+":local-only.txt"); err == nil {
		t.Error("untracked files must not be pushed")
	}
	if parents := mustGit(t, remote, "rev-list", "--parents", "-n1", pushed); len(strings.Fields(parents)) != 2 {
		t.Errorf("the WIP commit must have only the base as parent: %s", parents)
	}

	// Unchanged: nothing to push. Clean: the branch goes.
	if plan, err := gitops.PlanOffsite(ctx, clone, latest(b), o, rec); err != nil || !plan.Empty() {
		t.Errorf("an unchanged copy planned %+v, %v", plan, err)
	}
	mustGit(t, clone, "checkout", "-q", "--", "a.txt")
	if _, _, err := gitops.PushOffsite(ctx, clone, &gitops.Latest{}, o, rec); err != nil {
		t.Fatal(err)
	}
	if got := remoteRefs(t, remote, "refs/heads/gitstead/"); len(got) != 0 {
		t.Errorf("a clean copy's WIP branch stayed: %v", got)
	}
}
