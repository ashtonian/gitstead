package discovery

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ashtonian/git-mgr/internal/config"
	"github.com/google/go-github/v88/github"
)

// ghCore is the shared go-github listing + mapping core used by the GitHub
// provider (and, via the legacy shim, GitHub-API-compatible servers).
type ghCore struct {
	name               string
	client             *github.Client
	host               string
	inlineTopics       bool // GitHub returns topics inline
	pushSelfVisibility bool // GitHub's authenticated endpoint honors ?visibility=
}

// newGHClient builds a go-github (v88) client for the given token and base URL.
// A bare host (GHES) uses WithEnterpriseURLs (which appends /api/v3/); a URL that
// already contains an API path is set as the base URL via WithURLs (the legacy
// Gitea/Forgejo shim — new configs should use type: gitea). The client is built
// on our retrying http.Client so GitHub/GHES get the rate-limit resilience the
// GitLab SDK has built in; WithAuthToken layers Bearer auth over that transport.
func newGHClient(token, rawURL string, apiTimeout time.Duration) (*github.Client, error) {
	opts := []github.ClientOptionsFunc{github.WithHTTPClient(newRetryClient(apiTimeout))}
	if token != "" {
		opts = append(opts, github.WithAuthToken(token))
	}
	if rawURL != "" {
		if strings.Contains(rawURL, "/api/") {
			base := rawURL
			if !strings.HasSuffix(base, "/") {
				base += "/"
			}
			opts = append(opts, github.WithURLs(&base, &base))
		} else {
			opts = append(opts, github.WithEnterpriseURLs(rawURL, rawURL))
		}
	}
	client, err := github.NewClient(opts...)
	if err != nil {
		return nil, fmt.Errorf("creating github client: %w", err)
	}
	return client, nil
}

// hostFromURL extracts the host from a provider URL, defaulting to fallback.
// It drops any port, as repourl does: a repo's key is the same whether it was
// discovered, or adopted from a clone over SSH or HTTPS, whose ports differ.
func hostFromURL(rawURL, fallback string) string {
	if rawURL == "" {
		return fallback
	}
	if u, err := url.Parse(rawURL); err == nil && u.Hostname() != "" {
		return u.Hostname()
	}
	return fallback
}

// discover lists repos for the rule's scope and maps them to Repo, reporting any
// filters it applied server-side and why the list may be incomplete.
func (c *ghCore) discover(ctx context.Context, rule config.DiscoveryRule) ([]Repo, FilterSet, []string, error) {
	scope := ruleScope(rule)
	ghRepos, applied, incomplete, err := c.list(ctx, scope, rule)
	if err != nil {
		return nil, nil, nil, err
	}
	repos := make([]Repo, 0, len(ghRepos))
	for _, r := range ghRepos {
		repos = append(repos, c.toRepo(r))
	}
	return repos, applied, incomplete, nil
}

func (c *ghCore) list(ctx context.Context, scope Scope, rule config.DiscoveryRule) ([]*github.Repository, FilterSet, []string, error) {
	applied := FilterSet{}
	switch scope.Kind {
	case ScopeQuery:
		repos, incomplete, err := c.searchRepos(ctx, scope.Value)
		return repos, allFiltersApplied(), incomplete, err
	case ScopeOrg:
		repos, err := c.listByOrg(ctx, scope.Value)
		return repos, applied, nil, err
	case ScopeUser:
		repos, err := c.listByUser(ctx, scope.Value)
		return repos, applied, nil, err
	default: // ScopeSelf
		// GitHub's authenticated-user endpoint supports a server-side visibility
		// filter (all/public/private). Push it when the rule wants public or
		// private; "internal" is not supported there and stays client-side.
		vis := ""
		if c.pushSelfVisibility && (rule.Visibility == "public" || rule.Visibility == "private") {
			vis = rule.Visibility
			applied[FilterVisibility] = true
		}
		repos, err := c.listAuthenticated(ctx, vis, strings.Join(rule.Affiliation, ","))
		return repos, applied, nil, err
	}
}

// searchRepos runs a raw GitHub repository-search query. GitHub caps search
// results at 1000 and stops paginating there; when the reported total exceeds
// what we retrieved (or results are flagged incomplete), it says so.
func (c *ghCore) searchRepos(ctx context.Context, query string) ([]*github.Repository, []string, error) {
	var all []*github.Repository
	opts := &github.SearchOptions{ListOptions: github.ListOptions{PerPage: 100}}
	var total int
	incomplete := false
	for {
		result, resp, err := c.client.Search.Repositories(ctx, query, opts)
		if err != nil {
			return nil, nil, fmt.Errorf("searching repos: %w", err)
		}
		all = append(all, result.Repositories...)
		total = result.GetTotal()
		if result.GetIncompleteResults() {
			incomplete = true
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	var missing []string
	if total > len(all) {
		missing = append(missing, fmt.Sprintf(
			"GitHub search matched %d repos but the API caps results at %d; %d repos were not retrieved",
			total, len(all), total-len(all)))
	} else if incomplete {
		missing = append(missing, "GitHub reported incomplete search results (query timed out); some repos may be missing")
	}
	return all, missing, nil
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

func (c *ghCore) listAuthenticated(ctx context.Context, visibility, affiliation string) ([]*github.Repository, error) {
	var all []*github.Repository
	opts := &github.RepositoryListByAuthenticatedUserOptions{
		Visibility:  visibility,
		Affiliation: affiliation,
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
		ProviderID:    strconv.FormatInt(r.GetID(), 10),
		SSHURL:        r.GetSSHURL(),
		HTTPURL:       r.GetCloneURL(),
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

// enrichLanguages fills Repo.Languages from the per-repo languages endpoint,
// concurrently and best-effort (per-repo failures are joined, not fatal).
func (c *ghCore) enrichLanguages(ctx context.Context, repos []Repo, concurrency int) error {
	return enrichConcurrent(ctx, repos, concurrency, func(ctx context.Context, r *Repo) error {
		langs, _, err := c.client.Repositories.ListLanguages(ctx, r.Owner, r.Name)
		if err != nil {
			return fmt.Errorf("listing languages: %w", err)
		}
		r.Languages = mapKeys(langs)
		return nil
	})
}

// GitHubProvider discovers repos from GitHub or GitHub Enterprise.
type GitHubProvider struct {
	core *ghCore
}

// NewGitHubProvider creates a GitHub provider from config.
func NewGitHubProvider(cfg config.Provider, apiTimeout time.Duration) (*GitHubProvider, error) {
	tok, err := ResolveToken(cfg)
	if err != nil {
		return nil, err
	}
	client, err := newGHClient(tok, cfg.URL, apiTimeout)
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
	repos, applied, incomplete, err := g.core.discover(ctx, rule)
	if err != nil {
		return ProviderResult{}, err
	}
	return ProviderResult{
		Repos:        repos,
		Applied:      applied,
		Capabilities: Capabilities{CanEnrichLanguages: true},
		Incomplete:   incomplete,
	}, nil
}

func (g *GitHubProvider) EnrichLanguages(ctx context.Context, repos []Repo, concurrency int) error {
	return g.core.enrichLanguages(ctx, repos, concurrency)
}

// EnrichTopics is a no-op for GitHub; topics are returned inline.
func (g *GitHubProvider) EnrichTopics(ctx context.Context, repos []Repo, concurrency int) error {
	return nil
}
