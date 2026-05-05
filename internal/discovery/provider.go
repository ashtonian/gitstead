package discovery

import (
	"context"

	"github.com/ashtonian/git-mgr/internal/config"
)

// Repo represents a discovered repository from any provider.
type Repo struct {
	Host          string
	Owner         string
	Name          string
	FullName      string // "owner/name"
	SSHURL        string // "git@host:owner/name.git"
	Description   string
	Language      string
	Topics        []string // repo topics from provider (e.g., GitHub topics)
	Tags          []string // git-mgr tags assigned by discovery rules
	IsFork        bool
	IsArchived    bool
	Visibility    string // "public", "private", "internal"
	DefaultBranch string
	DiscoveredBy  string // name of the discovery rule that found this repo
}

// Key returns the canonical state key for this repo (host/owner/name).
func (r *Repo) Key() string {
	return r.Host + "/" + r.FullName
}

// Provider discovers repos from a hosting service instance.
type Provider interface {
	// Name returns the provider instance name.
	Name() string

	// Discover returns all repos matching the given rule.
	Discover(ctx context.Context, rule config.DiscoveryRule) ([]Repo, error)
}
