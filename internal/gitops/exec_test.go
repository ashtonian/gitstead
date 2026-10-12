package gitops_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ashtonian/git-mgr/internal/gitops"
)

// setupTestRepo creates a temp git repo with one commit.
func setupTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	cmds := [][]string{
		{"git", "init"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, out)
		}
	}

	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# test"), 0644); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	if _, err := gitops.Git(ctx, dir, "add", "."); err != nil {
		t.Fatalf("git add: %v", err)
	}
	if _, err := gitops.Git(ctx, dir, "commit", "-m", "initial"); err != nil {
		t.Fatalf("git commit: %v", err)
	}

	return dir
}

func TestGitExitCode(t *testing.T) {
	dir := setupTestRepo(t)
	ctx := context.Background()

	// Clean repo: diff --quiet should return 0
	code := gitops.GitExitCode(ctx, dir, "diff", "--quiet")
	if code != 0 {
		t.Errorf("expected exit code 0 for clean repo, got %d", code)
	}

	// Create a change
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("# changed"), 0644)

	// Dirty repo: diff --quiet should return 1
	code = gitops.GitExitCode(ctx, dir, "diff", "--quiet")
	if code != 1 {
		t.Errorf("expected exit code 1 for dirty repo, got %d", code)
	}
}

func TestGitExitCode_InvalidRepo(t *testing.T) {
	ctx := context.Background()
	code := gitops.GitExitCode(ctx, "/nonexistent/path", "status")
	if code == 0 {
		t.Error("expected non-zero exit code for invalid repo")
	}
}

func TestGit_RevParse(t *testing.T) {
	dir := setupTestRepo(t)
	ctx := context.Background()

	out, err := gitops.Git(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("rev-parse HEAD failed: %v", err)
	}
	if len(out) != 40 {
		t.Errorf("expected 40-char SHA, got len=%d %q", len(out), out)
	}
}

func TestIsLocked(t *testing.T) {
	dir := setupTestRepo(t)

	ctx := context.Background()
	locked, _ := gitops.IsLocked(ctx, dir)
	if locked {
		t.Error("expected repo to not be locked initially")
	}

	os.WriteFile(filepath.Join(dir, ".git", "index.lock"), []byte(""), 0644)
	locked, file := gitops.IsLocked(ctx, dir)
	if !locked {
		t.Error("expected repo to be locked")
	}
	if file != "index.lock" {
		t.Errorf("expected index.lock, got %s", file)
	}
}

func TestFetch_LockedReturnsErrLocked(t *testing.T) {
	dir := setupTestRepo(t)
	if err := os.WriteFile(filepath.Join(dir, ".git", "index.lock"), []byte(""), 0644); err != nil {
		t.Fatal(err)
	}
	err := gitops.Fetch(context.Background(), dir, "")
	if !errors.Is(err, gitops.ErrLocked) {
		t.Errorf("expected ErrLocked, got %v", err)
	}
}

func TestHasGitDir(t *testing.T) {
	dir := setupTestRepo(t)
	if !gitops.HasGitDir(dir) {
		t.Error("expected true for git repo")
	}
	if gitops.HasGitDir(t.TempDir()) {
		t.Error("expected false for non-repo")
	}
}

// A git error never repeats a credential from the URL it was given.
func TestGit_ErrorsRedactURLCredentials(t *testing.T) {
	t.Setenv("GIT_TERMINAL_PROMPT", "0")
	_, err := gitops.Git(context.Background(), t.TempDir(), "ls-remote", "https://me:s3cret-token@127.0.0.1:9/acme/api.git")
	if err == nil {
		t.Fatal("ls-remote against a closed port should fail")
	}
	if strings.Contains(err.Error(), "s3cret") || !strings.Contains(err.Error(), "https://***@127.0.0.1:9") {
		t.Fatalf("error leaks or loses the URL: %v", err)
	}
}
