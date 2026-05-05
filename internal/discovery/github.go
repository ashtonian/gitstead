package discovery

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strings"

	"github.com/ashtonian/git-mgr/internal/config"
	"github.com/google/go-github/v68/github"
	"golang.org/x/oauth2"
)

// GitHubProvider discovers repos from GitHub or GitHub Enterprise.
type GitHubProvider struct {
	cfg    config.Provider
	client *github.Client
	host   string
}

// NewGitHubProvider creates a GitHub provider from config.
func NewGitHubProvider(cfg config.Provider) (*GitHubProvider, error) {
	token := resolveToken(cfg.Token, cfg.TokenEnv, cfg.TokenCommand)

	var client *github.Client
	if token != "" {
		ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
		tc := oauth2.NewClient(context.Background(), ts)
		client = github.NewClient(tc)
	} else {
		client = github.NewClient(nil)
	}
	if cfg.URL != "" {
		// If the URL already contains an API path (e.g., /api/v1/ for Gitea/Forgejo),
		// set BaseURL directly. Otherwise, use WithEnterpriseURLs which appends /api/v3/.
		if strings.Contains(cfg.URL, "/api/") {
			u, err := url.Parse(cfg.URL)
			if err != nil {
				return nil, fmt.Errorf("parsing API URL: %w", err)
			}
			if !strings.HasSuffix(u.Path, "/") {
				u.Path += "/"
			}
			client.BaseURL = u
		} else {
			var err error
			client, err = client.WithEnterpriseURLs(cfg.URL, cfg.URL)
			if err != nil {
				return nil, fmt.Errorf("configuring GitHub Enterprise URL: %w", err)
			}
		}
	}

	host := "github.com"
	if cfg.URL != "" {
		u, err := url.Parse(cfg.URL)
		if err == nil && u.Host != "" {
			host = u.Host
		}
	}

	return &GitHubProvider{cfg: cfg, client: client, host: host}, nil
}

func (g *GitHubProvider) Name() string { return g.cfg.Name }

func (g *GitHubProvider) Discover(ctx context.Context, rule config.DiscoveryRule) ([]Repo, error) {
	var allRepos []*github.Repository
	var err error

	if rule.Org != "" {
		allRepos, err = g.listByOrg(ctx, rule.Org)
	} else if rule.User != "" {
		allRepos, err = g.listByUser(ctx, rule.User)
	} else {
		allRepos, err = g.listAuthenticatedUser(ctx)
	}
	if err != nil {
		return nil, err
	}

	var repos []Repo
	for _, r := range allRepos {
		repos = append(repos, g.toRepo(r))
	}
	return repos, nil
}

func (g *GitHubProvider) listByOrg(ctx context.Context, org string) ([]*github.Repository, error) {
	var all []*github.Repository
	opts := &github.RepositoryListByOrgOptions{ListOptions: github.ListOptions{PerPage: 100}}
	for {
		repos, resp, err := g.client.Repositories.ListByOrg(ctx, org, opts)
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

func (g *GitHubProvider) listByUser(ctx context.Context, user string) ([]*github.Repository, error) {
	var all []*github.Repository
	opts := &github.RepositoryListByUserOptions{ListOptions: github.ListOptions{PerPage: 100}}
	for {
		repos, resp, err := g.client.Repositories.ListByUser(ctx, user, opts)
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

func (g *GitHubProvider) listAuthenticatedUser(ctx context.Context) ([]*github.Repository, error) {
	var all []*github.Repository
	opts := &github.RepositoryListByAuthenticatedUserOptions{ListOptions: github.ListOptions{PerPage: 100}}
	for {
		repos, resp, err := g.client.Repositories.ListByAuthenticatedUser(ctx, opts)
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

func (g *GitHubProvider) toRepo(r *github.Repository) Repo {
	owner := r.GetOwner().GetLogin()
	name := r.GetName()

	visibility := "public"
	if r.GetPrivate() {
		visibility = "private"
	}

	return Repo{
		Host:          g.host,
		Owner:         owner,
		Name:          name,
		FullName:      owner + "/" + name,
		SSHURL:        r.GetSSHURL(),
		Description:   r.GetDescription(),
		Language:      r.GetLanguage(),
		Topics:        r.Topics,
		IsFork:        r.GetFork(),
		IsArchived:    r.GetArchived(),
		Visibility:    visibility,
		DefaultBranch: r.GetDefaultBranch(),
	}
}

// resolveToken resolves a token from: direct value > env var > command output.
func resolveToken(token, tokenEnv, tokenCommand string) string {
	if token != "" {
		return token
	}
	if tokenEnv != "" {
		if v := os.Getenv(tokenEnv); v != "" {
			return v
		}
	}
	// Try default env vars
	if v := os.Getenv("GITHUB_TOKEN"); v != "" {
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
