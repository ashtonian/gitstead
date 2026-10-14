package discovery

import (
	"regexp"
	"slices"
	"strings"

	"github.com/ashtonian/gitstead/internal/config"
	"github.com/ashtonian/gitstead/internal/repourl"
)

// FilterRepos applies inclusion and exclusion rules client-side. Filters listed
// in applied were already enforced server-side by the provider and are skipped.
//
// Language is intentionally NOT handled here: it may require per-repo enrichment
// (a network call), so RunDiscovery applies it separately via matchesLanguage
// after this cheap pass has narrowed the set.
func FilterRepos(repos []Repo, rule config.DiscoveryRule, excludes []config.ExcludeRule, applied FilterSet) []Repo {
	var nameRe *regexp.Regexp
	if rule.NamePattern != "" && !applied.Has(FilterName) {
		// Compile defensively: RunDiscovery can be driven with rules that never
		// went through config.Validate (e.g. tests), so a bad pattern must not
		// panic — an invalid regex simply applies no name filter.
		if re, err := regexp.Compile(rule.NamePattern); err == nil {
			nameRe = re
		}
	}

	var result []Repo
	for _, repo := range repos {
		if !applied.Has(FilterForks) && !rule.IncludeForks && repo.IsFork {
			continue
		}
		if !applied.Has(FilterArchived) && !rule.IncludeArchived && repo.IsArchived {
			continue
		}
		if !applied.Has(FilterVisibility) && rule.Visibility != "" && rule.Visibility != "all" && repo.Visibility != rule.Visibility {
			continue
		}
		if !applied.Has(FilterTopics) && len(rule.Topics) > 0 && !hasAll(repo.Topics, rule.Topics) {
			continue
		}
		if nameRe != nil && !nameRe.MatchString(repo.Name) {
			continue
		}
		if len(rule.Names) > 0 && !applied.Has(FilterName) && !config.NameMatches(rule.Names, repo.Name) {
			continue
		}
		if isExcluded(repo, excludes) {
			continue
		}
		result = append(result, repo)
	}
	return result
}

// matchesLanguage reports whether the repo uses any of the wanted languages,
// case-insensitively. It matches against the full language set, falling back to
// the primary language when the set is not populated.
func matchesLanguage(repo Repo, wanted []string) bool {
	langs := repo.Languages
	if len(langs) == 0 && repo.Language != "" {
		langs = []string{repo.Language}
	}
	for _, w := range wanted {
		for _, l := range langs {
			if strings.EqualFold(w, l) {
				return true
			}
		}
	}
	return false
}

// filterByLanguage keeps only repos matching one of the wanted languages.
func filterByLanguage(repos []Repo, wanted []string) []Repo {
	var out []Repo
	for _, r := range repos {
		if matchesLanguage(r, wanted) {
			out = append(out, r)
		}
	}
	return out
}

// filterByTopics keeps only repos that have all of the wanted topics.
func filterByTopics(repos []Repo, wanted []string) []Repo {
	var out []Repo
	for _, r := range repos {
		if hasAll(r.Topics, wanted) {
			out = append(out, r)
		}
	}
	return out
}

func hasAll(haystack, needles []string) bool {
	for _, n := range needles {
		if !slices.Contains(haystack, n) {
			return false
		}
	}
	return true
}

func isExcluded(repo Repo, excludes []config.ExcludeRule) bool {
	for _, excl := range excludes {
		if excl.NamePattern != "" {
			re, err := regexp.Compile(excl.NamePattern)
			if err == nil && re.MatchString(repo.Name) {
				return true
			}
		}
		if config.NameMatches(excl.Names, repo.Name) {
			return true
		}
		if slices.ContainsFunc(excl.Repos, func(p string) bool { return repourl.MatchKey(p, repo.Key()) }) {
			return true
		}
	}
	return false
}
