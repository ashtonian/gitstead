package discovery

import (
	"time"

	"github.com/ashtonian/git-mgr/internal/config"
	"github.com/ashtonian/git-mgr/internal/state"
)

// CleanupResult summarizes a DetectStale pass.
type CleanupResult struct {
	NewlyStale int // repos that transitioned to stale on this pass
	Reapable   int // stale repos past the grace period eligible for cleanup
	Reaped     int // repos removed from tracking (apply only)
	Failed     int // repos whose on-disk deletion failed (apply only); left tracked
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
// rule and tag filters. An unfiltered pass in which every rule succeeded covers
// every tracked repo, including repos whose rule has since been removed from
// config. Otherwise it covers only repos first found by a rule that ran and
// succeeded, so `sync --tag personal` cannot judge work repos, and a rule that
// failed judges nothing.
func StaleScopeOf(results []DiscoverResult, ruleFilter, tagFilter string) StaleScope {
	if ruleFilter == "" && tagFilter == "" && !AnyFailed(results) {
		return StaleScope{all: true}
	}
	rules := make(map[string]bool, len(results))
	for _, r := range results {
		if r.Err == nil {
			rules[r.Rule] = true
		}
	}
	return StaleScope{rules: rules}
}

// Covers reports whether the pass may mark repo stale.
func (s StaleScope) Covers(repo *state.RepoState) bool {
	return s.all || s.rules[repo.DiscoveredBy]
}

// DetectStale marks repos within scope that this pass no longer found as stale.
// Repos outside scope are left untouched. When apply is true it also enforces
// cleanup.Policy on stale repos past the grace period: "remove" untracks them,
// "remove_and_delete" deletes their files through remove first and untracks
// only those it deleted.
//
// When apply is false it is strictly non-destructive: it marks repos stale
// in-memory (the caller decides whether to persist) and only *counts* what would
// be reaped (Reapable), never touching disk or tracking. This is what makes a
// preview such as `discover` without --apply safe -- enforcement happens only on
// the explicit, lock-held apply paths (`discover --apply`, scheduled `sync`).
func DetectStale(st *state.State, discovered map[string]bool, scope StaleScope, cleanup config.CleanupConfig, apply bool, remove RemoveFunc) CleanupResult {
	var res CleanupResult
	now := time.Now()
	reaps := cleanup.Policy == "remove" || cleanup.Policy == "remove_and_delete"

	for key, repo := range st.Repos {
		// Never mark manually-added repos as stale.
		if cleanup.ProtectManual && repo.ManuallyAdded {
			continue
		}

		if discovered[key] {
			// Found -- clear any stale state.
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
			res.NewlyStale++
		}
		repo.Status = "stale"

		// Cleanup only applies once the grace period (measured from the first
		// time the repo went missing) has elapsed under a reaping policy.
		if !reaps || now.Sub(repo.StaleSince) <= cleanup.GracePeriod {
			continue
		}
		res.Reapable++
		if !apply {
			// Preview: report what would be reaped, but never mutate disk or
			// tracking. The grace check above used the persisted StaleSince, so
			// repos that only just went stale this pass are correctly excluded.
			continue
		}

		if cleanup.Policy == "remove_and_delete" {
			if err := remove(key, repo); err != nil {
				// Leave it tracked so the failure stays visible and is retried
				// next run rather than silently orphaning the directory.
				repo.Error = "stale cleanup kept the files: " + err.Error()
				res.Failed++
				continue
			}
		}
		delete(st.Repos, key)
		res.Reaped++
	}
	return res
}
