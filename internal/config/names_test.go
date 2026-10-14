package config

import (
	"strings"
	"testing"
)

func TestNameMatches(t *testing.T) {
	for _, tt := range []struct {
		patterns []string
		name     string
		want     bool
	}{
		// A pattern without a wildcard is a run of whole words.
		{[]string{"ops"}, "ops-tools", true},
		{[]string{"ops"}, "platform-ops", true},
		{[]string{"ops"}, "OpsPortal", true},
		{[]string{"ops"}, "platform_ops.v2", true},
		{[]string{"ops"}, "devops", false},
		{[]string{"ops"}, "ops2", true},
		{[]string{"ops"}, "dev-OPS", true},
		{[]string{"server"}, "HTTPServer", true},
		{[]string{"platform-ops"}, "team-platform-ops", true},
		{[]string{"platform-ops"}, "platform-team-ops", false},
		{[]string{"payments"}, "prepayments", false},
		{[]string{"payments", "billing"}, "billing-api", true},
		// With one, it is a glob over the whole name.
		{[]string{"*bill*"}, "Rebilling", true},
		{[]string{"billing-*"}, "BILLING-Web", true},
		{[]string{"svc-?"}, "svc-a", true},
		{[]string{"svc-*"}, "my-svc-a", false},
		{nil, "anything", false},
		// The README's "Matching names" table.
		{[]string{"billing"}, "billing", true},
		{[]string{"billing"}, "team-billing", true},
		{[]string{"billing"}, "BillingPortal", true},
		{[]string{"billing"}, "billing_v2", true},
		{[]string{"billing"}, "rebilling", false},
		{[]string{"billing"}, "billings", false},
		{[]string{"billing-api"}, "old-billing-api-v2", true},
		{[]string{"billing-api"}, "billing-web-api", false},
		{[]string{"billing-*"}, "team-billing-api", false},
	} {
		if got := NameMatches(tt.patterns, tt.name); got != tt.want {
			t.Errorf("NameMatches(%q, %q) = %v, want %v", tt.patterns, tt.name, got, tt.want)
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
		{"empty pattern", func(c *Config) { c.Discovery[0].Names = []string{" "} }, "an empty name pattern"},
		{"no word", func(c *Config) { c.Discovery[0].Names = []string{"--"} }, "no letters or digits"},
		{"empty skip", func(c *Config) { c.Discovery[0].Skip = []string{""} }, `discovery rule "r": skip`},
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
