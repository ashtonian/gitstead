package discovery

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/ashtonian/git-mgr/internal/config"
	"github.com/google/go-github/v68/github"
	"golang.org/x/oauth2"
)

// ghCore is the shared go-github listing + mapping core used by both the GitHub
// and Gitea/Forgejo providers, since Gitea exposes a GitHub-compatible REST API.
type ghCore struct {
	name               string
	client             *github.Client
	host               string
	inlineTopics       bool // GitHub returns topics inline; Gitea must enrich them
	pushSelfVisibility bool // GitHub's authenticated endpoint honors ?visibility=; Gitea may not
}

// newGHClient builds a go-github client for the given token and base URL.
// A URL containing an API path (e.g. /api/v1/ for Gitea/Forgejo) sets BaseURL
// directly; a bare host uses WithEnterpriseURLs (which appends /api/v3/).
func newGHClient(token, rawURL string) (*github.Client, error) {
	var client *github.Client
	if token != "" {
		ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
		client = github.NewClient(oauth2.NewClient(context.Background(), ts))
	} else {
		client = github.NewClient(nil)
	}
	if rawURL == "" {
		return client, nil
	}
	if strings.Contains(rawURL, "/api/") {
		u, err := url.Parse(rawURL)
		if err != nil {
			return nil, fmt.Errorf("parsing API URL: %w", err)
		}
		if !strings.HasSuffix(u.Path, "/") {
			u.Path += "/"
		}
		client.BaseURL = u
		return client, nil
	}
	client, err := client.WithEnterpriseURLs(rawURL, rawURL)
	if err != nil {
		return nil, fmt.Errorf("configuring enterprise URL: %w", err)
	}
	return client, nil
}

// hostFromURL extracts the host from a provider URL, defaulting to fallback.
func hostFromURL(rawURL, fallback string) string {
	if rawURL == "" {
		return fallback
	}
	if u, err := url.Parse(rawURL); err == nil && u.Host != "" {
		return u.Host
	}
	return fallback
}

// discover lists repos for the rule's scope and maps them to Repo, reporting any
// filters it applied server-side.
func (c *ghCore) discover(ctx context.Context, rule config.DiscoveryRule) ([]Repo, FilterSet, error) {
	scope := ruleScope(rule)
	ghRepos, applied, err := c.list(ctx, scope, rule)
	if err != nil {
		return nil, nil, err
	}
	repos := make([]Repo, 0, len(ghRepos))
	for _, r := range ghRepos {
		repos = append(repos, c.toRepo(r))
	}
	return repos, applied, nil
}

func (c *ghCore) list(ctx context.Context, scope Scope, rule config.DiscoveryRule) ([]*github.Repository, FilterSet, error) {
	applied := FilterSet{}
	switch scope.Kind {
	case ScopeOrg:
		repos, err := c.listByOrg(ctx, scope.Value)
		return repos, applied, err
	case ScopeUser:
		repos, err := c.listByUser(ctx, scope.Value)
		return repos, applied, err
	default: // ScopeSelf
		// GitHub's authenticated-user endpoint supports a server-side visibility
		// filter (all/public/private). Push it when the rule wants public or
		// private; "internal" is not supported there and stays client-side.
		vis := ""
		if c.pushSelfVisibility && (rule.Visibility == "public" || rule.Visibility == "private") {
			vis = rule.Visibility
			applied[FilterVisibility] = true
		}
		repos, err := c.listAuthenticated(ctx, vis)
		return repos, applied, err
	}
}

func (c *ghCore) listByOrg(ctx context.Context, org string) ([]*github.Repository, error) {
	var all []*github.Repository
	opts := &github.RepositoryListByOrgOptions{ListOptions: github.ListOptions{PerPage: 100}}
	for {
		repos, resp, err := c.client.Repositories.ListByOrg(ctx, org, opts)
		if err != nil {
			return nil, fmt.Errorf("listing repos for org %s: %w", org, err)
		}
		all = append(all, repos...)
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return all, nil
}

func (c *ghCore) listByUser(ctx context.Context, user string) ([]*github.Repository, error) {
	var all []*github.Repository
	opts := &github.RepositoryListByUserOptions{ListOptions: github.ListOptions{PerPage: 100}}
	for {
		repos, resp, err := c.client.Repositories.ListByUser(ctx, user, opts)
		if err != nil {
			return nil, fmt.Errorf("listing repos for user %s: %w", user, err)
		}
		all = append(all, repos...)
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return all, nil
}

func (c *ghCore) listAuthenticated(ctx context.Context, visibility string) ([]*github.Repository, error) {
	var all []*github.Repository
	opts := &github.RepositoryListByAuthenticatedUserOptions{
		Visibility:  visibility,
		ListOptions: github.ListOptions{PerPage: 100},
	}
	for {
		repos, resp, err := c.client.Repositories.ListByAuthenticatedUser(ctx, opts)
		if err != nil {
			return nil, fmt.Errorf("listing repos for authenticated user: %w", err)
		}
		all = append(all, repos...)
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return all, nil
}

func (c *ghCore) toRepo(r *github.Repository) Repo {
	owner := r.GetOwner().GetLogin()
	name := r.GetName()

	// Prefer the real visibility field (public/private/internal); fall back to
	// the private bool for servers that don't return it.
	visibility := r.GetVisibility()
	if visibility == "" {
		if r.GetPrivate() {
			visibility = "private"
		} else {
			visibility = "public"
		}
	}

	repo := Repo{
		Host:          c.host,
		Owner:         owner,
		Name:          name,
		FullName:      owner + "/" + name,
		SSHURL:        r.GetSSHURL(),
		Description:   r.GetDescription(),
		Language:      r.GetLanguage(),
		IsFork:        r.GetFork(),
		IsArchived:    r.GetArchived(),
		Visibility:    visibility,
		DefaultBranch: r.GetDefaultBranch(),
	}
	if c.inlineTopics {
		repo.Topics = r.Topics
	}
	return repo
}

// enrichLanguages fills Repo.Languages from the per-repo languages endpoint.
func (c *ghCore) enrichLanguages(ctx context.Context, repos []Repo) error {
	for i := range repos {
		langs, _, err := c.client.Repositories.ListLanguages(ctx, repos[i].Owner, repos[i].Name)
		if err != nil {
			return fmt.Errorf("listing languages for %s: %w", repos[i].FullName, err)
		}
		ls := make([]string, 0, len(langs))
		for l := range langs {
			ls = append(ls, l)
		}
		repos[i].Languages = ls
	}
	return nil
}

// enrichTopics fills Repo.Topics from the per-repo topics endpoint.
func (c *ghCore) enrichTopics(ctx context.Context, repos []Repo) error {
	for i := range repos {
		topics, _, err := c.client.Repositories.ListAllTopics(ctx, repos[i].Owner, repos[i].Name)
		if err != nil {
			return fmt.Errorf("listing topics for %s: %w", repos[i].FullName, err)
		}
		repos[i].Topics = topics
	}
	return nil
}

// GitHubProvider discovers repos from GitHub or GitHub Enterprise.
type GitHubProvider struct {
	core *ghCore
}

// NewGitHubProvider creates a GitHub provider from config.
func NewGitHubProvider(cfg config.Provider) (*GitHubProvider, error) {
	client, err := newGHClient(resolveToken(cfg, "GITHUB_TOKEN"), cfg.URL)
	if err != nil {
		return nil, err
	}
	return &GitHubProvider{core: &ghCore{
		name:               cfg.Name,
		client:             client,
		host:               hostFromURL(cfg.URL, "github.com"),
		inlineTopics:       true,
		pushSelfVisibility: true,
	}}, nil
}

func (g *GitHubProvider) Name() string { return g.core.name }

func (g *GitHubProvider) Discover(ctx context.Context, rule config.DiscoveryRule) (ProviderResult, error) {
	repos, applied, err := g.core.discover(ctx, rule)
	if err != nil {
		return ProviderResult{}, err
	}
	return ProviderResult{
		Repos:        repos,
		Applied:      applied,
		Capabilities: Capabilities{CanEnrichLanguages: true},
	}, nil
}

func (g *GitHubProvider) EnrichLanguages(ctx context.Context, repos []Repo) error {
	return g.core.enrichLanguages(ctx, repos)
}

// EnrichTopics is a no-op for GitHub; topics are returned inline.
func (g *GitHubProvider) EnrichTopics(ctx context.Context, repos []Repo) error { return nil }
