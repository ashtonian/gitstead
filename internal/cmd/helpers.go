package cmd

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/ashtonian/git-mgr/internal/config"
	"github.com/ashtonian/git-mgr/internal/discovery"
	"github.com/ashtonian/git-mgr/internal/gitops"
	"github.com/ashtonian/git-mgr/internal/oplog"
	"github.com/ashtonian/git-mgr/internal/state"
)

// isHTTPURL reports whether u is an http(s) clone URL (vs ssh/scp).
func isHTTPURL(u string) bool {
	return strings.HasPrefix(u, "https://") || strings.HasPrefix(u, "http://")
}

// eventsPath is the operation-log file, kept next to the state file so it
// follows GIT_MGR_STATE / --state.
func eventsPath(ctx *Context) string {
	return filepath.Join(filepath.Dir(config.ExpandHome(ctx.State)), "events.jsonl")
}

// logEvent appends a best-effort entry to the operation log (never fails the
// caller). Honors log.events: false as an off-switch.
func logEvent(ctx *Context, action, repo, detail string) {
	if cfg, err := ctx.LoadConfig(); err == nil && !cfg.Log.Events {
		return
	}
	_ = oplog.Append(eventsPath(ctx), oplog.Event{Time: time.Now(), Action: action, Repo: repo, Detail: detail})
}

// cloneTarget picks the clone URL and SSH key for a discovered repo given the
// resolved clone_protocol. For https the URL is the provider's HTTP(S) clone URL
// and auth flows through git's own credential helper (no SSH key); for ssh the
// SSH URL and the resolved key are used. Falls back to SSH if no HTTP URL.
func cloneTarget(repo discovery.Repo, protocol, sshKey string) (url, key string) {
	if protocol == "https" && repo.HTTPURL != "" {
		return repo.HTTPURL, ""
	}
	if repo.SSHURL != "" {
		return repo.SSHURL, sshKey
	}
	// Single-URL repos (e.g. a manual `repos:` entry given as https) have only
	// one of the two set -- honor whatever we have rather than returning empty.
	if repo.HTTPURL != "" {
		return repo.HTTPURL, ""
	}
	return repo.SSHURL, sshKey
}

// addRepoToState adds a discovered repo to the state. cloneURL is the URL it was
// actually cloned from (ssh or https), so later operations use the same transport.
func addRepoToState(st *state.State, repo discovery.Repo, localPath, cloneURL string) {
	now := time.Now()
	st.Repos[repo.Key()] = &state.RepoState{
		CloneURL:       cloneURL,
		Host:           repo.Host,
		Owner:          repo.Owner,
		Name:           repo.Name,
		FullName:       repo.FullName,
		Provider:       repo.Host,
		LocalPath:      localPath,
		Cloned:         true,
		DefaultBranch:  repo.DefaultBranch,
		DiscoveredBy:   repo.DiscoveredBy,
		IsFork:         repo.IsFork,
		IsArchived:     repo.IsArchived,
		Language:       repo.Language,
		Languages:      repo.Languages,
		Topics:         repo.Topics,
		Tags:           repo.Tags,
		Description:    repo.Description,
		Status:         "clean",
		FirstSeen:      now,
		LastDiscovered: now,
	}
}

// archivingRemover deletes stale clones through gitops.RemoveClone: unique work
// is bundled into archiveDir first, and a clone that can't be inspected or
// archived is kept.
func archivingRemover(ctx context.Context, log *slog.Logger, archiveDir string) discovery.RemoveFunc {
	return func(key string, repo *state.RepoState) error {
		archived, err := gitops.RemoveClone(ctx, repo.LocalPath, archiveDir, key, false)
		if err != nil {
			log.Warn("stale repo kept", "repo", key, "path", repo.LocalPath, "error", err)
			return err
		}
		if archived != "" {
			log.Info("archived stale repo's unique work before deleting it", "repo", key, "archive", archived)
		}
		return nil
	}
}
