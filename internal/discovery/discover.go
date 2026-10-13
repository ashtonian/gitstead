package discovery

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/ashtonian/git-mgr/internal/config"
	"github.com/ashtonian/git-mgr/internal/ctxutil"
	"github.com/ashtonian/git-mgr/internal/repourl"
)

// ProviderForConfig creates the right Provider for a given provider config.
// apiTimeout bounds each API request, retries included; 0 means none.
func ProviderForConfig(cfg config.Provider, apiTimeout time.Duration) (Provider, error) {
	switch config.NormalizeProviderType(cfg.Type) {
	case "github":
		return NewGitHubProvider(cfg, apiTimeout)
	case "gitlab":
		return NewGitLabProvider(cfg, apiTimeout)
	case "gitea":
		return NewGiteaProvider(cfg, apiTimeout)
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
	// Incomplete says why repos the rule matches may be missing from this
	// pass: a capped search, a failed filter lookup. Such a rule still adds
	// what it found but judges nothing stale.
	Incomplete []string
	Err        error // non-nil if this rule failed (others may still succeed)
}

// ManualRule is the rule a `repos:` entry is discovered by.
const ManualRule = "_manual"

// AnyFailed reports whether any discovery rule errored. Callers use this to
// suppress stale detection on a partial run: a transient provider outage yields
// an incomplete discovered set, and marking the missing repos stale (or, under
// remove_and_delete, deleting them) would be destructive.
func AnyFailed(results []DiscoverResult) bool {
	for _, r := range results {
		if r.Err != nil {
			return true
		}
	}
	return false
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
			// Config-level error: surface it but keep processing other rules.
			results = append(results, DiscoverResult{
				Rule: rule.Name, Provider: rule.Provider,
				Err: fmt.Errorf("rule %q references unknown provider %q", rule.Name, rule.Provider),
			})
			continue
		}

		provider, err := ProviderForConfig(*provCfg, cfg.Sync.APITimeout)
		if err != nil {
			results = append(results, DiscoverResult{
				Rule: rule.Name, Provider: provCfg.Name,
				Err: fmt.Errorf("creating provider %q: %w", provCfg.Name, err),
			})
			continue
		}

		ruleCtx, cancel := ctxutil.WithTimeout(ctx, cfg.Sync.DiscoveryTimeout)
		res, err := provider.Discover(ruleCtx, rule)
		if err != nil {
			// A single failing rule must not abort discovery for the rest:
			// otherwise one expired token wipes out the whole run (and, via
			// stale cleanup, could delete healthy repos).
			results = append(results, DiscoverResult{
				Rule: rule.Name, Provider: provCfg.Name,
				Err: fmt.Errorf("discovering from %q rule %q: %w", provCfg.Name, rule.Name, err),
			})
			cancel()
			continue
		}

		filtered, warnings, incomplete := applyFilters(ruleCtx, provider, res, rule, cfg.Exclude, cfg.Concurrency)
		cancel()

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
			Rule:       rule.Name,
			Provider:   provCfg.Name,
			Total:      len(filtered),
			Warnings:   warnings,
			Incomplete: incomplete,
		})
	}

	// If every rule that ran failed, report an aggregate error so callers don't
	// treat an all-failed run as "nothing exists upstream".
	if n := len(results); n > 0 {
		failed := 0
		for _, r := range results {
			if r.Err != nil {
				failed++
			}
		}
		if failed == n {
			var errs []error
			for _, r := range results {
				errs = append(errs, r.Err)
			}
			return results, discovered, errors.Join(errs...)
		}
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
			if existing.DiscoveredBy != "" && existing.DiscoveredBy != ManualRule {
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
func applyFilters(ctx context.Context, provider Provider, res ProviderResult, rule config.DiscoveryRule, excludes []config.ExcludeRule, concurrency int) (repos []Repo, warnings, incomplete []string) {
	incomplete = append(incomplete, res.Incomplete...)
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

	// FilterRepos silently ignores an un-compilable name_pattern (it must not
	// panic when driven without config.Validate); surface that as a warning so a
	// bad pattern doesn't look like "nothing matched".
	if rule.NamePattern != "" && !skip.Has(FilterName) {
		if _, err := regexp.Compile(rule.NamePattern); err != nil {
			warnings = append(warnings, fmt.Sprintf("rule %q: invalid name_pattern %q (%v); name filter not applied", rule.Name, rule.NamePattern, err))
		}
	}

	repos = FilterRepos(res.Repos, rule, excludes, skip)

	// Language: enrich the survivors, then match (any requested language).
	// Enrichment is best-effort: repos whose lookup failed have no languages
	// and drop out, which leaves the rule's result incomplete.
	if req.Has(FilterLanguage) && !res.Applied.Has(FilterLanguage) {
		if res.Capabilities.CanEnrichLanguages {
			if err := provider.EnrichLanguages(ctx, repos, concurrency); err != nil {
				incomplete = append(incomplete, fmt.Sprintf("rule %q: some language lookups failed (%v); repos whose lookup failed are left out", rule.Name, err))
			}
			repos = filterByLanguage(repos, rule.Languages)
		} else {
			warnings = append(warnings, fmt.Sprintf("rule %q: provider does not support the 'languages' filter; ignoring it", rule.Name))
		}
	}

	// Topics via enrichment (providers that don't return them inline).
	if deferTopics {
		if err := provider.EnrichTopics(ctx, repos, concurrency); err != nil {
			incomplete = append(incomplete, fmt.Sprintf("rule %q: some topic lookups failed (%v); repos whose lookup failed are left out", rule.Name, err))
		}
		repos = filterByTopics(repos, rule.Topics)
	}

	return repos, warnings, incomplete
}

// parseManualRepo converts a ManualRepo config entry into a Repo. It accepts any
// URL form git does (ssh, scp, https, git://, local) via repourl.Parse and
// records it under the matching transport, so a `repos:` entry given as https is
// cloned over https -- the URL's scheme decides, no clone_protocol needed.
func parseManualRepo(mr config.ManualRepo) (*Repo, error) {
	id, err := repourl.Parse(mr.URL)
	if err != nil {
		return nil, fmt.Errorf("%q: %w", mr.URL, err)
	}
	r := &Repo{
		Host:         id.Host,
		Owner:        id.Owner,
		Name:         id.Name,
		FullName:     id.FullName,
		Tags:         mr.Tags,
		DiscoveredBy: ManualRule,
		Path:         mr.Path,
	}
	if strings.HasPrefix(mr.URL, "http://") || strings.HasPrefix(mr.URL, "https://") {
		r.HTTPURL = mr.URL
	} else {
		r.SSHURL = mr.URL
	}
	return r, nil
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
