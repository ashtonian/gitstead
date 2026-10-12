package workgroup_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ashtonian/git-mgr/internal/config"
	"github.com/ashtonian/git-mgr/internal/gitops"
	"github.com/ashtonian/git-mgr/internal/state"
	"github.com/ashtonian/git-mgr/internal/workgroup"
)

// Exercises the full config-driven lifecycle: resolve -> ensure -> check ->
// group commit -> teardown, the same path the CLI takes.
func TestConfigDrivenLifecycle(t *testing.T) {
	ctx := context.Background()
	src := initRepo(t)
	if err := os.WriteFile(filepath.Join(src, "app.txt"), []byte("v1"), 0644); err != nil {
		t.Fatal(err)
	}
	gitops.Git(ctx, src, "add", ".")
	gitops.Git(ctx, src, "commit", "-m", "app")

	cfg := &config.Config{
		WorkgroupBaseDir: t.TempDir(),
		Workgroups: []config.Workgroup{{
			Name:    "feat",
			Kind:    "worktree",
			Source:  src,
			Members: []config.WorkgroupMember{{Name: "a"}, {Name: "b"}},
		}},
	}
	st := &state.State{
		Repos:      map[string]*state.RepoState{},
		Workgroups: map[string]*state.WorkgroupState{},
	}

	spec, err := workgroup.FromConfig(cfg, st, "feat")
	if err != nil {
		t.Fatalf("FromConfig: %v", err)
	}
	if len(spec.Members) != 2 {
		t.Fatalf("expected 2 members, got %d", len(spec.Members))
	}

	if _, err := workgroup.Ensure(ctx, spec, nil); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	if h := workgroup.ComputeHealth(ctx, spec); !h.OK() {
		t.Errorf("expected a healthy workgroup, got issues: %+v", h.Members)
	}

	// One member gets a tracked change; a group commit must touch only it.
	if err := os.WriteFile(filepath.Join(spec.Members[0].Path, "app.txt"), []byte("v2"), 0644); err != nil {
		t.Fatal(err)
	}
	results := workgroup.ForEach(ctx, spec, 4, func(ctx context.Context, m workgroup.Member) (string, bool, error) {
		committed, err := gitops.Commit(ctx, m.Path, "update", false)
		return "", !committed, err
	})
	committed := 0
	for _, r := range results {
		if r.Err != nil {
			t.Fatalf("%s commit: %v", r.Name, r.Err)
		}
		if !r.Skipped {
			committed++
		}
	}
	if committed != 1 {
		t.Errorf("expected exactly 1 member to commit, got %d", committed)
	}

	archiveDir := t.TempDir()
	for _, m := range spec.Members {
		if _, err := workgroup.RemoveMember(ctx, spec, m, archiveDir, false); err != nil {
			t.Fatalf("RemoveMember(%s): %v", m.Name, err)
		}
		if gitops.HasGitDir(m.Path) {
			t.Errorf("member %s should be gone after RemoveMember", m.Name)
		}
	}
}
