package discovery

import (
	"maps"
	"slices"
	"time"

	"github.com/ashtonian/git-mgr/internal/config"
	"github.com/ashtonian/git-mgr/internal/state"
)

// CleanupResult summarizes a stale pass.
type CleanupResult struct {
	NewlyStale int // repos that transitioned to stale on this pass
	Reapable   int // stale repos past the grace period eligible for cleanup
	Reaped     int // repos removed from tracking (Run only)
	Failed     int // repos whose on-disk deletion failed (Run only); left tracked
}

// RemoveFunc deletes a stale repo's files. When it cannot do so without
// losing work it must leave them in place and return an error.
type RemoveFunc func(key string, repo *state.RepoState) error

// StaleScope is the set of tracked repos a discovery pass is entitled to judge. A
// repo absent from a pass's results only means something if the pass ran the
// rule that would have found it.
type StaleScope struct {
	all   bool
	rules map[string]bool
}

// StaleScopeOf returns the scope of a pass that produced results under the given
// rule and tag filters. An unfiltered pass in which every rule succeeded
// completely covers every tracked repo, including repos whose rule has since
// been removed from config. Otherwise it covers only repos first found by a
// rule that ran and returned everything it matches, so `sync --tag personal`
// cannot judge work repos, and a rule that failed or came back incomplete
// judges nothing.
func StaleScopeOf(results []DiscoverResult, ruleFilter, tagFilter string) StaleScope {
	complete := func(r DiscoverResult) bool { return r.Err == nil && len(r.Incomplete) == 0 }
	if ruleFilter == "" && tagFilter == "" && !slices.ContainsFunc(results, func(r DiscoverResult) bool { return !complete(r) }) {
		return StaleScope{all: true}
	}
	rules := make(map[string]bool, len(results))
	for _, r := range results {
		if complete(r) {
			rules[r.Rule] = true
		}
	}
	return StaleScope{rules: rules}
}

// Covers reports whether the pass may mark repo stale.
func (s StaleScope) Covers(repo *state.RepoState) bool {
	return s.all || s.rules[repo.DiscoveredBy]
}

// Cleanup judges tracked repos against what a discovery pass found: repos in
// scope it no longer found go stale, and under a reaping policy the ones
// stale past the grace period are cleaned up -- "remove" untracks them,
// "remove_and_delete" deletes their files through Remove first and untracks
// only those it deleted.
type Cleanup struct {
	Config config.CleanupConfig
	// Remove deletes a repo's files under remove_and_delete.
	Remove RemoveFunc
	// Update runs a change to the state under the state lock and saves it.
	Update func(change func(*state.State) error) error
	Now    func() time.Time
}

// Preview marks stale in st, in memory, and counts what Run would clean up.
// It never touches disk or tracking, which is what makes a preview such as
// `discover` without --apply safe.
func (c Cleanup) Preview(st *state.State, discovered map[string]bool, scope StaleScope) CleanupResult {
	var res CleanupResult
	var due []staleRepo
	res.NewlyStale, due = c.mark(st, discovered, scope)
	res.Reapable = len(due)
	return res
}

// Run marks stale and cleans up. Files are deleted outside the state lock,
// so a slow archive holds up no other command; a repo that changed meanwhile
// (moved, found again) is left tracked.
func (c Cleanup) Run(st *state.State, discovered map[string]bool, scope StaleScope) (CleanupResult, error) {
	var res CleanupResult
	var due []staleRepo
	err := c.Update(func(st *state.State) error {
		res.NewlyStale, due = c.mark(st, discovered, scope)
		res.Reapable = len(due)
		if c.Config.Policy == "remove" {
			for _, d := range due {
				delete(st.Repos, d.key)
			}
			res.Reaped = len(due)
		}
		return nil
	})
	if err != nil || c.Config.Policy != "remove_and_delete" || len(due) == 0 {
		return res, err
	}

	failures := make(map[string]error)
	for _, d := range due {
		if err := c.Remove(d.key, &d.repo); err != nil {
			failures[d.key] = err
		}
	}
	err = c.Update(func(st *state.State) error {
		for _, d := range due {
			repo := st.Repos[d.key]
			if repo == nil || repo.LocalPath != d.repo.LocalPath || repo.Status != "stale" {
				continue
			}
			if ferr := failures[d.key]; ferr != nil {
				// Left tracked so the failure stays visible and is retried
				// next run rather than silently orphaning the directory.
				repo.Error = "stale cleanup kept the files: " + ferr.Error()
				res.Failed++
				continue
			}
			delete(st.Repos, d.key)
			res.Reaped++
		}
		return nil
	})
	return res, err
}

type staleRepo struct {
	key  string
	repo state.RepoState
}

// mark updates the stale state of every repo in scope and returns the ones
// due for cleanup: stale past the grace period, under a reaping policy. The
// grace period runs from the persisted StaleSince, so a repo that only just
// went missing is never due.
func (c Cleanup) mark(st *state.State, discovered map[string]bool, scope StaleScope) (newlyStale int, due []staleRepo) {
	now := time.Now()
	if c.Now != nil {
		now = c.Now()
	}
	reaps := c.Config.Policy == "remove" || c.Config.Policy == "remove_and_delete"

	for _, key := range slices.Sorted(maps.Keys(st.Repos)) {
		repo := st.Repos[key]
		if c.Config.ProtectManual && repo.ManuallyAdded {
			continue
		}
		if discovered[key] {
			if repo.Status == "stale" {
				repo.Status = "clean"
			}
			repo.StaleSince = time.Time{}
			repo.LastDiscovered = now
			continue
		}
		if !scope.Covers(repo) {
			continue
		}
		// Not found by any rule that could have found it.
		if repo.StaleSince.IsZero() {
			repo.StaleSince = now
			newlyStale++
		}
		repo.Status = "stale"
		if reaps && now.Sub(repo.StaleSince) > c.Config.GracePeriod {
			due = append(due, staleRepo{key, *repo})
		}
	}
	return newlyStale, due
}
