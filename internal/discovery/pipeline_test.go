package discovery

import (
	"context"
	"errors"
	"testing"

	"github.com/ashtonian/gitten/internal/config"
)

// fakeProvider is a Provider whose results and enrichment behavior are scripted,
// so the pipeline (applyFilters) can be tested without any network.
type fakeProvider struct {
	result      ProviderResult
	langs       map[string][]string // FullName -> languages, applied during EnrichLanguages
	topics      map[string][]string // FullName -> topics, applied during EnrichTopics
	langErr     error
	topicErr    error
	enrichCalls int
}

func (f *fakeProvider) Name() string { return "fake" }
func (f *fakeProvider) Discover(context.Context, config.DiscoveryRule) (ProviderResult, error) {
	return f.result, nil
}
func (f *fakeProvider) EnrichLanguages(_ context.Context, repos []Repo, _ int) error {
	f.enrichCalls++
	for i := range repos {
		repos[i].Languages = f.langs[repos[i].FullName]
	}
	return f.langErr
}
func (f *fakeProvider) EnrichTopics(_ context.Context, repos []Repo, _ int) error {
	for i := range repos {
		repos[i].Topics = f.topics[repos[i].FullName]
	}
	return f.topicErr
}

func repo(name string) Repo {
	return Repo{Host: "h", Owner: "o", Name: name, FullName: "o/" + name, Visibility: "public"}
}

func TestApplyFiltersLanguageEnrichment(t *testing.T) {
	f := &fakeProvider{
		result: ProviderResult{
			Repos:        []Repo{repo("go1"), repo("rust1")},
			Capabilities: Capabilities{CanEnrichLanguages: true},
		},
		langs: map[string][]string{"o/go1": {"Go"}, "o/rust1": {"Rust"}},
	}
	rule := config.DiscoveryRule{Name: "r", Languages: []string{"go"}} // case-insensitive
	got, warns, incomplete := applyFilters(context.Background(), f, f.result, rule, nil, 4)
	if len(got) != 1 || got[0].Name != "go1" {
		t.Fatalf("expected only go1, got %+v", got)
	}
	if len(warns) != 0 || len(incomplete) != 0 {
		t.Errorf("unexpected warnings %v, incomplete %v", warns, incomplete)
	}
	if f.enrichCalls != 1 {
		t.Errorf("expected exactly one enrichment call, got %d", f.enrichCalls)
	}
}

func TestApplyFiltersLanguageEnrichmentPartialFailure(t *testing.T) {
	// One repo's language resolved, the batch reported a (joined) error: we still
	// filter on what resolved, and the result is incomplete -- the repo whose
	// lookup failed may match.
	f := &fakeProvider{
		result: ProviderResult{
			Repos:        []Repo{repo("go1"), repo("unknown")},
			Capabilities: Capabilities{CanEnrichLanguages: true},
		},
		langs:   map[string][]string{"o/go1": {"Go"}}, // o/unknown stays empty
		langErr: errors.New("o/unknown: boom"),
	}
	rule := config.DiscoveryRule{Name: "r", Languages: []string{"Go"}}
	got, _, incomplete := applyFilters(context.Background(), f, f.result, rule, nil, 4)
	if len(got) != 1 || got[0].Name != "go1" {
		t.Fatalf("expected go1 to survive, got %+v", got)
	}
	if len(incomplete) == 0 {
		t.Error("a failed language lookup should leave the result incomplete")
	}
}

func TestApplyFiltersLanguageUnsupported(t *testing.T) {
	// Provider can't enrich languages: filter is skipped with a warning, not a
	// silent empty result.
	f := &fakeProvider{result: ProviderResult{Repos: []Repo{repo("a")}}}
	rule := config.DiscoveryRule{Name: "r", Languages: []string{"Go"}}
	got, warns, _ := applyFilters(context.Background(), f, f.result, rule, nil, 4)
	if len(got) != 1 {
		t.Fatalf("expected repo kept (filter ignored), got %d", len(got))
	}
	if len(warns) == 0 {
		t.Error("expected a warning that languages is unsupported")
	}
}

func TestApplyFiltersServerSideLanguageSkipsEnrichment(t *testing.T) {
	// When the provider already applied the language filter server-side, the
	// pipeline must NOT call enrichment again.
	f := &fakeProvider{
		result: ProviderResult{
			Repos:        []Repo{repo("go1")},
			Applied:      FilterSet{FilterLanguage: true},
			Capabilities: Capabilities{CanEnrichLanguages: true},
		},
	}
	rule := config.DiscoveryRule{Name: "r", Languages: []string{"Go"}}
	got, _, _ := applyFilters(context.Background(), f, f.result, rule, nil, 4)
	if len(got) != 1 {
		t.Fatalf("expected the server-filtered repo, got %d", len(got))
	}
	if f.enrichCalls != 0 {
		t.Errorf("enrichment should be skipped when applied server-side, got %d calls", f.enrichCalls)
	}
}

func TestApplyFiltersTopicEnrichment(t *testing.T) {
	f := &fakeProvider{
		result: ProviderResult{
			Repos:        []Repo{repo("a"), repo("b")},
			Capabilities: Capabilities{CanEnrichTopics: true},
		},
		topics: map[string][]string{"o/a": {"backend", "svc"}, "o/b": {"frontend"}},
	}
	rule := config.DiscoveryRule{Name: "r", Topics: []string{"backend"}}
	got, _, _ := applyFilters(context.Background(), f, f.result, rule, nil, 4)
	if len(got) != 1 || got[0].Name != "a" {
		t.Fatalf("expected only a, got %+v", got)
	}
}

func TestApplyFiltersCarriesProviderIncompleteness(t *testing.T) {
	f := &fakeProvider{result: ProviderResult{
		Repos:      []Repo{repo("a")},
		Incomplete: []string{"search truncated at 1000"},
	}}
	_, _, incomplete := applyFilters(context.Background(), f, f.result, config.DiscoveryRule{Name: "r"}, nil, 4)
	if len(incomplete) != 1 || incomplete[0] != "search truncated at 1000" {
		t.Errorf("provider incompleteness not carried through: %v", incomplete)
	}
}

func TestAnyFailed(t *testing.T) {
	if AnyFailed([]DiscoverResult{{}, {}}) {
		t.Error("no errors should report not-failed")
	}
	if !AnyFailed([]DiscoverResult{{}, {Err: errors.New("x")}}) {
		t.Error("one error should report failed")
	}
}

func TestEnrichConcurrentBestEffort(t *testing.T) {
	repos := []Repo{repo("a"), repo("b"), repo("c")}
	err := enrichConcurrent(context.Background(), repos, 2, func(_ context.Context, r *Repo) error {
		if r.Name == "b" {
			return errors.New("b failed")
		}
		r.Languages = []string{"Go"}
		return nil
	})
	if err == nil {
		t.Fatal("expected a joined error for b")
	}
	// a and c still enriched despite b failing.
	if len(repos[0].Languages) == 0 || len(repos[2].Languages) == 0 {
		t.Error("siblings should be enriched despite one failure")
	}
	if len(repos[1].Languages) != 0 {
		t.Error("failed repo should be left empty")
	}
}

func TestRuleScopeQueryPrecedence(t *testing.T) {
	s := ruleScope(config.DiscoveryRule{Query: "stars:>1", Org: "ignored"})
	if s.Kind != ScopeQuery || s.Value != "stars:>1" {
		t.Errorf("query scope: got %v/%q", s.Kind, s.Value)
	}
}

func TestAllFiltersApplied(t *testing.T) {
	fs := allFiltersApplied()
	for _, f := range []Filter{FilterForks, FilterArchived, FilterVisibility, FilterLanguage, FilterTopics, FilterName} {
		if !fs.Has(f) {
			t.Errorf("expected %s applied", f)
		}
	}
}
