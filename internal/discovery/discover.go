package discovery

import (
	"context"
	"fmt"
	"strings"

	"github.com/ashtonian/git-mgr/internal/config"
)

// ProviderForConfig creates the right Provider for a given provider config.
func ProviderForConfig(cfg config.Provider) (Provider, error) {
	switch cfg.Type {
	case "github":
		return NewGitHubProvider(cfg)
	case "gitlab":
		return NewGitLabProvider(cfg)
	default:
		return nil, fmt.Errorf("unknown provider type: %s", cfg.Type)
	}
}

// DiscoverResult holds the result of a discovery run.
type DiscoverResult struct {
	Rule       string
	Provider   string
	Total      int
	NewRepos   []Repo
	Existing   int
	StaleCount int
}

// RunDiscovery runs all discovery rules (or a filtered set) and returns discovered repos.
func RunDiscovery(ctx context.Context, cfg *config.Config, ruleFilter, tagFilter string) ([]DiscoverResult, map[string]Repo, error) {
	discovered := make(map[string]Repo)
	var results []DiscoverResult

	for _, rule := range cfg.Discovery {
		if ruleFilter != "" && rule.Name != ruleFilter {
			continue
		}
		if tagFilter != "" && !hasTag(rule.Tags, tagFilter) {
			continue
		}

		// Find the provider config
		var provCfg *config.Provider
		for i, p := range cfg.Providers {
			if p.Name == rule.Provider {
				provCfg = &cfg.Providers[i]
				break
			}
		}
		if provCfg == nil {
			return nil, nil, fmt.Errorf("rule %q references unknown provider %q", rule.Name, rule.Provider)
		}

		provider, err := ProviderForConfig(*provCfg)
		if err != nil {
			return nil, nil, fmt.Errorf("creating provider %q: %w", provCfg.Name, err)
		}

		repos, err := provider.Discover(ctx, rule)
		if err != nil {
			return nil, nil, fmt.Errorf("discovering from %q rule %q: %w", provCfg.Name, rule.Name, err)
		}

		filtered := FilterRepos(repos, rule, cfg.Exclude)

		for i := range filtered {
			filtered[i].Tags = rule.Tags
			filtered[i].DiscoveredBy = rule.Name
			discovered[filtered[i].Key()] = filtered[i]
		}

		results = append(results, DiscoverResult{
			Rule:     rule.Name,
			Provider: provCfg.Name,
			Total:    len(filtered),
		})
	}

	// Include manual repos from config
	for _, mr := range cfg.Repos {
		parsed, err := parseManualRepo(mr)
		if err != nil {
			return nil, nil, fmt.Errorf("manual repo %q: %w", mr.URL, err)
		}
		if tagFilter != "" && !hasTag(parsed.Tags, tagFilter) {
			continue
		}
		discovered[parsed.Key()] = *parsed
	}

	return results, discovered, nil
}

// parseManualRepo converts a ManualRepo config entry into a Repo.
func parseManualRepo(mr config.ManualRepo) (*Repo, error) {
	url := mr.URL

	// Parse SSH URL: git@host:owner/name.git
	if len(url) > 4 && url[:4] == "git@" {
		parts := splitSSH(url)
		if parts == nil {
			return nil, fmt.Errorf("invalid SSH URL: %s", url)
		}
		return &Repo{
			Host:         parts.host,
			Owner:        parts.owner,
			Name:         parts.name,
			FullName:     parts.owner + "/" + parts.name,
			SSHURL:       url,
			Tags:         mr.Tags,
			DiscoveredBy: "_manual",
		}, nil
	}

	return nil, fmt.Errorf("unsupported URL format (expected git@host:owner/repo.git): %s", url)
}

type sshParts struct {
	host, owner, name string
}

func splitSSH(url string) *sshParts {
	// git@host:owner/name.git
	colonIdx := -1
	for i, c := range url {
		if c == ':' {
			colonIdx = i
			break
		}
	}
	if colonIdx < 0 {
		return nil
	}
	host := url[4:colonIdx] // skip "git@"
	path := url[colonIdx+1:]
	path = strings.TrimSuffix(path, ".git")

	slashIdx := -1
	for i, c := range path {
		if c == '/' {
			slashIdx = i
			break
		}
	}
	if slashIdx < 0 {
		return nil
	}
	return &sshParts{
		host:  host,
		owner: path[:slashIdx],
		name:  path[slashIdx+1:],
	}
}

func hasTag(tags []string, target string) bool {
	for _, t := range tags {
		if t == target {
			return true
		}
	}
	return false
}
