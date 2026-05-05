package discovery

import (
	"os"
	"time"

	"github.com/ashtonian/git-mgr/internal/config"
	"github.com/ashtonian/git-mgr/internal/state"
)

// DetectStale marks repos as stale if they're no longer found by any discovery rule.
// Returns the number of newly stale repos.
func DetectStale(st *state.State, discovered map[string]bool, cleanup config.CleanupConfig) int {
	newlyStale := 0
	now := time.Now()

	for key, repo := range st.Repos {
		// Never mark manually-added repos as stale
		if cleanup.ProtectManual && repo.ManuallyAdded {
			continue
		}

		if discovered[key] {
			// Found — clear stale state
			if repo.Status == "stale" {
				repo.Status = "clean"
			}
			repo.StaleSince = time.Time{}
			repo.LastDiscovered = now
		} else {
			// Not found
			if repo.StaleSince.IsZero() {
				repo.StaleSince = now
				newlyStale++
			}
			repo.Status = "stale"

			// Apply cleanup policy if grace period has elapsed
			if cleanup.Policy == "remove" || cleanup.Policy == "remove_and_delete" {
				if now.Sub(repo.StaleSince) > cleanup.GracePeriod {
					if cleanup.Policy == "remove_and_delete" {
						os.RemoveAll(repo.LocalPath)
					}
					delete(st.Repos, key)
				}
			}
		}
	}
	return newlyStale
}
