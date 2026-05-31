package discovery

import (
	"context"
	"fmt"
	"strings"

	"github.com/ashtonian/git-mgr/internal/config"
)

// GiteaProvider discovers repos from a Gitea or Forgejo instance via its
// GitHub-compatible REST API (served under /api/v1/). It reuses the go-github
// listing core but enriches languages and topics from Gitea's per-repo
// endpoints, since those are not returned inline.
type GiteaProvider struct {
	core *ghCore
}

// NewGiteaProvider creates a Gitea/Forgejo provider from config. The provider
// URL is required; if it lacks an API path, /api/v1/ is appended.
func NewGiteaProvider(cfg config.Provider) (*GiteaProvider, error) {
	if cfg.URL == "" {
		return nil, fmt.Errorf("gitea provider %q requires 'url'", cfg.Name)
	}
	apiURL := cfg.URL
	if !strings.Contains(apiURL, "/api/") {
		apiURL = strings.TrimRight(apiURL, "/") + "/api/v1/"
	}
	client, err := newGHClient(resolveToken(cfg, "GITEA_TOKEN", "FORGEJO_TOKEN"), apiURL)
	if err != nil {
		return nil, err
	}
	return &GiteaProvider{core: &ghCore{
		name:         cfg.Name,
		client:       client,
		host:         hostFromURL(cfg.URL, cfg.Name),
		inlineTopics: false,
	}}, nil
}

func (g *GiteaProvider) Name() string { return g.core.name }

func (g *GiteaProvider) Discover(ctx context.Context, rule config.DiscoveryRule) (ProviderResult, error) {
	repos, applied, err := g.core.discover(ctx, rule)
	if err != nil {
		return ProviderResult{}, err
	}
	return ProviderResult{
		Repos:        repos,
		Applied:      applied,
		Capabilities: Capabilities{CanEnrichLanguages: true, CanEnrichTopics: true},
	}, nil
}

func (g *GiteaProvider) EnrichLanguages(ctx context.Context, repos []Repo) error {
	return g.core.enrichLanguages(ctx, repos)
}

func (g *GiteaProvider) EnrichTopics(ctx context.Context, repos []Repo) error {
	return g.core.enrichTopics(ctx, repos)
}
