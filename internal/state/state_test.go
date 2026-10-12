package state_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ashtonian/git-mgr/internal/state"
)

func TestLoadSave(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.yaml")

	// Load non-existent creates empty state
	st, err := state.Load(path)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if st.Version != state.CurrentVersion {
		t.Errorf("version: got %d, want %d", st.Version, state.CurrentVersion)
	}
	if len(st.Repos) != 0 {
		t.Errorf("repos: got %d, want 0", len(st.Repos))
	}

	// Add a repo and save
	now := time.Now()
	st.Repos["github.com/user/repo"] = &state.RepoState{
		Host:      "github.com",
		Owner:     "user",
		Name:      "repo",
		FullName:  "user/repo",
		Status:    "clean",
		Tags:      []string{"personal"},
		FirstSeen: now,
	}
	st.LastSync = now

	if err := st.Save(); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// Reload and verify
	st2, err := state.Load(path)
	if err != nil {
		t.Fatalf("Load after save failed: %v", err)
	}
	if len(st2.Repos) != 1 {
		t.Fatalf("repos: got %d, want 1", len(st2.Repos))
	}
	repo := st2.Repos["github.com/user/repo"]
	if repo.Host != "github.com" {
		t.Errorf("host: got %q", repo.Host)
	}
	if repo.Status != "clean" {
		t.Errorf("status: got %q", repo.Status)
	}
	if !repo.HasTag("personal") {
		t.Error("expected tag 'personal'")
	}
}

func TestAcquireLock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.yaml")

	st, _ := state.Load(path)
	if err := st.AcquireLock(); err != nil {
		t.Fatalf("first lock should succeed: %v", err)
	}

	// Second lock should fail
	st2, _ := state.Load(path)
	if err := st2.AcquireLock(); err == nil {
		t.Error("second lock should fail")
	}

	st.ReleaseLock()

	// After release, locking should work again
	st3, _ := state.Load(path)
	if err := st3.AcquireLock(); err != nil {
		t.Fatalf("lock after release should succeed: %v", err)
	}
	st3.ReleaseLock()
}

func TestHasTag(t *testing.T) {
	repo := &state.RepoState{Tags: []string{"work", "backend"}}
	if !repo.HasTag("work") {
		t.Error("expected HasTag('work') to be true")
	}
	if repo.HasTag("personal") {
		t.Error("expected HasTag('personal') to be false")
	}
}

func TestLoad_MigratesVersion1(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.yaml")
	v1 := `version: 1
repos:
  local/me/added:
    tags: [mine]
    manually_added: true
  github.com/acme/found:
    tags: [work]
`
	if err := os.WriteFile(path, []byte(v1), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := state.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Version != state.CurrentVersion {
		t.Errorf("version = %d", st.Version)
	}
	if got := st.Repos["local/me/added"].UserTags; len(got) != 1 || got[0] != "mine" {
		t.Errorf("a hand-added repo's tags are user tags, got %v", got)
	}
	if got := st.Repos["github.com/acme/found"].UserTags; len(got) != 0 {
		t.Errorf("a discovered repo's tags came from rules, got user tags %v", got)
	}
}

func TestLoad_RefusesNewerVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.yaml")
	if err := os.WriteFile(path, []byte("version: 99\nrepos: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := state.Load(path); !errors.Is(err, state.ErrNewerState) {
		t.Fatalf("Load of a newer state = %v, want ErrNewerState", err)
	}
}

func TestAcquireLock_HeldIsErrLocked(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.yaml")
	first, _ := state.Load(path)
	if err := first.AcquireLock(); err != nil {
		t.Fatal(err)
	}
	defer first.ReleaseLock()
	second, _ := state.Load(path)
	if err := second.AcquireLock(); !errors.Is(err, state.ErrLocked) {
		t.Fatalf("second lock = %v, want ErrLocked", err)
	}
}
