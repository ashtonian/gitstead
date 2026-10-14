package config

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"

	"github.com/ashtonian/gitstead/internal/repourl"
)

// NameMatches reports whether a repo's name matches any of patterns,
// ignoring case. A pattern with a wildcard -- *, ? or [...] -- is a glob over
// the whole name, as repo keys' are (see repourl.MatchKey): svc-* is every
// name starting svc-. A pattern without one matches a run of whole words in
// the name: ops matches ops-tools, platform-ops and OpsPortal but not devops,
// and platform-ops matches team-platform-ops. Words are split at anything
// but a letter or digit, where a lower-case letter meets a capital, at the
// end of a run of capitals (HTTPServer), and where letters meet digits.
func NameMatches(patterns []string, name string) bool {
	var words []string
	for _, p := range patterns {
		if hasWildcard(p) {
			if repourl.MatchKey(strings.ToLower(p), strings.ToLower(name)) {
				return true
			}
			continue
		}
		if words == nil {
			words = nameWords(name)
		}
		if containsRun(words, nameWords(p)) {
			return true
		}
	}
	return false
}

// CheckNameGlob reports a name pattern NameMatches can't use.
func CheckNameGlob(pattern string) error {
	switch {
	case strings.TrimSpace(pattern) == "":
		return errors.New("an empty name pattern matches nothing; leave it out")
	case hasWildcard(pattern):
		if err := repourl.ValidatePattern(pattern); err != nil {
			return fmt.Errorf("%q is not a valid glob: %w", pattern, err)
		}
	case len(nameWords(pattern)) == 0:
		return fmt.Errorf("%q has no letters or digits to match as a word", pattern)
	}
	return nil
}

func checkNameGlobs(where, field string, patterns []string) error {
	for _, p := range patterns {
		if err := CheckNameGlob(p); err != nil {
			return fmt.Errorf("%s: %s: %w", where, field, err)
		}
	}
	return nil
}

func hasWildcard(pattern string) bool {
	return strings.ContainsAny(pattern, "*?[")
}

// nameWords splits s into its lower-case words, as NameMatches defines them.
func nameWords(s string) []string {
	var words []string
	runes := []rune(s)
	start := -1
	flush := func(end int) {
		if start >= 0 {
			words = append(words, strings.ToLower(string(runes[start:end])))
			start = -1
		}
	}
	for i, r := range runes {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			flush(i)
			continue
		}
		if start >= 0 {
			prev := runes[i-1]
			switch {
			case unicode.IsLower(prev) && unicode.IsUpper(r):
				flush(i)
			case unicode.IsUpper(prev) && unicode.IsUpper(r) && i+1 < len(runes) && unicode.IsLower(runes[i+1]):
				flush(i)
			case unicode.IsDigit(prev) != unicode.IsDigit(r):
				flush(i)
			}
		}
		if start < 0 {
			start = i
		}
	}
	flush(len(runes))
	return words
}

// containsRun reports whether run appears in words, consecutively.
func containsRun(words, run []string) bool {
	if len(run) == 0 {
		return false
	}
	for i := 0; i+len(run) <= len(words); i++ {
		if slices.Equal(words[i:i+len(run)], run) {
			return true
		}
	}
	return false
}
