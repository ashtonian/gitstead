package config_test

import (
	"testing"

	"github.com/ashtonian/git-mgr/internal/config"
)

func TestCloneProtocolFor(t *testing.T) {
	cfg := &config.Config{
		CloneProtocol: "ssh",
		Providers:     []config.Provider{{Name: "gh", CloneProtocol: "https"}, {Name: "gl"}},
		Discovery:     []config.DiscoveryRule{{Name: "r-gh", Provider: "gh"}, {Name: "r-gl", Provider: "gl"}},
		Overrides:     []config.Override{{Repos: []string{"github.com/x/*"}, CloneProtocol: "ssh"}},
	}
	cases := []struct{ key, by, want string }{
		{"github.com/a/b", "r-gh", "https"}, // provider gh sets https
		{"gitlab.com/a/b", "r-gl", "ssh"},   // provider gl unset -> top-level default
		{"github.com/x/y", "r-gh", "ssh"},   // per-repo override beats provider
		{"foo", "nope", "ssh"},              // unknown rule -> top-level default
	}
	for _, c := range cases {
		if got := cfg.CloneProtocolFor(c.key, c.by); got != c.want {
			t.Errorf("CloneProtocolFor(%q, %q) = %q, want %q", c.key, c.by, got, c.want)
		}
	}
}
