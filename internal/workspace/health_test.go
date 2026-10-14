package workspace_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ashtonian/gitten/internal/config"
	"github.com/ashtonian/gitten/internal/gitops"
	"github.com/ashtonian/gitten/internal/workspace"
)

// A copy is flagged when it falls behind what it started from, sits on
// another branch than its agent's, or is missing.
func TestHealth(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	remote := bareRemote(t, root, "app")
	source := filepath.Join(root, "src")
	mustGit(t, root, "clone", "-q", remote, source)
	copyPath := filepath.Join(root, "ws", "agent-1")
	if err := gitops.AddWorktree(ctx, source, copyPath, "agents/ws/agent-1", "", gitops.CloneOptions{}); err != nil {
		t.Fatal(err)
	}
	spec := &workspace.Spec{Name: "ws", Isolation: config.IsolationWorktree, Replicas: []workspace.Replica{
		{Agent: "agent-1", Path: copyPath, Copies: []workspace.Copy{{Path: copyPath, Branch: "agents/ws/agent-1", Source: source}}},
		{Agent: "agent-2", Path: filepath.Join(root, "ws", "agent-2"), Copies: []workspace.Copy{{Path: filepath.Join(root, "ws", "agent-2"), Branch: "agents/ws/agent-2"}}},
	}}
	health := func() []workspace.CopyHealth { return workspace.Health(ctx, spec, spec.Targets()) }

	hs := health()
	if len(hs[0].Issues) != 0 || hs[0].State() != "clean" {
		t.Errorf("a fresh copy: %+v", hs[0])
	}
	if hs[1].State() != "missing" || len(hs[1].Issues) != 1 {
		t.Errorf("a missing copy: %+v", hs[1])
	}

	work := filepath.Join(root, "work")
	mustGit(t, root, "clone", "-q", remote, work)
	if err := os.WriteFile(filepath.Join(work, "new"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, work, "add", ".")
	mustGit(t, work, "commit", "-qm", "new")
	mustGit(t, work, "push", "-q", "origin", "HEAD:main")
	mustGit(t, source, "fetch", "-q", "origin")
	if hs := health(); hs[0].Behind != 1 || !strings.Contains(strings.Join(hs[0].Issues, ";"), "behind origin/HEAD by 1") {
		t.Errorf("a copy behind its base: %+v", hs[0])
	}

	mustGit(t, copyPath, "checkout", "-q", "-b", "elsewhere")
	if hs := health(); !strings.Contains(strings.Join(hs[0].Issues, ";"), `on branch "elsewhere"`) {
		t.Errorf("a copy on another branch: %+v", hs[0])
	}
	if workspace.OK(health()) {
		t.Error("OK with flagged copies")
	}
}

// Without origin's default branch on record, a copy is compared with the
// branch its repo's checkout tracks; a base ref that doesn't resolve is
// flagged rather than read as level.
func TestHealth_ComparesWithWhatItWasBranchedFrom(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	remote := bareRemote(t, root, "app")
	source := filepath.Join(root, "src")
	mustGit(t, root, "clone", "-q", remote, source)
	mustGit(t, source, "remote", "set-head", "origin", "--delete")
	mustGit(t, source, "config", "remote.origin.followRemoteHEAD", "never") // git 2.48+ fetches it back
	copyPath := filepath.Join(root, "ws", "agent-1")
	if err := gitops.AddWorktree(ctx, source, copyPath, "agents/ws/agent-1", "", gitops.CloneOptions{}); err != nil {
		t.Fatal(err)
	}
	spec := &workspace.Spec{Name: "ws", Isolation: config.IsolationWorktree, Replicas: []workspace.Replica{
		{Agent: "agent-1", Path: copyPath, Copies: []workspace.Copy{{Path: copyPath, Branch: "agents/ws/agent-1", Source: source}}},
	}}

	work := filepath.Join(root, "work")
	mustGit(t, root, "clone", "-q", remote, work)
	mustGit(t, work, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "new")
	mustGit(t, work, "push", "-q", "origin", "HEAD:main")
	mustGit(t, source, "fetch", "-q", "origin")
	if hs := workspace.Health(ctx, spec, spec.Targets()); hs[0].Behind != 1 || !strings.Contains(strings.Join(hs[0].Issues, ";"), "behind origin/main by 1") {
		t.Errorf("compared with the source's upstream: %+v", hs[0])
	}

	spec.BaseRef = "no-such-branch"
	if hs := workspace.Health(ctx, spec, spec.Targets()); !strings.Contains(strings.Join(hs[0].Issues, ";"), "can't compare with no-such-branch") {
		t.Errorf("an unresolvable base ref: %+v", hs[0])
	}
}
