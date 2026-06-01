package discovery

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ashtonian/git-mgr/internal/config"
	"github.com/ashtonian/git-mgr/internal/state"
)

// stalePastGrace builds a state with one tracked repo that went stale an hour
// ago, rooted at a real on-disk directory so deletion is observable.
func stalePastGrace(t *testing.T) (*state.State, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "host", "owner", "repo")
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "wip.txt"), []byte("uncommitted"), 0o644); err != nil {
		t.Fatal(err)
	}
	st := &state.State{Repos: map[string]*state.RepoState{
		"host/owner/repo": {
			LocalPath:  dir,
			Cloned:     true,
			Status:     "stale",
			StaleSince: time.Now().Add(-time.Hour),
		},
	}}
	return st, dir
}

func removeAndDelete() config.CleanupConfig {
	return config.CleanupConfig{Policy: "remove_and_delete", GracePeriod: time.Minute, ProtectManual: true}
}

// The headline regression: a preview pass (apply=false) must never touch disk
// or tracking, even under remove_and_delete past the grace period.
func TestDetectStale_PreviewNeverDeletes(t *testing.T) {
	st, dir := stalePastGrace(t)

	res := DetectStale(st, map[string]bool{}, removeAndDelete(), false)

	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("preview deleted the working tree: %v", err)
	}
	if _, ok := st.Repos["host/owner/repo"]; !ok {
		t.Fatal("preview untracked the repo")
	}
	if res.Reaped != 0 || res.Failed != 0 {
		t.Fatalf("preview reaped/failed should be 0, got %+v", res)
	}
	if res.Reapable != 1 {
		t.Fatalf("preview should report 1 reapable, got %+v", res)
	}
}

func TestDetectStale_ApplyDeletes(t *testing.T) {
	st, dir := stalePastGrace(t)

	res := DetectStale(st, map[string]bool{}, removeAndDelete(), true)

	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("apply should have deleted the working tree, stat err=%v", err)
	}
	if _, ok := st.Repos["host/owner/repo"]; ok {
		t.Fatal("apply should have untracked the repo")
	}
	if res.Reaped != 1 || res.Failed != 0 {
		t.Fatalf("expected Reaped=1 Failed=0, got %+v", res)
	}
}

// A shallow/unsafe LocalPath must be refused by the SafeRemoveAll guard, leaving
// the repo tracked and counted as Failed rather than silently orphaned.
func TestDetectStale_ApplyRefusesUnsafePath(t *testing.T) {
	st := &state.State{Repos: map[string]*state.RepoState{
		"host/owner/repo": {
			LocalPath:  "/x", // <3 path components -> SafeRemoveAll refuses
			Cloned:     true,
			Status:     "stale",
			StaleSince: time.Now().Add(-time.Hour),
		},
	}}

	res := DetectStale(st, map[string]bool{}, removeAndDelete(), true)

	if _, ok := st.Repos["host/owner/repo"]; !ok {
		t.Fatal("repo with unsafe path should stay tracked after a refused delete")
	}
	if res.Failed != 1 || res.Reaped != 0 {
		t.Fatalf("expected Failed=1 Reaped=0, got %+v", res)
	}
}

// The default policy marks stale but must never reap, even with apply=true past
// the grace period.
func TestDetectStale_MarkStaleNeverReaps(t *testing.T) {
	st, dir := stalePastGrace(t)
	cleanup := config.CleanupConfig{Policy: "mark_stale", GracePeriod: time.Minute, ProtectManual: true}

	res := DetectStale(st, map[string]bool{}, cleanup, true)

	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("mark_stale must not delete: %v", err)
	}
	if res.Reapable != 0 || res.Reaped != 0 {
		t.Fatalf("mark_stale should never reap, got %+v", res)
	}
}

func TestDetectStale_ProtectsManual(t *testing.T) {
	st, dir := stalePastGrace(t)
	st.Repos["host/owner/repo"].ManuallyAdded = true

	res := DetectStale(st, map[string]bool{}, removeAndDelete(), true)

	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("manually-added repo must not be deleted: %v", err)
	}
	if _, ok := st.Repos["host/owner/repo"]; !ok {
		t.Fatal("manually-added repo must stay tracked")
	}
	if res.Reapable != 0 || res.Reaped != 0 {
		t.Fatalf("manually-added repo must be skipped, got %+v", res)
	}
}

// A repo found again by discovery has its stale state cleared.
func TestDetectStale_FoundClearsStale(t *testing.T) {
	st, _ := stalePastGrace(t)

	DetectStale(st, map[string]bool{"host/owner/repo": true}, removeAndDelete(), true)

	repo := st.Repos["host/owner/repo"]
	if repo == nil {
		t.Fatal("found repo must stay tracked")
	}
	if repo.Status == "stale" || !repo.StaleSince.IsZero() {
		t.Fatalf("found repo should have stale state cleared, got status=%q staleSince=%v", repo.Status, repo.StaleSince)
	}
}

// A repo that only just went missing is marked stale but is not yet reapable.
func TestDetectStale_NewlyStaleNotYetReapable(t *testing.T) {
	st, _ := stalePastGrace(t)
	st.Repos["host/owner/repo"].StaleSince = time.Time{} // never seen missing before
	st.Repos["host/owner/repo"].Status = "clean"

	res := DetectStale(st, map[string]bool{}, removeAndDelete(), true)

	if res.NewlyStale != 1 {
		t.Fatalf("expected NewlyStale=1, got %+v", res)
	}
	if res.Reapable != 0 || res.Reaped != 0 {
		t.Fatalf("newly stale repo must not be reaped, got %+v", res)
	}
	if st.Repos["host/owner/repo"].Status != "stale" {
		t.Fatal("repo should be marked stale")
	}
}
