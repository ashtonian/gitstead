package config

import "testing"

func TestValidateSelectorByProviderType(t *testing.T) {
	cases := []struct {
		name     string
		rule     DiscoveryRule
		provType string
		wantErr  bool
	}{
		{"org on gitlab", DiscoveryRule{Name: "r", Provider: "p", Org: "a"}, "gitlab", true},
		{"group on github", DiscoveryRule{Name: "r", Provider: "p", Group: "g"}, "github", true},
		{"group on gitea", DiscoveryRule{Name: "r", Provider: "p", Group: "g"}, "gitea", true},
		{"group on forgejo", DiscoveryRule{Name: "r", Provider: "p", Group: "g"}, "forgejo", true},
		{"two selectors", DiscoveryRule{Name: "r", Provider: "p", Org: "a", User: "u"}, "github", true},
		{"org on github ok", DiscoveryRule{Name: "r", Provider: "p", Org: "a"}, "github", false},
		{"group on gitlab ok", DiscoveryRule{Name: "r", Provider: "p", Group: "g"}, "gitlab", false},
		{"user on gitea ok", DiscoveryRule{Name: "r", Provider: "p", User: "u"}, "gitea", false},
		{"self on github ok", DiscoveryRule{Name: "r", Provider: "p"}, "github", false},
	}
	for _, c := range cases {
		cfg := Defaults()
		cfg.Providers = []Provider{{Name: "p", Type: c.provType, URL: "https://example.com"}}
		cfg.Discovery = []DiscoveryRule{c.rule}
		err := Validate(cfg)
		if (err != nil) != c.wantErr {
			t.Errorf("%s: got err=%v, wantErr=%v", c.name, err, c.wantErr)
		}
	}
}

func TestNormalizeProviderType(t *testing.T) {
	if got := NormalizeProviderType("forgejo"); got != "gitea" {
		t.Errorf("forgejo -> %q, want gitea", got)
	}
	if got := NormalizeProviderType("github"); got != "github" {
		t.Errorf("github -> %q, want github", got)
	}
}
