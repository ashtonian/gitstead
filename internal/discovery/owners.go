package discovery

import (
	"context"
	"fmt"

	"code.gitea.io/sdk/gitea"
	"github.com/google/go-github/v88/github"
	gitlab "gitlab.com/gitlab-org/api/client-go"

	"github.com/ashtonian/git-mgr/internal/config"
)

// Owner is an account or organization whose repos a rule can track.
type Owner struct {
	Login string
	Kind  string // "self" for the token's own account, else "org" (GitLab: "group")
}

// ListOwners returns the token's own login, then the organizations it belongs
// to (top-level groups on GitLab), for choosing what to track.
func ListOwners(ctx context.Context, p config.Provider) ([]Owner, error) {
	switch config.NormalizeProviderType(p.Type) {
	case "github":
		gp, err := NewGitHubProvider(p)
		if err != nil {
			return nil, err
		}
		return githubOwners(ctx, gp.core.client)
	case "gitlab":
		gp, err := NewGitLabProvider(p)
		if err != nil {
			return nil, err
		}
		return gitlabOwners(ctx, gp.client)
	case "gitea":
		gp, err := NewGiteaProvider(p)
		if err != nil {
			return nil, err
		}
		return giteaOwners(gp.client)
	}
	return nil, fmt.Errorf("provider %q: unknown type %q", p.Name, p.Type)
}

func githubOwners(ctx context.Context, c *github.Client) ([]Owner, error) {
	me, _, err := c.Users.Get(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("reading the token's account: %w", err)
	}
	owners := []Owner{{Login: me.GetLogin(), Kind: "self"}}
	opts := &github.ListOptions{PerPage: 100}
	for {
		orgs, resp, err := c.Organizations.List(ctx, "", opts)
		if err != nil {
			return owners, fmt.Errorf("listing organizations: %w", err)
		}
		for _, o := range orgs {
			owners = append(owners, Owner{Login: o.GetLogin(), Kind: "org"})
		}
		if resp.NextPage == 0 {
			return owners, nil
		}
		opts.Page = resp.NextPage
	}
}

func gitlabOwners(ctx context.Context, c *gitlab.Client) ([]Owner, error) {
	me, _, err := c.Users.CurrentUser(gitlab.WithContext(ctx))
	if err != nil {
		return nil, fmt.Errorf("reading the token's account: %w", err)
	}
	owners := []Owner{{Login: me.Username, Kind: "self"}}
	opts := &gitlab.ListGroupsOptions{ListOptions: gitlab.ListOptions{PerPage: 100}, TopLevelOnly: gitlab.Ptr(true)}
	for {
		groups, resp, err := c.Groups.ListGroups(opts, gitlab.WithContext(ctx))
		if err != nil {
			return owners, fmt.Errorf("listing groups: %w", err)
		}
		for _, g := range groups {
			owners = append(owners, Owner{Login: g.FullPath, Kind: "group"})
		}
		if resp.NextPage == 0 {
			return owners, nil
		}
		opts.Page = resp.NextPage
	}
}

func giteaOwners(c *gitea.Client) ([]Owner, error) {
	me, _, err := c.GetMyUserInfo()
	if err != nil {
		return nil, fmt.Errorf("reading the token's account: %w", err)
	}
	owners := []Owner{{Login: me.UserName, Kind: "self"}}
	for page := 1; ; page++ {
		orgs, _, err := c.ListMyOrgs(gitea.ListOrgsOptions{ListOptions: gitea.ListOptions{Page: page, PageSize: 50}})
		if err != nil {
			return owners, fmt.Errorf("listing organizations: %w", err)
		}
		for _, o := range orgs {
			owners = append(owners, Owner{Login: o.UserName, Kind: "org"})
		}
		if len(orgs) < 50 {
			return owners, nil
		}
	}
}
