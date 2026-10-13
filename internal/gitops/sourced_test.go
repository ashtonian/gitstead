package gitops_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/ashtonian/git-mgr/internal/gitops"
)

// sourceFixture is an upstream with main and feature, and a clone of it, the
// source, with a local branch of its own checked out.
func sourceFixture(t *testing.T) (upstream, source string) {
	t.Helper()
	dir := t.TempDir()
	upstream = filepath.Join(dir, "upstream.git")
	seed := filepath.Join(dir, "seed")
	source = filepath.Join(dir, "source")
	mustGit(t, dir, "init", "--quiet", "--bare", "-b", "main", upstream)
	mustGit(t, dir, "clone", "--quiet", upstream, seed)
	mustGit(t, seed, "config", "user.email", "t@t")
	mustGit(t, seed, "config", "user.name", "t")
	writeFile(t, filepath.Join(seed, "a"), []byte("a\n"))
	mustGit(t, seed, "add", ".")
	mustGit(t, seed, "commit", "--quiet", "-m", "a")
	mustGit(t, seed, "push", "--quiet", "origin", "HEAD:main")
	mustGit(t, seed, "checkout", "--quiet", "-b", "feature")
	writeFile(t, filepath.Join(seed, "f"), []byte("f\n"))
	mustGit(t, seed, "add", ".")
	mustGit(t, seed, "commit", "--quiet", "-m", "f")
	mustGit(t, seed, "push", "--quiet", "origin", "feature")
	mustGit(t, dir, "clone", "--quiet", upstream, source)
	mustGit(t, source, "checkout", "--quiet", "-b", "mine")
	return upstream, source
}

func pushUpstream(t *testing.T, upstream, file string) string {
	t.Helper()
	work := filepath.Join(t.TempDir(), "w")
	mustGit(t, filepath.Dir(work), "clone", "--quiet", upstream, work)
	mustGit(t, work, "config", "user.email", "t@t")
	mustGit(t, work, "config", "user.name", "t")
	writeFile(t, filepath.Join(work, file), []byte(file+"\n"))
	mustGit(t, work, "add", ".")
	mustGit(t, work, "commit", "--quiet", "-m", file)
	mustGit(t, work, "push", "--quiet", "origin", "HEAD:main")
	return mustGit(t, work, "rev-parse", "HEAD")
}

func TestCloneFromSource(t *testing.T) {
	ctx := context.Background()
	upstream, source := sourceFixture(t)
	dest := filepath.Join(t.TempDir(), "ws", "agent-1")

	if err := gitops.CloneFromSource(ctx, source, dest, upstream, "agents/ws/agent-1", "", false, gitops.CloneOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := mustGit(t, dest, "symbolic-ref", "--short", "HEAD"); got != "agents/ws/agent-1" {
		t.Errorf("HEAD = %s", got)
	}
	if mustGit(t, dest, "rev-parse", "HEAD") != mustGit(t, source, "rev-parse", "origin/main") {
		t.Error("a new branch should start at origin's default branch")
	}
	if got := mustGit(t, dest, "remote", "get-url", "origin"); got != upstream {
		t.Errorf("origin = %s, want the upstream", got)
	}
	if got := mustGit(t, dest, "remote", "get-url", gitops.SourceRemote); got != source {
		t.Errorf("git-mgr remote = %s, want the source", got)
	}
	refs := mustGit(t, dest, "for-each-ref", "--format=%(refname)", "refs/heads")
	if refs != "refs/heads/agents/ws/agent-1" {
		t.Errorf("local branches:\n%s\nwant only the agent's", refs)
	}
	refs = mustGit(t, dest, "for-each-ref", "--format=%(refname)")
	if !strings.Contains(refs, "refs/remotes/git-mgr/mine") {
		t.Errorf("the git-mgr remote doesn't show the source's branches:\n%s", refs)
	}
	if !strings.Contains(refs, "refs/remotes/origin/feature") {
		t.Errorf("the copy lacks origin's branches:\n%s", refs)
	}
	if gitops.ReadCopyID(ctx, dest) == "" {
		t.Error("the copy has no copy ID")
	}
	if got := gitops.Source(ctx, dest); got != source {
		t.Errorf("Source = %q", got)
	}

	// Objects are hardlinked from the source, not downloaded or copied.
	var linked bool
	_ = filepath.WalkDir(filepath.Join(dest, ".git", "objects"), func(p string, d os.DirEntry, err error) error {
		if info, err := os.Stat(p); err == nil && !d.IsDir() {
			if st, ok := info.Sys().(*syscall.Stat_t); ok && st.Nlink > 1 {
				linked = true
			}
		}
		return nil
	})
	if !linked {
		t.Error("no object file is hardlinked to the source's")
	}
}

func TestCloneFromSource_TracksOriginsBranch(t *testing.T) {
	ctx := context.Background()
	upstream, source := sourceFixture(t)
	dest := filepath.Join(t.TempDir(), "agent-1")
	if err := gitops.CloneFromSource(ctx, source, dest, upstream, "feature", "", false, gitops.CloneOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := mustGit(t, dest, "rev-parse", "--abbrev-ref", "@{u}"); got != "origin/feature" {
		t.Errorf("upstream = %s; a branch origin has should be resumed", got)
	}
}

func TestCloneFromSource_Shared(t *testing.T) {
	ctx := context.Background()
	upstream, source := sourceFixture(t)
	dest := filepath.Join(t.TempDir(), "agent-1")
	if err := gitops.CloneFromSource(ctx, source, dest, upstream, "agents/x", "main", true, gitops.CloneOptions{}); err != nil {
		t.Fatal(err)
	}
	alt, err := os.ReadFile(filepath.Join(dest, ".git", "objects", "info", "alternates"))
	if err != nil || !strings.Contains(string(alt), filepath.Join(source, ".git", "objects")) {
		t.Errorf("alternates = %q, %v", alt, err)
	}
	if got := mustGit(t, source, "config", "gc.pruneExpire"); got != "never" {
		t.Errorf("source gc.pruneExpire = %q; gc there could prune objects the copy borrows", got)
	}

	// Moving the source breaks the borrowed objects until the copy is
	// repointed.
	moved := filepath.Join(filepath.Dir(source), "moved")
	if err := os.Rename(source, moved); err != nil {
		t.Fatal(err)
	}
	if err := gitops.RepointSource(ctx, dest, source, moved); err != nil {
		t.Fatal(err)
	}
	if _, err := gitops.Git(ctx, dest, "log", "--oneline", "-1", "origin/feature"); err != nil {
		t.Errorf("after repointing, the copy can't read its objects: %v", err)
	}
	if got := mustGit(t, dest, "remote", "get-url", gitops.SourceRemote); got != moved {
		t.Errorf("git-mgr remote = %s, want %s", got, moved)
	}
}

func TestRefreshFromSource_UsesTheSourcesFetch(t *testing.T) {
	ctx := context.Background()
	upstream, source := sourceFixture(t)
	dest := filepath.Join(t.TempDir(), "agent-1")
	if err := gitops.CloneFromSource(ctx, source, dest, upstream, "agents/x", "", false, gitops.CloneOptions{}); err != nil {
		t.Fatal(err)
	}
	head := pushUpstream(t, upstream, "b")
	mustGit(t, source, "fetch", "--quiet", "origin")
	// The copy's own origin is unreachable: the refresh must come from the
	// source.
	mustGit(t, dest, "remote", "set-url", "origin", "/nonexistent")

	if err := gitops.RefreshFromSource(ctx, dest, source); err != nil {
		t.Fatal(err)
	}
	if got := mustGit(t, dest, "rev-parse", "origin/main"); got != head {
		t.Errorf("origin/main = %s, want %s", got, head)
	}
}

func TestCloneFromSource_RefusesAPartialSource(t *testing.T) {
	ctx := context.Background()
	upstream, _ := sourceFixture(t)
	mustGit(t, upstream, "config", "uploadpack.allowFilter", "true")
	partial := filepath.Join(t.TempDir(), "partial")
	mustGit(t, filepath.Dir(partial), "clone", "--quiet", "--filter=blob:none", "file://"+upstream, partial)

	err := gitops.CloneFromSource(ctx, partial, filepath.Join(t.TempDir(), "c"), upstream, "agents/x", "", false, gitops.CloneOptions{})
	if !errors.Is(err, gitops.ErrPartialSource) {
		t.Fatalf("err = %v, want ErrPartialSource", err)
	}
}

func TestAddWorktree(t *testing.T) {
	ctx := context.Background()
	_, source := sourceFixture(t)
	root := t.TempDir()

	fresh := filepath.Join(root, "ws", "agent-1")
	if err := gitops.AddWorktree(ctx, source, fresh, "agents/ws/agent-1", "", gitops.CloneOptions{}); err != nil {
		t.Fatal(err)
	}
	if mustGit(t, fresh, "rev-parse", "HEAD") != mustGit(t, source, "rev-parse", "origin/main") {
		t.Error("a new branch should start at origin's default branch, not the source's checkout")
	}
	if got, _ := filepath.EvalSymlinks(gitops.Source(ctx, fresh)); got != mustEval(t, source) {
		t.Errorf("Source = %q, want %q", got, source)
	}

	resumed := filepath.Join(root, "ws", "agent-2")
	if err := gitops.AddWorktree(ctx, source, resumed, "feature", "", gitops.CloneOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := mustGit(t, resumed, "rev-parse", "--abbrev-ref", "@{u}"); got != "origin/feature" {
		t.Errorf("upstream = %s", got)
	}

	// A local branch is checked out as it is, here after its worktree was
	// removed.
	mustGit(t, source, "worktree", "remove", fresh)
	if err := gitops.AddWorktree(ctx, source, fresh, "agents/ws/agent-1", "feature", gitops.CloneOptions{}); err != nil {
		t.Fatal(err)
	}
	if mustGit(t, fresh, "rev-parse", "HEAD") != mustGit(t, source, "rev-parse", "origin/main") {
		t.Error("an existing branch was moved to the base")
	}
}

func mustEval(t *testing.T, path string) string {
	t.Helper()
	p, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestResolveBase(t *testing.T) {
	ctx := context.Background()
	_, source := sourceFixture(t)
	if got, err := gitops.ResolveBase(ctx, source, "feature"); err != nil || got != mustGit(t, source, "rev-parse", "origin/feature") {
		t.Errorf("a branch only origin has: %s, %v", got, err)
	}
	if _, err := gitops.ResolveBase(ctx, source, "nope"); err == nil {
		t.Error("an unknown base ref resolved")
	}
}
