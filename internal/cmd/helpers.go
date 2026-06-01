package cmd

import (
	"strings"
	"time"

	"github.com/ashtonian/git-mgr/internal/discovery"
	"github.com/ashtonian/git-mgr/internal/state"
)

// isHTTPURL reports whether u is an http(s) clone URL (vs ssh/scp).
func isHTTPURL(u string) bool {
	return strings.HasPrefix(u, "https://") || strings.HasPrefix(u, "http://")
}

// cloneTarget picks the clone URL and SSH key for a discovered repo given the
// resolved clone_protocol. For https the URL is the provider's HTTP(S) clone URL
// and auth flows through git's own credential helper (no SSH key); for ssh the
// SSH URL and the resolved key are used. Falls back to SSH if no HTTP URL.
func cloneTarget(repo discovery.Repo, protocol, sshKey string) (url, key string) {
	if protocol == "https" && repo.HTTPURL != "" {
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
