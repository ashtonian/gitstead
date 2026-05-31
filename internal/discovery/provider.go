package discovery

import (
	"context"

	"github.com/ashtonian/git-mgr/internal/config"
)

// Repo represents a discovered repository from any provider.
type Repo struct {
	Host          string
	Owner         string
	Name          string
	FullName      string // "owner/name"
	SSHURL        string // "git@host:owner/name.git"
	Description   string
	Language      string   // primary language (for display/state)
	Languages     []string // all languages detected in the repo (for filtering)
	Topics        []string // repo topics from provider (e.g., GitHub topics)
	Tags          []string // git-mgr tags assigned by discovery rules
	IsFork        bool
	IsArchived    bool
	Visibility    string // "public", "private", "internal"
	DefaultBranch string
	DiscoveredBy  string // name of the discovery rule that found this repo
}

// Key returns the canonical state key for this repo (host/owner/name).
func (r *Repo) Key() string {
	return r.Host + "/" + r.FullName
}

// Filter identifies a discovery filter dimension. Providers report which
// filters they enforce server-side (via ProviderResult.Applied) so the
// client-side pass can skip them rather than re-applying.
type Filter string

const (
	FilterForks      Filter = "forks"
	FilterArchived   Filter = "archived"
	FilterVisibility Filter = "visibility"
	FilterLanguage   Filter = "language"
	FilterTopics     Filter = "topics"
	FilterName       Filter = "name_pattern"
)

// FilterSet is a set of filters. The zero value (nil) is a usable empty set.
type FilterSet map[Filter]bool

// Has reports whether f is in the set (nil-safe).
func (s FilterSet) Has(f Filter) bool { return s[f] }

// Capabilities declares what a provider can do beyond plain listing, so the
// pipeline can choose between per-repo enrichment and warning when a filter
// cannot be honored.
type Capabilities struct {
	// CanEnrichLanguages indicates the provider can populate Repo.Languages
	// on demand (used when a language filter is not applied server-side).
	CanEnrichLanguages bool
	// CanEnrichTopics indicates the provider can populate Repo.Topics on demand
	// (used when topics are not returned inline nor filtered server-side).
	CanEnrichTopics bool
}

// ProviderResult is what Discover returns: the repos, the set of filters the
// provider already enforced server-side, and its capabilities.
type ProviderResult struct {
	Repos        []Repo
	Applied      FilterSet
	Capabilities Capabilities
}

// Provider discovers repos from a hosting service instance.
type Provider interface {
	// Name returns the provider instance name.
	Name() string

	// Discover returns repos matching the rule's selection, applying whatever
	// filters it can server-side and reporting them in ProviderResult.Applied.
	Discover(ctx context.Context, rule config.DiscoveryRule) (ProviderResult, error)

	// EnrichLanguages populates Repo.Languages in place for the given repos.
	// Providers whose Capabilities.CanEnrichLanguages is false may no-op.
	EnrichLanguages(ctx context.Context, repos []Repo) error

	// EnrichTopics populates Repo.Topics in place for the given repos.
	// Providers whose Capabilities.CanEnrichTopics is false may no-op.
	EnrichTopics(ctx context.Context, repos []Repo) error
}

// ScopeKind is the kind of selection a discovery rule targets.
type ScopeKind int

const (
	ScopeSelf  ScopeKind = iota // authenticated user / the token's own repos
	ScopeUser                   // a named user
	ScopeOrg                    // a GitHub/Gitea organization
	ScopeGroup                  // a GitLab group
)

// Scope is the resolved selection target for a rule.
type Scope struct {
	Kind  ScopeKind
	Value string
}

// ruleScope resolves a discovery rule to a selection scope. Which selectors are
// valid for which provider type is enforced in config.Validate; here we just
// pick the first set selector in precedence order org > group > user > self.
func ruleScope(rule config.DiscoveryRule) Scope {
	switch {
	case rule.Org != "":
		return Scope{Kind: ScopeOrg, Value: rule.Org}
	case rule.Group != "":
		return Scope{Kind: ScopeGroup, Value: rule.Group}
	case rule.User != "":
		return Scope{Kind: ScopeUser, Value: rule.User}
	default:
		return Scope{Kind: ScopeSelf}
	}
}

// requestedFilters returns the set of filters a rule asks for. Forks/archived
// are exclusion filters: they are "requested" unless explicitly included.
func requestedFilters(rule config.DiscoveryRule) FilterSet {
	fs := FilterSet{}
	if !rule.IncludeForks {
		fs[FilterForks] = true
	}
	if !rule.IncludeArchived {
		fs[FilterArchived] = true
	}
	if rule.Visibility != "" && rule.Visibility != "all" {
		fs[FilterVisibility] = true
	}
	if len(rule.Languages) > 0 {
		fs[FilterLanguage] = true
	}
	if len(rule.Topics) > 0 {
		fs[FilterTopics] = true
	}
	if rule.NamePattern != "" {
		fs[FilterName] = true
	}
	return fs
}
