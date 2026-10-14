package config

import (
	"strings"
	"testing"
)

func TestNameMatches(t *testing.T) {
	for _, tt := range []struct {
		globs []string
		name  string
		want  bool
	}{
		{[]string{"*billing*"}, "billing-api", true},
		{[]string{"*billing*"}, "Rebilling", true},
		{[]string{"*BILLING*", "*payments*"}, "payments-ui", true},
		{[]string{"billing-*"}, "BILLING-Web", true},
		{[]string{"billing"}, "billing-api", false},
		{[]string{"svc-?"}, "svc-a", true},
		{nil, "anything", false},
	} {
		if got := NameMatches(tt.globs, tt.name); got != tt.want {
			t.Errorf("NameMatches(%q, %q) = %v, want %v", tt.globs, tt.name, got, tt.want)
		}
	}
}

// A malformed or empty glob is refused wherever config holds one, and
// names, like the other filters, can't be combined with a raw query.
func TestCheck_NameGlobs(t *testing.T) {
	for _, tt := range []struct {
		name string
		mod  func(*Config)
		err  string
	}{
		{"rule names", func(c *Config) { c.Discovery[0].Names = []string{"billing-[a"} }, `discovery rule "r": names: "billing-[a" is not a valid glob`},
		{"empty glob", func(c *Config) { c.Discovery[0].Names = []string{" "} }, "an empty name glob"},
		{"exclude names", func(c *Config) { c.Exclude = []ExcludeRule{{Names: []string{"[x"}}} }, "exclude[0]: names"},
		{"exclude repos", func(c *Config) { c.Exclude = []ExcludeRule{{Repos: []string{"github.com/[x"}}} }, "exclude[0].repos"},
		{"with a query", func(c *Config) {
			c.Discovery[0].Org, c.Discovery[0].Query, c.Discovery[0].Names = "", "org:acme", []string{"x*"}
		}, "'query' cannot be combined"},
		{"valid", func(c *Config) { c.Discovery[0].Names = []string{"*billing*", "payments-?"} }, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Defaults()
			cfg.Providers = []Provider{{Name: "gh", Type: "github"}}
			cfg.Discovery = []DiscoveryRule{{Name: "r", Provider: "gh", Org: "acme"}}
			tt.mod(cfg)
			err := Check(cfg)
			if tt.err == "" && err != nil || tt.err != "" && (err == nil || !strings.Contains(err.Error(), tt.err)) {
				t.Errorf("Check = %v, want %q", err, tt.err)
			}
		})
	}
}
