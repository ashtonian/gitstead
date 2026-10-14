package discovery

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"code.gitea.io/sdk/gitea"
	"github.com/ashtonian/gitten/internal/config"
)

// giteaPageSize is the per-page count for Gitea/Forgejo list endpoints.
const giteaPageSize = 50

// GiteaProvider discovers repos from a Gitea or Forgejo instance using the
// official code.gitea.io/sdk/gitea client. Unlike the GitHub providers it does
// not ride the go-github shim, so response shapes (topics, languages, the
// internal-visibility flag) map natively without hand-rolled requests.
type GiteaProvider struct {
	client *gitea.Client
	name   string
	host   string
}

// NewGiteaProvider creates a Gitea/Forgejo provider. The SDK targets the
// instance base URL and appends /api/v1 itself, so a URL copied from the
// GitHub-shim style (".../api/v1/") is normalized back to the base.
func NewGiteaProvider(cfg config.Provider, apiTimeout time.Duration) (*GiteaProvider, error) {
	if cfg.URL == "" {
		return nil, fmt.Errorf("gitea provider %q requires 'url'", cfg.Name)
	}
	base := strings.TrimRight(cfg.URL, "/")
	base = strings.TrimSuffix(base, "/api/v1")
	base = strings.TrimRight(base, "/")

	opts := []gitea.ClientOption{gitea.SetHTTPClient(newRetryClient(apiTimeout))}
	tok, err := ResolveToken(cfg)
	if err != nil {
		return nil, err
	}
	if tok != "" {
		opts = append(opts, gitea.SetToken(tok))
	}
	client, err := gitea.NewClient(base, opts...)
	if err != nil {
		return nil, fmt.Errorf("creating gitea client: %w", err)
	}
	return &GiteaProvider{client: client, name: cfg.Name, host: hostFromURL(cfg.URL, cfg.Name)}, nil
}

func (g *GiteaProvider) Name() string { return g.name }

func (g *GiteaProvider) Discover(ctx context.Context, rule config.DiscoveryRule) (ProviderResult, error) {
	g.client.SetContext(ctx)
	scope := ruleScope(rule)

	var gRepos []*gitea.Repository
	var err error
	applied := FilterSet{}
	switch scope.Kind {
	case ScopeQuery:
		gRepos, err = g.search(scope.Value)
		applied = allFiltersApplied()
	case ScopeOrg:
		gRepos, err = g.listOrg(scope.Value)
	case ScopeUser:
		gRepos, err = g.listUser(scope.Value)
	default: // ScopeSelf
		gRepos, err = g.listSelf()
	}
	if err != nil {
		return ProviderResult{}, err
	}

	repos := make([]Repo, 0, len(gRepos))
	for _, r := range gRepos {
		repos = append(repos, g.toRepo(r))
	}
	return ProviderResult{
		Repos:        repos,
		Applied:      applied,
		Capabilities: Capabilities{CanEnrichLanguages: true, CanEnrichTopics: true},
	}, nil
}

func (g *GiteaProvider) listOrg(org string) ([]*gitea.Repository, error) {
	var all []*gitea.Repository
	for page := 1; ; page++ {
		repos, _, err := g.client.ListOrgRepos(org, gitea.ListOrgReposOptions{
			ListOptions: gitea.ListOptions{Page: page, PageSize: giteaPageSize},
		})
		if err != nil {
			return nil, fmt.Errorf("listing repos for org %s: %w", org, err)
		}
		if len(repos) == 0 {
			break
		}
		all = append(all, repos...)
	}
	return all, nil
}

func (g *GiteaProvider) listUser(user string) ([]*gitea.Repository, error) {
	var all []*gitea.Repository
	for page := 1; ; page++ {
		repos, _, err := g.client.ListUserRepos(user, gitea.ListReposOptions{
			ListOptions: gitea.ListOptions{Page: page, PageSize: giteaPageSize},
		})
		if err != nil {
			return nil, fmt.Errorf("listing repos for user %s: %w", user, err)
		}
		if len(repos) == 0 {
			break
		}
		all = append(all, repos...)
	}
	return all, nil
}

func (g *GiteaProvider) listSelf() ([]*gitea.Repository, error) {
	var all []*gitea.Repository
	for page := 1; ; page++ {
		repos, _, err := g.client.ListMyRepos(gitea.ListReposOptions{
			ListOptions: gitea.ListOptions{Page: page, PageSize: giteaPageSize},
		})
		if err != nil {
			return nil, fmt.Errorf("listing repos for authenticated user: %w", err)
		}
		if len(repos) == 0 {
			break
		}
		all = append(all, repos...)
	}
	return all, nil
}

// search runs a raw keyword search against the instance's /repos/search.
func (g *GiteaProvider) search(query string) ([]*gitea.Repository, error) {
	var all []*gitea.Repository
	for page := 1; ; page++ {
		repos, _, err := g.client.SearchRepos(gitea.SearchRepoOptions{
			ListOptions: gitea.ListOptions{Page: page, PageSize: giteaPageSize},
			Keyword:     query,
		})
		if err != nil {
			return nil, fmt.Errorf("searching repos: %w", err)
		}
		if len(repos) == 0 {
			break
		}
		all = append(all, repos...)
	}
	return all, nil
}

func (g *GiteaProvider) toRepo(r *gitea.Repository) Repo {
	owner := ""
	if r.Owner != nil {
		owner = r.Owner.UserName
	}
	visibility := "public"
	switch {
	case r.Private:
		visibility = "private"
	case r.Internal:
		visibility = "internal"
	}
	return Repo{
		Host:          g.host,
		Owner:         owner,
		Name:          r.Name,
		FullName:      owner + "/" + r.Name,
		ProviderID:    strconv.FormatInt(r.ID, 10),
		SSHURL:        r.SSHURL,
		HTTPURL:       r.CloneURL,
		Description:   r.Description,
		Language:      r.Language,
		IsFork:        r.Fork,
		IsArchived:    r.Archived,
		Visibility:    visibility,
		DefaultBranch: r.DefaultBranch,
	}
}

// EnrichLanguages fills Repo.Languages from Gitea's per-repo languages endpoint.
func (g *GiteaProvider) EnrichLanguages(ctx context.Context, repos []Repo, concurrency int) error {
	g.client.SetContext(ctx)
	return enrichConcurrent(ctx, repos, concurrency, func(ctx context.Context, r *Repo) error {
		langs, _, err := g.client.GetRepoLanguages(r.Owner, r.Name)
		if err != nil {
			return fmt.Errorf("listing languages: %w", err)
		}
		r.Languages = mapKeys(langs)
		return nil
	})
}

// EnrichTopics fills Repo.Topics from Gitea's per-repo topics endpoint.
func (g *GiteaProvider) EnrichTopics(ctx context.Context, repos []Repo, concurrency int) error {
	g.client.SetContext(ctx)
	return enrichConcurrent(ctx, repos, concurrency, func(ctx context.Context, r *Repo) error {
		topics, _, err := g.client.ListRepoTopics(r.Owner, r.Name, gitea.ListRepoTopicsOptions{})
		if err != nil {
			return fmt.Errorf("listing topics: %w", err)
		}
		r.Topics = topics
		return nil
	})
}
