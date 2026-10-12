package config_test

import (
	"strings"
	"testing"

	"github.com/ashtonian/git-mgr/internal/config"
)

func accountsConfig() *config.Config {
	cfg := config.Defaults()
	cfg.Accounts = []config.Account{
		{Name: "personal", Hosts: []string{"github.com"}, SSHKey: "/k/personal", Git: config.GitIdentity{Email: "me@home"}},
		{Name: "work", Hosts: []string{"github.com"}, Owners: []string{"acme"}, SSHKey: "/k/work", Git: config.GitIdentity{Email: "me@acme"}},
		{Name: "lab", Hosts: []string{"gitlab.com"}, Owners: []string{"research"}},
		{Name: "ghe", SSHKey: "/k/ghe"},
	}
	cfg.Providers = []config.Provider{{Name: "ghes", Type: "github", URL: "https://ghes.corp", Account: "ghe"}}
	cfg.Discovery = []config.DiscoveryRule{
		{Name: "corp", Provider: "ghes"},
		{Name: "oss", Provider: "ghes", SSHKey: "/k/oss"},
	}
	return cfg
}

func TestAccountFor(t *testing.T) {
	cfg := accountsConfig()
	cfg.Overrides = []config.Override{{Repos: []string{"github.com/acme/public-*"}, Account: "personal"}}
	cfg.Repos = []config.ManualRepo{{URL: "git@github.com:acme/pinned.git", Account: "personal"}}

	tests := []struct {
		key, rule, account, sshKey string
	}{
		{"github.com/someone/dotfiles", "", "personal", "/k/personal"}, // the host-wide account
		{"github.com/acme/api", "", "work", "/k/work"},                 // the owner's account wins
		{"github.com/ACME/api", "", "work", "/k/work"},                 // owners match case-insensitively
		{"github.com/acme/public-site", "", "personal", "/k/personal"}, // an override wins
		{"github.com/acme/pinned", "", "personal", "/k/personal"},      // a repos: entry wins
		{"gitlab.com/research/vision/model", "", "lab", ""},            // a subgroup of the account's group
		{"gitlab.com/other/x", "", "", ""},                             // nothing covers it
		{"ghes.corp/team/svc", "corp", "ghe", "/k/ghe"},                // the rule's provider names it
		{"ghes.corp/team/svc", "oss", "ghe", "/k/oss"},                 // the rule's own key beats the provider's account
	}
	for _, tt := range tests {
		name := ""
		if a := cfg.AccountFor(tt.key, tt.rule); a != nil {
			name = a.Name
		}
		if name != tt.account {
			t.Errorf("AccountFor(%s, %q) = %q, want %q", tt.key, tt.rule, name, tt.account)
		}
		if got := cfg.ResolveSSHKey(tt.key, tt.rule); got != tt.sshKey {
			t.Errorf("ResolveSSHKey(%s, %q) = %q, want %q", tt.key, tt.rule, got, tt.sshKey)
		}
	}
}

func TestValidate_Accounts(t *testing.T) {
	tests := []struct {
		name   string
		modify func(*config.Config)
		want   string
	}{
		{"valid", func(*config.Config) {}, ""},
		{"duplicate name", func(c *config.Config) { c.Accounts = append(c.Accounts, config.Account{Name: "work"}) }, `duplicate account name: "work"`},
		{"two accounts claim one owner", func(c *config.Config) {
			c.Accounts = append(c.Accounts, config.Account{Name: "work2", Hosts: []string{"github.com"}, Owners: []string{"Acme"}})
		}, `accounts "work" and "work2" both cover github.com/Acme`},
		{"two host-wide accounts", func(c *config.Config) {
			c.Accounts = append(c.Accounts, config.Account{Name: "other", Hosts: []string{"github.com"}})
		}, `accounts "personal" and "other" both cover all of github.com`},
		{"unknown reference", func(c *config.Config) { c.Discovery[0].Account = "nope" }, `discovery rule corp names unknown account "nope"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := accountsConfig()
			tt.modify(cfg)
			err := config.Validate(cfg)
			switch {
			case tt.want == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)):
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}
