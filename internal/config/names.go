package config

import (
	"errors"
	"fmt"
	"strings"

	"github.com/ashtonian/gitten/internal/repourl"
)

// NameMatches reports whether a repo's name matches any of globs, ignoring
// case. The globs are repo keys' (see repourl.MatchKey): * matches any run
// of characters, ? one, [...] one of a class.
func NameMatches(globs []string, name string) bool {
	name = strings.ToLower(name)
	for _, g := range globs {
		if repourl.MatchKey(strings.ToLower(g), name) {
			return true
		}
	}
	return false
}

// CheckNameGlob reports a glob NameMatches can't use.
func CheckNameGlob(glob string) error {
	if strings.TrimSpace(glob) == "" {
		return errors.New("an empty name glob matches nothing; leave it out")
	}
	if err := repourl.ValidatePattern(glob); err != nil {
		return fmt.Errorf("%q is not a valid glob: %w", glob, err)
	}
	return nil
}

func checkNameGlobs(where string, globs []string) error {
	for _, g := range globs {
		if err := CheckNameGlob(g); err != nil {
			return fmt.Errorf("%s: names: %w", where, err)
		}
	}
	return nil
}
