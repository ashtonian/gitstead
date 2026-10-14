package discovery

import (
	"slices"
	"testing"

	"github.com/ashtonian/gitstead/internal/config"
)

func TestRequestedFilters(t *testing.T) {
	rule := config.DiscoveryRule{
		Visibility:  "private",
		Languages:   []string{"Go"},
		Topics:      []string{"x"},
		NamePattern: "^a",
	}
	fs := requestedFilters(rule)
	for _, f := range []Filter{FilterForks, FilterArchived, FilterVisibility, FilterLanguage, FilterTopics, FilterName} {
		if !fs.Has(f) {
			t.Errorf("expected %s to be requested", f)
		}
	}

	rule2 := config.DiscoveryRule{IncludeForks: true, IncludeArchived: true, Visibility: "all"}
	fs2 := requestedFilters(rule2)
	if fs2.Has(FilterForks) || fs2.Has(FilterArchived) {
		t.Error("forks/archived should not be requested when included")
	}
	if fs2.Has(FilterVisibility) {
		t.Error("visibility 'all' should not be requested")
	}
}

func TestRuleScope(t *testing.T) {
	cases := []struct {
		rule config.DiscoveryRule
		kind ScopeKind
		val  string
	}{
		{config.DiscoveryRule{Org: "acme"}, ScopeOrg, "acme"},
		{config.DiscoveryRule{Group: "grp/sub"}, ScopeGroup, "grp/sub"},
		{config.DiscoveryRule{User: "bob"}, ScopeUser, "bob"},
		{config.DiscoveryRule{}, ScopeSelf, ""},
		{config.DiscoveryRule{Org: "acme", User: "bob"}, ScopeOrg, "acme"}, // org precedence
	}
	for _, c := range cases {
		got := ruleScope(c.rule)
		if got.Kind != c.kind || got.Value != c.val {
			t.Errorf("ruleScope(%+v) = %v/%q, want %v/%q", c.rule, got.Kind, got.Value, c.kind, c.val)
		}
	}
}

func TestMatchesLanguage(t *testing.T) {
	if !matchesLanguage(Repo{Languages: []string{"Go", "Shell"}}, []string{"go"}) {
		t.Error("expected case-insensitive match against the language set")
	}
	if !matchesLanguage(Repo{Language: "Python"}, []string{"python"}) {
		t.Error("expected fallback match against primary language")
	}
	if matchesLanguage(Repo{Languages: []string{"Rust"}}, []string{"Go"}) {
		t.Error("did not expect a match")
	}
	if matchesLanguage(Repo{}, []string{"Go"}) {
		t.Error("empty repo should not match")
	}
}

func TestFilterReposAppliedSkipsServerSide(t *testing.T) {
	repos := []Repo{{Name: "a", FullName: "o/a", IsArchived: true}}
	rule := config.DiscoveryRule{} // archived excluded by default

	if got := FilterRepos(repos, rule, nil, nil); len(got) != 0 {
		t.Errorf("expected archived repo dropped, got %d", len(got))
	}
	if got := FilterRepos(repos, rule, nil, FilterSet{FilterArchived: true}); len(got) != 1 {
		t.Errorf("expected archived repo kept when filtered server-side, got %d", len(got))
	}
}

func TestFilterReposDimensions(t *testing.T) {
	repos := []Repo{
		{Name: "keep", FullName: "o/keep", Visibility: "public", Topics: []string{"x", "y"}},
		{Name: "fork", FullName: "o/fork", Visibility: "public", IsFork: true},
		{Name: "arch", FullName: "o/arch", Visibility: "public", IsArchived: true},
		{Name: "priv", FullName: "o/priv", Visibility: "private", Topics: []string{"x", "y"}},
		{Name: "notopic", FullName: "o/notopic", Visibility: "public", Topics: []string{"x"}},
	}
	rule := config.DiscoveryRule{Visibility: "public", Topics: []string{"x", "y"}}
	got := FilterRepos(repos, rule, nil, nil)
	if len(got) != 1 || got[0].Name != "keep" {
		t.Fatalf("expected only 'keep', got %+v", got)
	}
}

func TestFilterReposNameAndExclude(t *testing.T) {
	repos := []Repo{
		{Host: "github.com", Name: "api-foo", FullName: "o/api-foo"},
		{Host: "github.com", Name: "lib-bar", FullName: "o/lib-bar"},
		{Host: "github.com", Name: "api-skip", FullName: "o/api-skip"},
	}
	rule := config.DiscoveryRule{NamePattern: "^api-"}
	excl := []config.ExcludeRule{{Repos: []string{"github.com/o/api-skip"}}}
	got := FilterRepos(repos, rule, excl, nil)
	if len(got) != 1 || got[0].Name != "api-foo" {
		t.Fatalf("expected only api-foo, got %+v", got)
	}
}

// names keeps a repo whose name matches any glob, ignoring case, and with
// name_pattern a name must match both; an exclude's names and repos globs
// skip what they match.
func TestFilterReposNameGlobs(t *testing.T) {
	repo := func(owner, name string) Repo {
		return Repo{Host: "github.com", Name: name, FullName: owner + "/" + name}
	}
	repos := []Repo{
		repo("acme", "billing-api"), repo("acme", "BILLING-web"), repo("acme", "payments"), repo("acme", "data-payments-etl"),
		repo("acme", "search"), repo("acme", "billing-sandbox"), repo("legacy", "billing-old"),
	}
	names := func(got []Repo) []string {
		var out []string
		for _, r := range got {
			out = append(out, r.Name)
		}
		return out
	}
	for _, tt := range []struct {
		name     string
		rule     config.DiscoveryRule
		excludes []config.ExcludeRule
		want     []string
	}{
		{"any glob, any case", config.DiscoveryRule{Names: []string{"*billing*", "*payments*"}}, nil,
			[]string{"billing-api", "BILLING-web", "payments", "data-payments-etl", "billing-sandbox", "billing-old"}},
		{"a word, not part of one", config.DiscoveryRule{Names: []string{"payments"}}, nil, []string{"payments", "data-payments-etl"}},
		{"skipped by whole name", config.DiscoveryRule{Names: []string{"billing"}, Skip: []string{"Billing-Sandbox", "billing"}}, nil,
			[]string{"billing-api", "BILLING-web", "billing-old"}},
		{"with name_pattern, both", config.DiscoveryRule{Names: []string{"*billing*", "*payments*"}, NamePattern: "^(billing|payments)"}, nil,
			[]string{"billing-api", "payments", "billing-sandbox", "billing-old"}},
		{"excluded by name and by key", config.DiscoveryRule{Names: []string{"*billing*"}},
			[]config.ExcludeRule{{Names: []string{"*-SANDBOX"}}, {Repos: []string{"github.com/legacy/*"}}},
			[]string{"billing-api", "BILLING-web"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := names(FilterRepos(repos, tt.rule, tt.excludes, nil)); !slices.Equal(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
	if !requestedFilters(config.DiscoveryRule{Names: []string{"x*"}}).Has(FilterName) {
		t.Error("names is a name filter")
	}
}

func TestFilterByLanguageAndTopics(t *testing.T) {
	repos := []Repo{
		{Name: "a", Languages: []string{"Go"}, Topics: []string{"x", "y"}},
		{Name: "b", Languages: []string{"Rust"}, Topics: []string{"x"}},
	}
	if got := filterByLanguage(repos, []string{"go"}); len(got) != 1 || got[0].Name != "a" {
		t.Fatalf("filterByLanguage got %+v", got)
	}
	if got := filterByTopics(repos, []string{"x", "y"}); len(got) != 1 || got[0].Name != "a" {
		t.Fatalf("filterByTopics got %+v", got)
	}
}
