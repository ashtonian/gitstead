package discovery

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
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
	token := resolveGitLabToken(cfg.Token, cfg.TokenEnv, cfg.TokenCommand)

	baseURL := cfg.URL
	if baseURL == "" {
		baseURL = "https://gitlab.com"
	}

	var opts []gitlab.ClientOptionFunc
	opts = append(opts, gitlab.WithBaseURL(baseURL))

	client, err := gitlab.NewClient(token, opts...)
	if err != nil {
		return nil, fmt.Errorf("creating GitLab client: %w", err)
	}

	host := "gitlab.com"
	u, err := url.Parse(baseURL)
	if err == nil && u.Host != "" {
		host = u.Host
	}

	return &GitLabProvider{cfg: cfg, client: client, host: host}, nil
}

func (g *GitLabProvider) Name() string { return g.cfg.Name }

func (g *GitLabProvider) Discover(ctx context.Context, rule config.DiscoveryRule) ([]Repo, error) {
	if rule.Group != "" {
		return g.listByGroup(ctx, rule.Group)
	}
	if rule.User != "" {
		return g.listByUser(ctx, rule.User)
	}
	return g.listOwned(ctx)
}

func (g *GitLabProvider) listByGroup(ctx context.Context, group string) ([]Repo, error) {
	var all []Repo
	includeSubgroups := true
	opts := &gitlab.ListGroupProjectsOptions{
		ListOptions:      gitlab.ListOptions{PerPage: 100},
		IncludeSubGroups: &includeSubgroups,
	}
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

func (g *GitLabProvider) listByUser(ctx context.Context, user string) ([]Repo, error) {
	var all []Repo
	opts := &gitlab.ListProjectsOptions{ListOptions: gitlab.ListOptions{PerPage: 100}}
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

func (g *GitLabProvider) listOwned(ctx context.Context) ([]Repo, error) {
	var all []Repo
	owned := true
	opts := &gitlab.ListProjectsOptions{
		ListOptions: gitlab.ListOptions{PerPage: 100},
		Owned:       &owned,
	}
	for {
		projects, resp, err := g.client.Projects.ListProjects(opts, gitlab.WithContext(ctx))
		if err != nil {
			return nil, fmt.Errorf("listing owned projects: %w", err)
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

	defaultBranch := ""
	if p.DefaultBranch != "" {
		defaultBranch = p.DefaultBranch
	}

	return Repo{
		Host:          g.host,
		Owner:         owner,
		Name:          p.Path,
		FullName:      owner + "/" + p.Path,
		SSHURL:        p.SSHURLToRepo,
		Description:   p.Description,
		Language:      "",   // GitLab doesn't expose primary language in list
		Topics:        p.Topics,
		IsFork:        p.ForkedFromProject != nil,
		IsArchived:    p.Archived,
		Visibility:    visibility,
		DefaultBranch: defaultBranch,
	}
}

func resolveGitLabToken(token, tokenEnv, tokenCommand string) string {
	if token != "" {
		return token
	}
	if tokenEnv != "" {
		if v := os.Getenv(tokenEnv); v != "" {
			return v
		}
	}
	if v := os.Getenv("GITLAB_TOKEN"); v != "" {
		return v
	}
	if tokenCommand != "" {
		out, err := exec.Command("sh", "-c", tokenCommand).Output()
		if err == nil {
			return strings.TrimSpace(string(out))
		}
	}
	return ""
}
