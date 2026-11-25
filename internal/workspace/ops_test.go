package workspace

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ashtonian/gitstead/internal/gitops"
)

// The worktrees of one repo share its refs, so they take turns; a clone has
// refs of its own.
func TestRunsByRepo(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	source := filepath.Join(root, "src")
	git := func(dir string, args ...string) {
		t.Helper()
		if _, err := gitops.Git(ctx, dir, args...); err != nil {
			t.Fatal(err)
		}
	}
	git(root, "init", "-q", "-b", "main", source)
	git(source, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "init")
	var targets []Located
	for _, name := range []string{"agent-1", "agent-2"} {
		path := filepath.Join(root, "ws", name)
		git(source, "worktree", "add", "-q", "-b", name, path)
		targets = append(targets, Located{Agent: name, Copy: Copy{Path: path}})
	}
	clone := filepath.Join(root, "ws", "agent-3")
	git(root, "clone", "-q", source, clone)
	targets = []Located{targets[0], {Agent: "agent-3", Copy: Copy{Path: clone}}, targets[1]}

	if got, want := runsByRepo(ctx, targets), [][]int{{0, 2}, {1}}; !reflect.DeepEqual(got, want) {
		t.Errorf("runsByRepo = %v, want %v", got, want)
	}
}
