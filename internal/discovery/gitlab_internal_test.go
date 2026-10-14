package discovery

import (
	"testing"

	"github.com/ashtonian/gitstead/internal/config"
	gitlab "gitlab.com/gitlab-org/api/client-go"
)

func TestGLVisibility(t *testing.T) {
	cases := map[string]*gitlab.VisibilityValue{
		"public":   gitlab.Ptr(gitlab.PublicVisibility),
		"internal": gitlab.Ptr(gitlab.InternalVisibility),
		"private":  gitlab.Ptr(gitlab.PrivateVisibility),
		"all":      nil,
		"":         nil,
	}
	for in, want := range cases {
		got := glVisibility(in)
		if (got == nil) != (want == nil) {
			t.Errorf("glVisibility(%q): nil mismatch", in)
			continue
		}
		if got != nil && *got != *want {
			t.Errorf("glVisibility(%q) = %v, want %v", in, *got, *want)
		}
	}
}

// commonProjectFilters must push every requested topic (comma-joined, AND
// semantics) server-side and mark FilterTopics applied — not just the single
// topic case.
func TestCommonProjectFiltersMultiTopic(t *testing.T) {
	applied := FilterSet{}
	rule := config.DiscoveryRule{
		Visibility: "private",
		Topics:     []string{"a", "b", "c"},
		// IncludeArchived false => archived filter pushed
	}
	vis, archived, topic := commonProjectFilters(rule, applied)

	if vis == nil || *vis != gitlab.PrivateVisibility {
		t.Errorf("visibility not pushed: %v", vis)
	}
	if archived == nil || *archived != false {
		t.Errorf("archived=false not pushed: %v", archived)
	}
	if topic == nil || *topic != "a,b,c" {
		t.Errorf("topic = %v, want \"a,b,c\"", topic)
	}
	if !applied.Has(FilterTopics) || !applied.Has(FilterVisibility) || !applied.Has(FilterArchived) {
		t.Errorf("applied set incomplete: %v", applied)
	}
}

func TestCommonProjectFiltersIncludeArchived(t *testing.T) {
	applied := FilterSet{}
	rule := config.DiscoveryRule{IncludeArchived: true}
	_, archived, _ := commonProjectFilters(rule, applied)
	if archived != nil {
		t.Error("archived filter should not be pushed when IncludeArchived=true")
	}
	if applied.Has(FilterArchived) {
		t.Error("FilterArchived should not be marked applied when including archived")
	}
}
