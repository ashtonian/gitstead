package discovery

import (
	"time"

	"github.com/ashtonian/git-mgr/internal/config"
	"github.com/ashtonian/git-mgr/internal/pathutil"
	"github.com/ashtonian/git-mgr/internal/state"
)

// CleanupResult summarizes a DetectStale pass.
type CleanupResult struct {
	NewlyStale int // repos that transitioned to stale on this pass
	Reapable   int // stale repos past the grace period eligible for cleanup
	Reaped     int // repos removed from tracking (apply only)
	Failed     int // repos whose on-disk deletion failed (apply only); left tracked
}

// DetectStale marks repos that are no longer found by any discovery rule as
// stale. When apply is true it also enforces cleanup.Policy on stale repos past
// the grace period: "remove" untracks them, "remove_and_delete" additionally
// deletes the working tree via the shared pathutil.SafeRemoveAll guard.
//
// When apply is false it is strictly non-destructive: it marks repos stale
// in-memory (the caller decides whether to persist) and only *counts* what would
// be reaped (Reapable), never touching disk or tracking. This is what makes a
// preview such as `discover` without --apply safe -- enforcement happens only on
// the explicit, lock-held apply paths (`discover --apply`, scheduled `sync`).
func DetectStale(st *state.State, discovered map[string]bool, cleanup config.CleanupConfig, apply bool) CleanupResult {
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

		// Not found by any rule.
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
			if err := pathutil.SafeRemoveAll(repo.LocalPath); err != nil {
				// Leave it tracked so the failure stays visible and is retried
				// next run rather than silently orphaning the directory.
				res.Failed++
				continue
			}
		}
		delete(st.Repos, key)
		res.Reaped++
	}
	return res
}
