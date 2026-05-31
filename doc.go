// git-mgr discovers, clones, syncs, and backs up git repositories
// across multiple GitHub, GitLab, and Gitea/Forgejo instances into a structured
// local directory layout.
//
// It is designed to run both interactively (bootstrapping a new machine)
// and as a background cron job (periodic fetch + transparent backup
// of uncommitted work).
//
// Key features:
//   - Multi-provider discovery (GitHub, GitHub Enterprise, GitLab, Gitea/Forgejo)
//   - Templated directory structure (e.g., github.com/owner/repo)
//   - Transparent backup of uncommitted changes (stash refs + patch files)
//   - Cron-safe concurrent sync with flock and lock detection
//   - Per-repo config overrides and tag-based filtering
//   - Stale repo detection with configurable cleanup policy
package main
