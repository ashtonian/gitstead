package config_test

import (
	"testing"

	"github.com/ashtonian/gitten/internal/config"
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

func TestResolveForRepo_BaseDir(t *testing.T) {
	cfg := &config.Config{
		BaseDir:   "/repos",
		Overrides: []config.Override{{Repos: []string{"github.com/work/*"}, BaseDir: "/work"}},
	}
	if got := cfg.ResolveForRepo("github.com/work/api").BaseDir; got != "/work" {
		t.Errorf("override base_dir: got %q, want /work", got)
	}
	if got := cfg.ResolveForRepo("github.com/personal/x").BaseDir; got != "/repos" {
		t.Errorf("default base_dir: got %q, want /repos", got)
	}
}
