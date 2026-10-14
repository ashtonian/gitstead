package repourl_test

import (
	"testing"

	"github.com/ashtonian/gitten/internal/repourl"
)

func TestMatchKey(t *testing.T) {
	tests := []struct {
		pattern, key string
		want         bool
	}{
		{"github.com/acme/api", "github.com/acme/api", true},
		{"github.com/acme/*", "github.com/acme/api", true},
		{"github.com/acme/*", "github.com/acme/team/api", true}, // a subgroup
		{"github.com/acme/*", "github.com/acmeco/api", false},
		{"ghes.company.com/*", "ghes.company.com/team/api", true},
		{"*/my-repo", "github.com/me/my-repo", true},
		{"*/my-repo", "github.com/me/my-repo-2", false},
		{"github.com/acme/archived-*", "github.com/acme/archived-x", true},
		{"github.com/acme/ap?", "github.com/acme/api", true},
		{"github.com/[!x]cme/api", "github.com/acme/api", true},
		{"github.com/[x]cme/api", "github.com/acme/api", false},
		{"github.com/acme/a.i", "github.com/acme/api", false}, // . is literal
		{"github.com/[acme", "github.com/[acme", true},        // malformed, but the key itself
		{"github.com/[acme/*", "github.com/[acme/api", false},
	}
	for _, tt := range tests {
		if got := repourl.MatchKey(tt.pattern, tt.key); got != tt.want {
			t.Errorf("MatchKey(%q, %q) = %v, want %v", tt.pattern, tt.key, got, tt.want)
		}
	}
	if repourl.ValidatePattern("github.com/[acme/*") == nil || repourl.ValidatePattern("github.com/[]x") == nil {
		t.Error("malformed patterns must be reported")
	}
}
