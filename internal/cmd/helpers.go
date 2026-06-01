package cmd

import (
	"time"

	"github.com/ashtonian/git-mgr/internal/discovery"
	"github.com/ashtonian/git-mgr/internal/state"
)

// addRepoToState adds a discovered repo to the state.
func addRepoToState(st *state.State, repo discovery.Repo, localPath string) {
	now := time.Now()
	st.Repos[repo.Key()] = &state.RepoState{
		CloneURL:       repo.SSHURL,
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
