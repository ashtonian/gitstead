package discovery

import (
	"regexp"
	"slices"

	"github.com/ashtonian/git-mgr/internal/config"
)

// FilterRepos applies inclusion and exclusion rules to a list of repos.
func FilterRepos(repos []Repo, rule config.DiscoveryRule, excludes []config.ExcludeRule) []Repo {
	var result []Repo
	for _, repo := range repos {
		if !rule.IncludeForks && repo.IsFork {
			continue
		}
		if !rule.IncludeArchived && repo.IsArchived {
			continue
		}
		if rule.Visibility != "" && rule.Visibility != "all" && repo.Visibility != rule.Visibility {
			continue
		}
		if len(rule.Languages) > 0 && !slices.Contains(rule.Languages, repo.Language) {
			continue
		}
		if len(rule.Topics) > 0 && !hasAll(repo.Topics, rule.Topics) {
			continue
		}
		if rule.NamePattern != "" {
			re, err := regexp.Compile(rule.NamePattern)
			if err != nil || !re.MatchString(repo.Name) {
				continue
			}
		}

		if isExcluded(repo, excludes) {
			continue
		}

		result = append(result, repo)
	}
	return result
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
		if slices.Contains(excl.Repos, repo.FullName) {
			return true
		}
	}
	return false
}
