package setup

import (
	"fmt"
	"slices"

	"gopkg.in/yaml.v3"

	"github.com/ashtonian/gitstead/internal/config"
)

// Render writes answers into a config document in place. Everything setup
// doesn't manage is left as it is, comments included; each account's own
// account, provider, rules and base-dir override are replaced, and rules of
// its provider for owners no longer tracked are removed.
func Render(doc *config.Document, a Answers) error {
	if err := a.Validate(); err != nil {
		return err
	}
	set := func(key string, value any) error { return doc.Set(key, value) }
	if err := set("base_dir", a.BaseDir); err != nil {
		return err
	}
	if a.Layout != "custom" {
		if err := set("layout", a.Layout); err != nil {
			return err
		}
		doc.Delete("path_template")
	}
	for _, acct := range a.Accounts {
		if err := renderAccount(doc, acct); err != nil {
			return fmt.Errorf("account %q: %w", acct.Name, err)
		}
	}
	for _, kv := range []struct {
		key   string
		value any
	}{
		{"backup.enabled", a.Backups},
		{"remote_sync.enabled", a.RemoteSync},
		{"sync.update", map[bool]string{true: "ff-only-clean", false: "off"}[a.Update]},
		{"reconcile.auto", a.ReconcileAuto},
		{"cleanup.policy", a.Cleanup},
	} {
		if err := set(kv.key, kv.value); err != nil {
			return err
		}
	}
	if w := a.Workspaces; w != nil {
		for _, p := range w.Profiles {
			if err := doc.Upsert("agents", config.Named(p.Name), p); err != nil {
				return err
			}
		}
		for _, kv := range []struct {
			key   string
			value any
		}{
			{"workspace_defaults.root", w.Root},
			{"workspace_defaults.agents", w.Agents},
			{"launchers.default", w.Launcher},
		} {
			if err := set(kv.key, kv.value); err != nil {
				return err
			}
		}
		if w.Agent != "" {
			if err := set("workspace_defaults.agent", w.Agent); err != nil {
				return err
			}
		} else {
			doc.Delete("workspace_defaults.agent")
		}
	}
	return nil
}

func renderAccount(doc *config.Document, acct Account) error {
	account := config.Account{
		Name: acct.Name, Hosts: []string{acct.Host}, Owners: acct.Owners(),
		SSHKey: acct.SSHKey, Git: acct.Git, Signing: acct.Signing,
	}
	if err := doc.Upsert("accounts", config.Named(acct.Name), account); err != nil {
		return err
	}
	provider, rules := acct.Discovery()
	if err := doc.Upsert("providers", config.Named(acct.Name), provider); err != nil {
		return err
	}
	var names []string
	for _, rule := range rules {
		names = append(names, rule.Name)
		if err := doc.Upsert("discovery", config.Named(rule.Name), rule); err != nil {
			return err
		}
	}
	doc.Remove("discovery", func(n *yaml.Node) bool {
		return config.Field("provider", acct.Name)(n) && !slices.ContainsFunc(names, func(r string) bool { return config.Named(r)(n) })
	})

	if acct.BaseDir == "" {
		return nil
	}
	var globs []string
	for _, owner := range acct.Owners() {
		globs = append(globs, acct.Host+"/"+owner+"/*", acct.Host+"/"+owner+"/*/*")
	}
	override := config.Override{Repos: globs, BaseDir: acct.BaseDir, Account: acct.Name}
	return doc.Upsert("overrides", config.Field("account", acct.Name), override)
}

// Discovery is the provider an account's repos are found through, and a
// discovery rule for each owner it tracks.
func (acct Account) Discovery() (config.Provider, []config.DiscoveryRule) {
	provider := config.Provider{
		Name: acct.Name, Type: acct.Type, URL: acct.URL, Account: acct.Name,
		TokenCommand: acct.TokenCommand, TokenEnv: acct.TokenEnv, CloneProtocol: acct.Protocol,
	}
	var rules []config.DiscoveryRule
	for _, t := range acct.Track {
		rule := config.DiscoveryRule{Name: ruleName(acct, t), Provider: acct.Name, Tags: []string{acct.Name},
			Languages: t.Languages, Topics: t.Topics, Names: t.Names, NamePattern: t.NamePattern}
		switch {
		case t.Kind == "org":
			rule.Org = t.Owner
		case t.Kind == "group":
			rule.Group = t.Owner
		case acct.Type == "github":
			rule.Affiliation = []string{"owner"} // the token's own repos, private ones included
		case acct.Type == "gitlab":
			rule.User = t.Owner
		}
		rules = append(rules, rule)
	}
	return provider, rules
}

// ruleName names an account's rule for an owner: the account's own repos are
// the account's name, an organization's are account/org.
func ruleName(acct Account, t Track) string {
	if t.Kind == "self" {
		return acct.Name
	}
	return acct.Name + "/" + t.Owner
}
