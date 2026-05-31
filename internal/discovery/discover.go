package discovery

import (
	"context"
	"fmt"
	"strings"

	"github.com/ashtonian/git-mgr/internal/config"
)

// ProviderForConfig creates the right Provider for a given provider config.
func ProviderForConfig(cfg config.Provider) (Provider, error) {
	switch config.NormalizeProviderType(cfg.Type) {
	case "github":
		return NewGitHubProvider(cfg)
	case "gitlab":
		return NewGitLabProvider(cfg)
	case "gitea":
		return NewGiteaProvider(cfg)
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
	Warnings   []string // filters that could not be honored, etc.
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

		res, err := provider.Discover(ctx, rule)
		if err != nil {
			return nil, nil, fmt.Errorf("discovering from %q rule %q: %w", provCfg.Name, rule.Name, err)
		}

		filtered, warnings := applyFilters(ctx, provider, res, rule, cfg.Exclude)

		for i := range filtered {
			r := filtered[i]
			r.Tags = rule.Tags
			r.DiscoveredBy = rule.Name
			// A repo can match several rules; union their tags and keep the
			// first matching rule as discoverer (drives ssh-key resolution).
			if existing, ok := discovered[r.Key()]; ok {
				r.DiscoveredBy = existing.DiscoveredBy
				r.Tags = mergeTags(existing.Tags, rule.Tags)
			}
			discovered[r.Key()] = r
		}

		results = append(results, DiscoverResult{
			Rule:     rule.Name,
			Provider: provCfg.Name,
			Total:    len(filtered),
			Warnings: warnings,
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
		if existing, ok := discovered[parsed.Key()]; ok {
			parsed.Tags = mergeTags(existing.Tags, parsed.Tags)
			if existing.DiscoveredBy != "" && existing.DiscoveredBy != "_manual" {
				parsed.DiscoveredBy = existing.DiscoveredBy
			}
		}
		discovered[parsed.Key()] = *parsed
	}

	return results, discovered, nil
}

// applyFilters runs the client-side portion of discovery: it skips filters the
// provider already enforced server-side, applies the cheap field-based filters,
// then handles language (and, where needed, topics) via per-repo enrichment on
// the survivors only. It returns the matching repos plus any warnings about
// filters that could not be honored.
func applyFilters(ctx context.Context, provider Provider, res ProviderResult, rule config.DiscoveryRule, excludes []config.ExcludeRule) ([]Repo, []string) {
	var warnings []string
	req := requestedFilters(rule)

	// Filters to skip in the cheap client-side pass: those already applied
	// server-side, plus the ones handled below via enrichment.
	skip := FilterSet{}
	for f := range res.Applied {
		skip[f] = true
	}
	skip[FilterLanguage] = true // always handled here, never in FilterRepos
	deferTopics := req.Has(FilterTopics) && !res.Applied.Has(FilterTopics) && res.Capabilities.CanEnrichTopics
	if deferTopics {
		skip[FilterTopics] = true
	}

	repos := FilterRepos(res.Repos, rule, excludes, skip)

	// Language: enrich the survivors, then match (any requested language).
	if req.Has(FilterLanguage) && !res.Applied.Has(FilterLanguage) {
		if res.Capabilities.CanEnrichLanguages {
			if err := provider.EnrichLanguages(ctx, repos); err != nil {
				warnings = append(warnings, fmt.Sprintf("rule %q: language lookup failed (%v); 'languages' filter not applied", rule.Name, err))
			} else {
				repos = filterByLanguage(repos, rule.Languages)
			}
		} else {
			warnings = append(warnings, fmt.Sprintf("rule %q: provider does not support the 'languages' filter; ignoring it", rule.Name))
		}
	}

	// Topics via enrichment (providers that don't return them inline).
	if deferTopics {
		if err := provider.EnrichTopics(ctx, repos); err != nil {
			warnings = append(warnings, fmt.Sprintf("rule %q: topic lookup failed (%v); 'topics' filter not applied", rule.Name, err))
		} else {
			repos = filterByTopics(repos, rule.Topics)
		}
	}

	return repos, warnings
}

// parseManualRepo converts a ManualRepo config entry into a Repo.
func parseManualRepo(mr config.ManualRepo) (*Repo, error) {
	parts := parseGitURL(mr.URL)
	if parts == nil {
		return nil, fmt.Errorf("unsupported URL format (expected git@host:owner/repo.git or ssh://git@host[:port]/owner/repo.git): %s", mr.URL)
	}
	return &Repo{
		Host:         parts.host,
		Owner:        parts.owner,
		Name:         parts.name,
		FullName:     parts.owner + "/" + parts.name,
		SSHURL:       mr.URL,
		Tags:         mr.Tags,
		DiscoveredBy: "_manual",
	}, nil
}

type sshParts struct {
	host, owner, name string
}

// parseGitURL parses scp-like (git@host:owner/name.git) and ssh://
// (ssh://git@host[:port]/owner/name.git) URLs. Other schemes return nil.
func parseGitURL(url string) *sshParts {
	if strings.HasPrefix(url, "ssh://") {
		return parseSSHURL(url)
	}
	if strings.Contains(url, "://") {
		return nil
	}
	if strings.Contains(url, ":") {
		return splitSSH(url)
	}
	return nil
}

// splitSSH parses an scp-like URL: [user@]host:owner/.../name[.git]
func splitSSH(url string) *sshParts {
	if at := strings.Index(url, "@"); at >= 0 {
		url = url[at+1:]
	}
	colon := strings.Index(url, ":")
	if colon < 0 {
		return nil
	}
	host := url[:colon]
	owner, name := splitOwnerName(strings.TrimSuffix(url[colon+1:], ".git"))
	if host == "" || owner == "" || name == "" {
		return nil
	}
	return &sshParts{host: host, owner: owner, name: name}
}

// parseSSHURL parses ssh://[user@]host[:port]/owner/.../name[.git]
func parseSSHURL(url string) *sshParts {
	s := strings.TrimPrefix(url, "ssh://")
	if at := strings.Index(s, "@"); at >= 0 {
		s = s[at+1:]
	}
	slash := strings.Index(s, "/")
	if slash < 0 {
		return nil
	}
	host := s[:slash]
	if c := strings.Index(host, ":"); c >= 0 {
		host = host[:c]
	}
	owner, name := splitOwnerName(strings.TrimSuffix(s[slash+1:], ".git"))
	if host == "" || owner == "" || name == "" {
		return nil
	}
	return &sshParts{host: host, owner: owner, name: name}
}

// splitOwnerName splits owner/name or group/subgroup/name into the leading
// owner path and the final repo name.
func splitOwnerName(path string) (string, string) {
	path = strings.Trim(path, "/")
	idx := strings.LastIndex(path, "/")
	if idx < 0 {
		return "", ""
	}
	return path[:idx], path[idx+1:]
}

// mergeTags returns the de-duplicated union of two tag slices, preserving order.
func mergeTags(a, b []string) []string {
	seen := make(map[string]bool, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	for _, group := range [][]string{a, b} {
		for _, t := range group {
			if t == "" || seen[t] {
				continue
			}
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}

func hasTag(tags []string, target string) bool {
	for _, t := range tags {
		if t == target {
			return true
		}
	}
	return false
}
