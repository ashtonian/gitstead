package discovery

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
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

// deleteFiles deletes unconditionally. Archiving unique work first is
// gitops.RemoveClone's job and is tested there.
func deleteFiles(_ string, repo *state.RepoState) (*state.ArchiveEntry, error) {
	return nil, os.RemoveAll(repo.LocalPath)
}

// mustNotRemove fails the test if a pass tries to delete anything.
func mustNotRemove(t *testing.T) RemoveFunc {
	return func(key string, _ *state.RepoState) (*state.ArchiveEntry, error) {
		t.Errorf("unexpected removal of %s", key)
		return nil, errors.New("unexpected removal")
	}
}

// run is a Cleanup.Run whose state lives only in memory.
func run(t *testing.T, st *state.State, discovered map[string]bool, scope StaleScope, cfg config.CleanupConfig, remove RemoveFunc) CleanupResult {
	t.Helper()
	c := Cleanup{Config: cfg, Remove: remove, Update: func(change func(*state.State) error) error { return change(st) }}
	res, err := c.Run(st, discovered, scope)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// The headline regression: a preview must never touch disk or tracking, even
// under remove_and_delete past the grace period.
func TestCleanup_PreviewNeverDeletes(t *testing.T) {
	st, dir := stalePastGrace(t)

	res := Cleanup{Config: removeAndDelete(), Remove: mustNotRemove(t)}.Preview(st, map[string]bool{}, StaleScope{all: true})

	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("preview deleted the working tree: %v", err)
	}
	if _, ok := st.Repos["host/owner/repo"]; !ok {
		t.Fatal("preview untracked the repo")
	}
	if res.Reaped != 0 || len(res.Kept) != 0 {
		t.Fatalf("preview reaped/failed should be 0, got %+v", res)
	}
	if res.Reapable != 1 {
		t.Fatalf("preview should report 1 reapable, got %+v", res)
	}
}

func TestCleanup_ApplyDeletes(t *testing.T) {
	st, dir := stalePastGrace(t)

	res := run(t, st, map[string]bool{}, StaleScope{all: true}, removeAndDelete(), deleteFiles)

	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("apply should have deleted the working tree, stat err=%v", err)
	}
	if _, ok := st.Repos["host/owner/repo"]; ok {
		t.Fatal("apply should have untracked the repo")
	}
	if res.Reaped != 1 || len(res.Kept) != 0 {
		t.Fatalf("expected Reaped=1 and nothing kept, got %+v", res)
	}
}

// A repo whose files the remover keeps (unique work it could not archive, an
// unsafe path) stays tracked and is reported with the reason.
func TestCleanup_KeepsRepoWhenRemoveFails(t *testing.T) {
	st, dir := stalePastGrace(t)
	refuse := func(string, *state.RepoState) (*state.ArchiveEntry, error) { return nil, errors.New("cannot archive") }

	res := run(t, st, map[string]bool{}, StaleScope{all: true}, removeAndDelete(), refuse)

	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("kept repo's files are gone: %v", err)
	}
	if _, ok := st.Repos["host/owner/repo"]; !ok {
		t.Fatal("repo should stay tracked after a refused delete")
	}
	if err := res.Kept["host/owner/repo"]; err == nil || !strings.Contains(err.Error(), "cannot archive") || res.Reaped != 0 {
		t.Fatalf("want the repo kept with its reason and nothing reaped, got %+v", res)
	}
}

// A repo found again while its files were being deleted stays tracked.
func TestCleanup_KeepsRepoChangedMeanwhile(t *testing.T) {
	st, _ := stalePastGrace(t)
	foundAgain := func(key string, _ *state.RepoState) (*state.ArchiveEntry, error) {
		st.Repos[key].Status = "clean" // another run's discovery found it
		return nil, nil
	}

	res := run(t, st, map[string]bool{}, StaleScope{all: true}, removeAndDelete(), foundAgain)

	if _, ok := st.Repos["host/owner/repo"]; !ok {
		t.Fatal("a repo found again meanwhile was untracked")
	}
	if res.Reaped != 0 || len(res.Kept) != 0 {
		t.Fatalf("expected nothing reaped, got %+v", res)
	}
}

// "remove" untracks and leaves the files alone.
func TestCleanup_RemoveUntracksOnly(t *testing.T) {
	st, dir := stalePastGrace(t)
	cleanup := config.CleanupConfig{Policy: "remove", GracePeriod: time.Minute}

	res := run(t, st, map[string]bool{}, StaleScope{all: true}, cleanup, mustNotRemove(t))

	if _, ok := st.Repos["host/owner/repo"]; ok || res.Reaped != 1 {
		t.Fatalf("remove should untrack the repo, got %+v", res)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("remove must keep the files: %v", err)
	}
}

// The default policy marks stale but must never reap, even with apply=true past
// the grace period.
func TestCleanup_MarkStaleNeverReaps(t *testing.T) {
	st, dir := stalePastGrace(t)
	cleanup := config.CleanupConfig{Policy: "mark_stale", GracePeriod: time.Minute, ProtectManual: true}

	res := run(t, st, map[string]bool{}, StaleScope{all: true}, cleanup, mustNotRemove(t))

	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("mark_stale must not delete: %v", err)
	}
	if res.Reapable != 0 || res.Reaped != 0 {
		t.Fatalf("mark_stale should never reap, got %+v", res)
	}
}

func TestCleanup_ProtectsManual(t *testing.T) {
	st, dir := stalePastGrace(t)
	st.Repos["host/owner/repo"].ManuallyAdded = true

	res := run(t, st, map[string]bool{}, StaleScope{all: true}, removeAndDelete(), mustNotRemove(t))

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
func TestCleanup_FoundClearsStale(t *testing.T) {
	st, _ := stalePastGrace(t)

	run(t, st, map[string]bool{"host/owner/repo": true}, StaleScope{all: true}, removeAndDelete(), mustNotRemove(t))

	repo := st.Repos["host/owner/repo"]
	if repo == nil {
		t.Fatal("found repo must stay tracked")
	}
	if repo.Status == "stale" || !repo.StaleSince.IsZero() {
		t.Fatalf("found repo should have stale state cleared, got status=%q staleSince=%v", repo.Status, repo.StaleSince)
	}
}

// A repo that only just went missing is marked stale but is not yet reapable.
func TestCleanup_NewlyStaleNotYetReapable(t *testing.T) {
	st, _ := stalePastGrace(t)
	st.Repos["host/owner/repo"].StaleSince = time.Time{} // never seen missing before
	st.Repos["host/owner/repo"].Status = "clean"

	res := run(t, st, map[string]bool{}, StaleScope{all: true}, removeAndDelete(), mustNotRemove(t))

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

// A filtered pass (`sync --tag personal`) must not judge repos found by rules it
// didn't run: a work repo already past the grace period under remove_and_delete
// stays on disk and tracked.
func TestCleanup_ScopedPassLeavesOtherRulesAlone(t *testing.T) {
	st, dir := stalePastGrace(t)
	st.Repos["host/owner/repo"].DiscoveredBy = "work"

	scope := StaleScopeOf([]DiscoverResult{{Rule: "personal"}}, "", "personal")
	res := run(t, st, map[string]bool{}, scope, removeAndDelete(), mustNotRemove(t))

	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("out-of-scope repo was deleted: %v", err)
	}
	if _, ok := st.Repos["host/owner/repo"]; !ok {
		t.Fatal("out-of-scope repo was untracked")
	}
	if res.NewlyStale != 0 || res.Reapable != 0 || res.Reaped != 0 {
		t.Fatalf("out-of-scope repo must be skipped, got %+v", res)
	}
}

// A filtered pass still judges repos found by a rule it ran.
func TestCleanup_ScopedPassJudgesItsOwnRules(t *testing.T) {
	st, _ := stalePastGrace(t)
	repo := st.Repos["host/owner/repo"]
	repo.DiscoveredBy = "personal"
	repo.Status = "clean"
	repo.StaleSince = time.Time{}

	scope := StaleScopeOf([]DiscoverResult{{Rule: "personal"}}, "", "personal")
	res := run(t, st, map[string]bool{}, scope, removeAndDelete(), mustNotRemove(t))

	if res.NewlyStale != 1 || repo.Status != "stale" {
		t.Fatalf("in-scope missing repo should go stale, got status=%q %+v", repo.Status, res)
	}
}

// An unfiltered pass covers every repo, including one whose rule was removed
// from config.
func TestStaleScopeOf_UnfilteredCoversRemovedRules(t *testing.T) {
	if !StaleScopeOf(nil, "", "").Covers(&state.RepoState{DiscoveredBy: "deleted-rule"}) {
		t.Fatal("unfiltered pass should cover repos from removed rules")
	}
	if StaleScopeOf(nil, "only-this", "").Covers(&state.RepoState{DiscoveredBy: "deleted-rule"}) {
		t.Fatal("a rule-filtered pass with no results should cover nothing")
	}
}

// A rule that failed judges none of its repos, even in an unfiltered pass.
func TestStaleScopeOf_FailedRuleCoversNothing(t *testing.T) {
	results := []DiscoverResult{{Rule: "work"}, {Rule: "personal", Err: errors.New("401")}}
	scope := StaleScopeOf(results, "", "")
	if !scope.Covers(&state.RepoState{DiscoveredBy: "work"}) {
		t.Error("the rule that ran should be covered")
	}
	if scope.Covers(&state.RepoState{DiscoveredBy: "personal"}) {
		t.Error("the failed rule must not be covered")
	}
	if scope.Covers(&state.RepoState{DiscoveredBy: "removed-rule"}) {
		t.Error("with a failure, repos of removed rules must not be judged")
	}
}

// A rule that came back incomplete judges none of its repos, and keeps an
// unfiltered pass from judging repos of removed rules.
func TestStaleScopeOf_IncompleteRuleCoversNothing(t *testing.T) {
	results := []DiscoverResult{{Rule: "work"}, {Rule: "search", Incomplete: []string{"capped at 1000"}}}
	scope := StaleScopeOf(results, "", "")
	if !scope.Covers(&state.RepoState{DiscoveredBy: "work"}) {
		t.Error("the complete rule should be covered")
	}
	if scope.Covers(&state.RepoState{DiscoveredBy: "search"}) {
		t.Error("the incomplete rule must not be covered")
	}
	if scope.Covers(&state.RepoState{DiscoveredBy: "removed-rule"}) {
		t.Error("with an incomplete rule, repos of removed rules must not be judged")
	}
}

// "ignore" never marks a repo stale, so it keeps syncing.
func TestCleanup_IgnoreLeavesReposAlone(t *testing.T) {
	st, _ := stalePastGrace(t)
	repo := st.Repos["host/owner/repo"]
	repo.Status, repo.StaleSince = "clean", time.Time{}
	cleanup := config.CleanupConfig{Policy: "ignore", GracePeriod: time.Minute, ProtectManual: true}

	res := run(t, st, map[string]bool{}, StaleScope{all: true}, cleanup, mustNotRemove(t))

	if repo.Status != "clean" || !repo.StaleSince.IsZero() || res.NewlyStale != 0 {
		t.Errorf("ignore marked the repo: status=%q since=%v %+v", repo.Status, repo.StaleSince, res)
	}
}

// A repos: entry is added by hand like a clone: dropping it from config
// doesn't make it stale.
func TestCleanup_ProtectsReposEntries(t *testing.T) {
	st, dir := stalePastGrace(t)
	st.Repos["host/owner/repo"].DiscoveredBy = ManualRule

	res := run(t, st, map[string]bool{}, StaleScope{all: true}, removeAndDelete(), mustNotRemove(t))

	if _, err := os.Stat(dir); err != nil || st.Repos["host/owner/repo"] == nil || res.Reapable != 0 {
		t.Errorf("a repos: entry was judged: %+v, %v", res, err)
	}
}

// A stale repo that workspace copies are made from is kept, and the reason
// reported, under either reaping policy.
func TestCleanup_KeepsARepoWithWorkspaceCopies(t *testing.T) {
	for _, policy := range []string{"remove", "remove_and_delete"} {
		st, dir := stalePastGrace(t)
		st.Workspaces = map[string]*state.WorkspaceState{"app": {Name: "app", Replicas: []state.ReplicaState{
			{Agent: "agent-1", Copies: []state.CopyState{{Repo: "host/owner/repo", Path: "/ws/app/agent-1"}}},
		}}}
		cfg := config.CleanupConfig{Policy: policy, GracePeriod: time.Minute}
		res := run(t, st, map[string]bool{}, StaleScope{all: true}, cfg, mustNotRemove(t))
		if st.Repos["host/owner/repo"] == nil || res.Reaped != 0 || !errors.Is(res.Kept["host/owner/repo"], state.ErrHasCopies) {
			t.Errorf("%s: result %+v, repos %v", policy, res, st.Repos)
		}
		if _, err := os.Stat(dir); err != nil {
			t.Errorf("%s: the repo's files were deleted", policy)
		}
	}
}

// The archive of a deleted stale repo's unique work goes in the catalog.
func TestCleanup_CatalogsArchives(t *testing.T) {
	st, dir := stalePastGrace(t)
	archived := func(key string, repo *state.RepoState) (*state.ArchiveEntry, error) {
		return &state.ArchiveEntry{ID: "a1", Repo: key, Source: repo.LocalPath}, os.RemoveAll(repo.LocalPath)
	}
	res := run(t, st, map[string]bool{}, StaleScope{all: true}, removeAndDelete(), archived)
	if res.Reaped != 1 || len(st.Archives) != 1 || st.Archives[0].ID != "a1" || st.Archives[0].Source != dir {
		t.Errorf("result %+v, archives %+v", res, st.Archives)
	}
}
