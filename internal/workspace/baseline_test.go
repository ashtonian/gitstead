package workspace_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ashtonian/gitstead/internal/config"
	"github.com/ashtonian/gitstead/internal/gitops"
	"github.com/ashtonian/gitstead/internal/workspace"
)

// copyFixture is one agent's copy, by isolation, of a repo whose tracked
// clone is source, and a seed to push upstream commits from.
type copyFixture struct {
	spec         *workspace.Spec
	copy         workspace.Located
	remote, seed string
	source       string
}

func newCopyFixture(t *testing.T, isolation string) *copyFixture {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	f := &copyFixture{remote: bareRemote(t, root, "app"), source: filepath.Join(root, "src"), seed: filepath.Join(root, "seed")}
	mustGit(t, root, "clone", "-q", f.remote, f.source)
	mustGit(t, root, "clone", "-q", f.remote, f.seed)
	for _, repo := range []string{f.source, f.seed} {
		mustGit(t, repo, "config", "user.email", "t@t")
		mustGit(t, repo, "config", "user.name", "t")
	}
	path, branch := filepath.Join(root, "ws", "agent-1"), "agents/ws/agent-1"
	var err error
	switch isolation {
	case config.IsolationWorktree:
		err = gitops.AddWorktree(ctx, f.source, path, branch, "", gitops.CloneOptions{})
	default:
		err = gitops.CloneFromSource(ctx, f.source, path, f.remote, branch, "", isolation == config.IsolationReferenceClone, gitops.CloneOptions{})
		mustGit(t, path, "config", "user.email", "t@t")
		mustGit(t, path, "config", "user.name", "t")
	}
	if err != nil {
		t.Fatal(err)
	}
	c := workspace.Copy{Path: path, Branch: branch, Source: f.source}
	f.spec = &workspace.Spec{Name: "ws", Isolation: isolation, Replicas: []workspace.Replica{{Agent: "agent-1", Path: path, Copies: []workspace.Copy{c}}}}
	f.copy = workspace.Located{Workspace: "ws", Agent: "agent-1", Copy: c}
	return f
}

// pushUpstream commits name on main upstream and fetches it as ws pull
// does: into the repo's clone, then into a clone copy from there.
func (f *copyFixture) pushUpstream(t *testing.T, name string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.seed, name), []byte(name+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, f.seed, "add", ".")
	mustGit(t, f.seed, "commit", "-qm", name)
	mustGit(t, f.seed, "push", "-q", "origin", "HEAD:main")
	mustGit(t, f.source, "fetch", "-q", "origin")
	if f.spec.Isolation != config.IsolationWorktree {
		if err := gitops.RefreshFromSource(context.Background(), f.copy.Path, f.source); err != nil {
			t.Fatal(err)
		}
	}
	return mustGit(t, f.seed, "rev-parse", "HEAD")
}

func (f *copyFixture) health(t *testing.T) workspace.CopyHealth {
	t.Helper()
	return workspace.Health(context.Background(), f.spec, f.spec.Targets())[0]
}

var copyIsolations = []string{config.IsolationWorktree, config.IsolationClone, config.IsolationReferenceClone}

// What a copy is compared with is what it can be brought up to: a branch
// without an upstream starts behind what it started from once that moves,
// and ws pull's update brings it level, as ws status then shows.
func TestUpdateToBase(t *testing.T) {
	ctx := context.Background()
	for _, isolation := range copyIsolations {
		t.Run(isolation, func(t *testing.T) {
			f := newCopyFixture(t, isolation)
			b := f.spec.Baseline(ctx, f.copy)
			if b != (workspace.Baseline{Ref: "origin/HEAD"}) {
				t.Fatalf("Baseline = %+v", b)
			}
			head := f.pushUpstream(t, "second")
			if h := f.health(t); !strings.Contains(strings.Join(h.Issues, ";"), "behind origin/HEAD by 1") {
				t.Fatalf("before: %+v", h)
			}

			out, skipped, err := workspace.UpdateToBase(ctx, f.copy.Path, b.Ref, false)
			if err != nil || skipped || out != "fast-forwarded to origin/HEAD" {
				t.Fatalf("UpdateToBase = %q, %v, %v", out, skipped, err)
			}
			if got := mustGit(t, f.copy.Path, "rev-parse", "HEAD"); got != head {
				t.Errorf("HEAD = %s, want %s", got, head)
			}
			if h := f.health(t); len(h.Issues) != 0 || h.Behind != 0 {
				t.Errorf("after: %+v", h)
			}
			if out, skipped, err := workspace.UpdateToBase(ctx, f.copy.Path, b.Ref, false); err != nil || skipped || out != "up to date with origin/HEAD" {
				t.Errorf("again: %q, %v, %v", out, skipped, err)
			}

			// An agent's own commit and a new one upstream: the fast-forward
			// is refused, the rebase carries the commit across.
			if err := os.WriteFile(filepath.Join(f.copy.Path, "work"), []byte("mine\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			mustGit(t, f.copy.Path, "add", "work")
			mustGit(t, f.copy.Path, "commit", "-qm", "agent's work")
			f.pushUpstream(t, "third")
			out, skipped, err = workspace.UpdateToBase(ctx, f.copy.Path, b.Ref, false)
			if err != nil || !skipped || !strings.Contains(out, "diverged from origin/HEAD: 1 ahead, 1 behind") {
				t.Fatalf("diverged: %q, %v, %v", out, skipped, err)
			}
			out, skipped, err = workspace.UpdateToBase(ctx, f.copy.Path, b.Ref, true)
			if err != nil || skipped || out != "rebased onto origin/HEAD" {
				t.Fatalf("rebase: %q, %v, %v", out, skipped, err)
			}
			if h := f.health(t); len(h.Issues) != 0 || h.Ahead != 1 || h.Behind != 0 {
				t.Errorf("after the rebase: %+v", h)
			}
		})
	}
}

// A worktree sees its repo's branches, a clone only its own view of origin:
// a base ref is named in the copy as a new branch resolves it there.
func TestBaseline(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		isolation, baseRef string
		want               workspace.Baseline
	}{
		{config.IsolationWorktree, "", workspace.Baseline{Ref: "origin/HEAD"}},
		{config.IsolationClone, "", workspace.Baseline{Ref: "origin/HEAD"}},
		{config.IsolationWorktree, "main", workspace.Baseline{Ref: "main"}},
		{config.IsolationClone, "main", workspace.Baseline{Ref: "origin/main"}},
		{config.IsolationClone, "no-such-branch", workspace.Baseline{Ref: "no-such-branch"}},
	}
	for _, tt := range tests {
		t.Run(tt.isolation+"/"+tt.baseRef, func(t *testing.T) {
			f := newCopyFixture(t, tt.isolation)
			f.spec.BaseRef = tt.baseRef
			if got := f.spec.Baseline(ctx, f.copy); got != tt.want {
				t.Errorf("Baseline = %+v, want %+v", got, tt.want)
			}
		})
	}

	t.Run("a published branch is compared with its upstream", func(t *testing.T) {
		for _, isolation := range []string{config.IsolationWorktree, config.IsolationClone} {
			f := newCopyFixture(t, isolation)
			f.spec.BaseRef = "main"
			mustGit(t, f.copy.Path, "push", "-q", "-u", "origin", f.copy.Branch)
			want := workspace.Baseline{Upstream: "origin/" + f.copy.Branch, Ref: "origin/" + f.copy.Branch}
			if got := f.spec.Baseline(ctx, f.copy); got != want {
				t.Errorf("%s: Baseline = %+v, want %+v", isolation, got, want)
			}
		}
	})

	t.Run("a shared folder without an upstream has nothing to compare with", func(t *testing.T) {
		f := newCopyFixture(t, config.IsolationWorktree)
		f.spec.Isolation = config.IsolationShared
		if got := f.spec.Baseline(ctx, f.copy); got != (workspace.Baseline{}) {
			t.Errorf("Baseline = %+v", got)
		}
		out, skipped, err := workspace.UpdateToBase(ctx, f.copy.Path, "", false)
		if err != nil || !skipped || !strings.Contains(out, "no upstream") {
			t.Errorf("UpdateToBase = %q, %v, %v", out, skipped, err)
		}
	})
}
