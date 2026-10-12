package state_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
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
		LastFetch: now,
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
	if err := st.Lock(0); err != nil {
		t.Fatalf("first lock should succeed: %v", err)
	}

	// Second lock should fail
	st2, _ := state.Load(path)
	if err := st2.Lock(0); err == nil {
		t.Error("second lock should fail")
	}

	st.ReleaseLock()

	// After release, locking should work again
	st3, _ := state.Load(path)
	if err := st3.Lock(0); err != nil {
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

func TestLoad_MigratesWorkgroupsToWorkspaces(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.yaml")
	v2 := `version: 2
repos:
  github.com/me/app:
    local_path: /src/app
workgroups:
  app:
    kind: worktree
    root: /wg/app
    source: /src/app
    base_ref: origin/main
    declared: true
    members:
      - {name: agent-1, path: /wg/app/agent-1, branch: app/agent-1, copy_id: 0123456789abcdef, last_backup_error: boom}
  scratch:
    kind: worktree
    root: /wg/scratch
    source: /elsewhere/untracked
    members:
      - {name: agent-1, path: /wg/scratch/agent-1, branch: scratch/agent-1}
  review:
    kind: clone
    root: /wg/review
    members:
      - {name: api, path: /wg/review/api, repo_url: "git@github.com:me/api.git", repo_key: github.com/me/api}
`
	if err := os.WriteFile(path, []byte(v2), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := state.Load(path)
	if err != nil {
		t.Fatal(err)
	}

	app := st.Workspaces["app"]
	if app == nil || app.Dir != "/wg/app" || app.Isolation != "worktree" || app.BaseRef != "origin/main" || !app.Declared {
		t.Fatalf("app = %+v", app)
	}
	r := app.Replica("agent-1")
	if r == nil || r.Path != "/wg/app/agent-1" || len(r.Copies) != 1 {
		t.Fatalf("app's replica = %+v", r)
	}
	if c := r.Copies[0]; c.Repo != "github.com/me/app" || c.Path != r.Path || c.Branch != "app/agent-1" ||
		c.CopyID != "0123456789abcdef" || c.LastBackupError != "boom" {
		t.Errorf("app's copy = %+v; want the tracked source's copy, with its branch, ID and backup status", c)
	}
	if c := st.Workspaces["scratch"].Replica("agent-1").Copies[0]; c.Repo != "" {
		t.Errorf("a copy of an untracked source names repo %q", c.Repo)
	}
	review := st.Workspaces["review"]
	if c := review.Replica("api").Copies[0]; review.Isolation != "clone" || c.Repo != "github.com/me/api" || c.URL != "git@github.com:me/api.git" {
		t.Errorf("review = %+v, copy %+v", review, c)
	}

	if err := st.Save(); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "workgroups") {
		t.Errorf("saved state still has workgroups:\n%s", data)
	}
}

func TestRenameRepo_MovesItsCopies(t *testing.T) {
	st := &state.State{
		Repos: map[string]*state.RepoState{"github.com/me/old": {Name: "old"}},
		Workspaces: map[string]*state.WorkspaceState{"w": {Replicas: []state.ReplicaState{
			{Agent: "agent-1", Copies: []state.CopyState{{Repo: "github.com/me/old"}, {Repo: "github.com/me/other"}}},
		}}},
	}
	st.RenameRepo("github.com/me/old", "github.com/me/new")
	if st.Repos["github.com/me/new"] == nil || st.Repos["github.com/me/old"] != nil {
		t.Errorf("repos = %v", st.Repos)
	}
	copies := st.Workspaces["w"].Replicas[0].Copies
	if copies[0].Repo != "github.com/me/new" || copies[1].Repo != "github.com/me/other" {
		t.Errorf("copies = %+v", copies)
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
	if err := first.Lock(0); err != nil {
		t.Fatal(err)
	}
	defer first.ReleaseLock()
	second, _ := state.Load(path)
	if err := second.Lock(0); !errors.Is(err, state.ErrLocked) {
		t.Fatalf("second lock = %v, want ErrLocked", err)
	}
}

func TestLock_WaitsForRelease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.yaml")
	holder, _ := state.Load(path)
	if err := holder.Lock(0); err != nil {
		t.Fatal(err)
	}
	waiter, _ := state.Load(path)
	got := make(chan error, 1)
	go func() { got <- waiter.Lock(10 * time.Second) }()
	holder.ReleaseLock()
	if err := <-got; err != nil {
		t.Fatalf("a waiting Lock should get the lock once it is released: %v", err)
	}
	waiter.ReleaseLock()
}

func TestRunLock_IsSeparateFromTheStateLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.yaml")
	st, _ := state.Load(path)
	release, err := st.RunLock("sync")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := st.RunLock("sync"); !errors.Is(err, state.ErrLocked) {
		t.Errorf("a second sync must not start: %v", err)
	}
	other, _ := state.Load(path)
	if err := other.Lock(0); err != nil {
		t.Fatalf("the state lock stays free while a sync runs: %v", err)
	}
	other.ReleaseLock()
}

func TestUpdate_AppliesToTheLatestState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.yaml")
	a, _ := state.Load(path)
	b, _ := state.Load(path) // loaded before a's change
	if err := a.Update(0, func(s *state.State) error {
		s.Repos["one"] = &state.RepoState{Name: "one"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := b.Update(0, func(s *state.State) error {
		s.Repos["two"] = &state.RepoState{Name: "two"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	final, _ := state.Load(path)
	if len(final.Repos) != 2 {
		t.Fatalf("an update must not lose another's change: %v", final.Repos)
	}
}
