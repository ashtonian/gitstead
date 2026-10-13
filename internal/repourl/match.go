package repourl

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
)

// MatchKey reports whether repo key host/owner/name matches pattern: a key,
// or a glob over keys in which * matches any run of characters, slashes
// included, ? matches one, and [...] (or [!...]) one in or out of a class.
// So github.com/acme/* is every repo of acme, subgroups included,
// ghes.company.com/* every repo on that host, and */api every repo named api.
// A malformed pattern matches nothing; ValidatePattern reports it.
func MatchKey(pattern, key string) bool {
	if pattern == key {
		return true
	}
	re, err := compilePattern(pattern)
	return err == nil && re.MatchString(key)
}

// ValidatePattern returns an error when pattern isn't a well-formed glob.
func ValidatePattern(pattern string) error {
	_, err := compilePattern(pattern)
	return err
}

var patterns sync.Map // pattern -> *regexp.Regexp or error

func compilePattern(pattern string) (*regexp.Regexp, error) {
	if v, ok := patterns.Load(pattern); ok {
		if err, isErr := v.(error); isErr {
			return nil, err
		}
		return v.(*regexp.Regexp), nil
	}
	re, err := globRegexp(pattern)
	if err != nil {
		patterns.Store(pattern, err)
		return nil, err
	}
	patterns.Store(pattern, re)
	return re, nil
}

func globRegexp(pattern string) (*regexp.Regexp, error) {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		switch c := pattern[i]; c {
		case '*':
			b.WriteString(".*")
		case '?':
			b.WriteString(".")
		case '[':
			end := strings.IndexByte(pattern[i+1:], ']')
			if end < 0 {
				return nil, fmt.Errorf("pattern %q: [ without ]", pattern)
			}
			class := pattern[i+1 : i+1+end]
			if strings.HasPrefix(class, "!") {
				class = "^" + class[1:]
			}
			if class == "" || class == "^" {
				return nil, fmt.Errorf("pattern %q: empty [...]", pattern)
			}
			b.WriteString("[" + strings.ReplaceAll(class, `\`, `\\`) + "]")
			i += end + 1
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	re, err := regexp.Compile(b.String())
	if err != nil {
		return nil, fmt.Errorf("pattern %q: %w", pattern, err)
	}
	return re, nil
}
