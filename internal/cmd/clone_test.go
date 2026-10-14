package cmd

import (
	"strings"
	"testing"

	"github.com/ashtonian/gitstead/internal/config"
)

// A selection's rule is named after what it selects, and never takes a name
// config already has.
func TestRuleName(t *testing.T) {
	cfg := &config.Config{Discovery: []config.DiscoveryRule{{Name: "acme-go"}, {Name: "acme-go-2"}}}
	tests := []struct {
		name string
		rule config.DiscoveryRule
		mine bool
		want string
	}{
		{name: "org with filters", rule: config.DiscoveryRule{Org: "Acme", Languages: []string{"Go"}, Topics: []string{"api service"}}, want: "acme-go-api-service"},
		{name: "taken twice", rule: config.DiscoveryRule{Org: "acme", Languages: []string{"go"}}, want: "acme-go-3"},
		{name: "gitlab subgroup", rule: config.DiscoveryRule{Group: "acme/platform"}, want: "acme/platform"},
		{name: "org with name globs", rule: config.DiscoveryRule{Org: "acme", Names: []string{"*billing*", "*payments*"}}, want: "acme-billing-payments"},
		{name: "mine", mine: true, want: "mine"},
		{name: "search", rule: config.DiscoveryRule{Query: "org:acme stars:>50"}, want: "search"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ruleName(cfg, tt.rule, tt.mine); got != tt.want {
				t.Errorf("ruleName = %q, want %q", got, tt.want)
			}
		})
	}
}

// One repo by URL or a selection, never both, and a selection needs a
// selector, not just filters.
func TestCloneCmd_ArgumentsCheckedFirst(t *testing.T) {
	tests := []struct {
		name string
		cmd  CloneCmd
		want string
	}{
		{name: "nothing", cmd: CloneCmd{}, want: "give a repo URL"},
		{name: "filters without a selector", cmd: CloneCmd{Language: []string{"go"}}, want: "give a repo URL"},
		{name: "URL and selection", cmd: CloneCmd{URL: "git@github.com:a/b.git", Org: "acme"}, want: "not both"},
		{name: "URL and a filter", cmd: CloneCmd{URL: "git@github.com:a/b.git", Topic: []string{"x"}}, want: "not both"},
		{name: "path with a selection", cmd: CloneCmd{Org: "acme", Path: "/tmp/x"}, want: "--path places one repo"},
		{name: "save and no-save", cmd: CloneCmd{Org: "acme", Save: "x", NoSave: true}, want: "contradict"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cmd.Run(&Context{})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Run = %v, want an error about %q", err, tt.want)
			}
		})
	}
}

func TestSplitList(t *testing.T) {
	for in, want := range map[string]string{"": "", "Go": "Go", " Go , Rust,,": "Go|Rust", ",": ""} {
		if got := strings.Join(splitList(in), "|"); got != want {
			t.Errorf("splitList(%q) = %q, want %q", in, got, want)
		}
	}
}
