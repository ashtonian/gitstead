package discovery

import (
	"context"
	"fmt"
	"strings"

	"github.com/ashtonian/git-mgr/internal/config"
	gitlab "gitlab.com/gitlab-org/api/client-go"
)

// GitLabProvider discovers repos from GitLab or self-hosted GitLab.
type GitLabProvider struct {
	cfg    config.Provider
	client *gitlab.Client
	host   string
}

// NewGitLabProvider creates a GitLab provider from config.
func NewGitLabProvider(cfg config.Provider) (*GitLabProvider, error) {
	baseURL := cfg.URL
	if baseURL == "" {
		baseURL = "https://gitlab.com"
	}
	client, err := gitlab.NewClient(resolveToken(cfg, "GITLAB_TOKEN"), gitlab.WithBaseURL(baseURL))
	if err != nil {
		return nil, fmt.Errorf("creating GitLab client: %w", err)
	}
	return &GitLabProvider{cfg: cfg, client: client, host: hostFromURL(baseURL, "gitlab.com")}, nil
}

func (g *GitLabProvider) Name() string { return g.cfg.Name }

func (g *GitLabProvider) Discover(ctx context.Context, rule config.DiscoveryRule) (ProviderResult, error) {
	scope := ruleScope(rule)
	applied := FilterSet{}
	var repos []Repo
	var err error
	switch scope.Kind {
	case ScopeQuery:
		repos, err = g.search(ctx, scope.Value)
		applied = allFiltersApplied()
	case ScopeGroup:
		repos, err = g.listByGroup(ctx, scope.Value, rule, applied)
	case ScopeUser:
		repos, err = g.listByUser(ctx, scope.Value, rule, applied)
	default: // ScopeSelf — everything the token is a member of.
		repos, err = g.listMembership(ctx, rule, applied)
	}
	if err != nil {
		return ProviderResult{}, err
	}
	return ProviderResult{
		Repos:        repos,
		Applied:      applied,
		Capabilities: Capabilities{CanEnrichLanguages: true},
	}, nil
}

// glVisibility maps a rule visibility to a GitLab visibility pointer, or nil.
func glVisibility(v string) *gitlab.VisibilityValue {
	switch v {
	case "public":
		return gitlab.Ptr(gitlab.PublicVisibility)
	case "internal":
		return gitlab.Ptr(gitlab.InternalVisibility)
	case "private":
		return gitlab.Ptr(gitlab.PrivateVisibility)
	}
	return nil
}

// commonProjectFilters sets the project-list filters supported by both the
// /projects and group endpoints, recording them in applied. Topics are pushed
// one value server-side to narrow results; only marked applied when a single
// topic is requested (multiple require a client-side ALL-of check on the inline
// topics list).
func commonProjectFilters(rule config.DiscoveryRule, applied FilterSet) (vis *gitlab.VisibilityValue, archived *bool, topic *string) {
	if v := glVisibility(rule.Visibility); v != nil {
		vis = v
		applied[FilterVisibility] = true
	}
	if !rule.IncludeArchived {
		archived = gitlab.Ptr(false)
		applied[FilterArchived] = true
	}
	if len(rule.Topics) > 0 {
		// GitLab's topic param is a comma-separated AND list, so all requested
		// topics can be pushed server-side.
		topic = gitlab.Ptr(strings.Join(rule.Topics, ","))
		applied[FilterTopics] = true
	}
	return vis, archived, topic
}

func (g *GitLabProvider) listByGroup(ctx context.Context, group string, rule config.DiscoveryRule, applied FilterSet) ([]Repo, error) {
	vis, archived, topic := commonProjectFilters(rule, applied)
	opts := &gitlab.ListGroupProjectsOptions{
		ListOptions:      gitlab.ListOptions{PerPage: 100},
		IncludeSubGroups: gitlab.Ptr(true),
		Visibility:       vis,
		Archived:         archived,
		Topic:            topic,
	}
	// The group projects endpoint has no programming-language filter; language
	// is handled via enrichment (CanEnrichLanguages).
	var all []Repo
	for {
		projects, resp, err := g.client.Groups.ListGroupProjects(group, opts, gitlab.WithContext(ctx))
		if err != nil {
			return nil, fmt.Errorf("listing group %s projects: %w", group, err)
		}
		for _, p := range projects {
			all = append(all, g.toRepo(p))
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return all, nil
}

func (g *GitLabProvider) listByUser(ctx context.Context, user string, rule config.DiscoveryRule, applied FilterSet) ([]Repo, error) {
	vis, archived, topic := commonProjectFilters(rule, applied)
	opts := &gitlab.ListProjectsOptions{
		ListOptions: gitlab.ListOptions{PerPage: 100},
		Visibility:  vis,
		Archived:    archived,
		Topic:       topic,
	}
	// /users/:user/projects honors with_programming_language; push a single
	// requested language server-side (multiple need client-side any-match).
	if len(rule.Languages) == 1 {
		opts.WithProgrammingLanguage = gitlab.Ptr(rule.Languages[0])
		applied[FilterLanguage] = true
	}
	var all []Repo
	for {
		projects, resp, err := g.client.Projects.ListUserProjects(user, opts, gitlab.WithContext(ctx))
		if err != nil {
			return nil, fmt.Errorf("listing user %s projects: %w", user, err)
		}
		for _, p := range projects {
			all = append(all, g.toRepo(p))
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return all, nil
}

func (g *GitLabProvider) listMembership(ctx context.Context, rule config.DiscoveryRule, applied FilterSet) ([]Repo, error) {
	vis, archived, topic := commonProjectFilters(rule, applied)
	opts := &gitlab.ListProjectsOptions{
		ListOptions: gitlab.ListOptions{PerPage: 100},
		Membership:  gitlab.Ptr(true),
		Visibility:  vis,
		Archived:    archived,
		Topic:       topic,
	}
	// The /projects endpoint honors with_programming_language; push a single
	// requested language server-side (multiple require client-side any-match).
	if len(rule.Languages) == 1 {
		opts.WithProgrammingLanguage = gitlab.Ptr(rule.Languages[0])
		applied[FilterLanguage] = true
	}
	var all []Repo
	for {
		projects, resp, err := g.client.Projects.ListProjects(opts, gitlab.WithContext(ctx))
		if err != nil {
			return nil, fmt.Errorf("listing membership projects: %w", err)
		}
		for _, p := range projects {
			all = append(all, g.toRepo(p))
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return all, nil
}

func (g *GitLabProvider) toRepo(p *gitlab.Project) Repo {
	owner := ""
	if p.Namespace != nil {
		owner = p.Namespace.FullPath
	}

	visibility := "private"
	if p.Visibility == gitlab.PublicVisibility {
		visibility = "public"
	} else if p.Visibility == gitlab.InternalVisibility {
		visibility = "internal"
	}

	return Repo{
		Host:          g.host,
		Owner:         owner,
		Name:          p.Path,
		FullName:      owner + "/" + p.Path,
		SSHURL:        p.SSHURLToRepo,
		HTTPURL:       p.HTTPURLToRepo,
		Description:   p.Description,
		Language:      "", // GitLab doesn't expose a primary language in the list
		Topics:        p.Topics,
		IsFork:        p.ForkedFromProject != nil,
		IsArchived:    p.Archived,
		Visibility:    visibility,
		DefaultBranch: p.DefaultBranch,
	}
}

// search runs a raw GitLab project search across everything the token can see.
func (g *GitLabProvider) search(ctx context.Context, query string) ([]Repo, error) {
	var all []Repo
	opts := &gitlab.SearchOptions{ListOptions: gitlab.ListOptions{PerPage: 100}}
	for {
		projects, resp, err := g.client.Search.Projects(query, opts, gitlab.WithContext(ctx))
		if err != nil {
			return nil, fmt.Errorf("searching projects: %w", err)
		}
		for _, p := range projects {
			all = append(all, g.toRepo(p))
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return all, nil
}

// EnrichLanguages fills Repo.Languages via the per-project languages endpoint,
// concurrently and best-effort.
func (g *GitLabProvider) EnrichLanguages(ctx context.Context, repos []Repo, concurrency int) error {
	return enrichConcurrent(ctx, repos, concurrency, func(ctx context.Context, r *Repo) error {
		langs, _, err := g.client.Projects.GetProjectLanguages(r.FullName, gitlab.WithContext(ctx))
		if err != nil {
			return fmt.Errorf("listing languages: %w", err)
		}
		r.Languages = mapKeys(*langs)
		return nil
	})
}

// EnrichTopics is a no-op for GitLab; topics are returned inline.
func (g *GitLabProvider) EnrichTopics(ctx context.Context, repos []Repo, concurrency int) error {
	return nil
}
