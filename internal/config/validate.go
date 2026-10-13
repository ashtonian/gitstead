package config

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
	"text/template"

	"github.com/go-playground/validator/v10"

	"github.com/ashtonian/git-mgr/internal/repourl"
)

// --- Validation ---

// Validate checks the config using go-playground/validator struct tags
// plus custom cross-field validation.
func Validate(cfg *Config) error {
	v := validator.New()

	// Register custom validator for regex patterns
	_ = v.RegisterValidation("valid_regex", func(fl validator.FieldLevel) bool {
		_, err := regexp.Compile(fl.Field().String())
		return err == nil
	})

	// Struct tag validation (required, oneof, min, max, etc.)
	if err := v.Struct(cfg); err != nil {
		if validationErrors, ok := err.(validator.ValidationErrors); ok {
			return formatValidationErrors(validationErrors)
		}
		return fmt.Errorf("validation: %w", err)
	}

	if err := validatePatterns(cfg); err != nil {
		return err
	}

	// Cross-field validation
	if t, ok := Layouts[cfg.Layout]; ok && cfg.PathTemplate != DefaultPathTemplate && cfg.PathTemplate != t {
		return fmt.Errorf("set layout or path_template, not both (layout %q, path_template %q)", cfg.Layout, cfg.PathTemplate)
	}
	if _, err := template.New("test").Parse(cfg.PathTemplate); err != nil {
		return fmt.Errorf("invalid path_template %q: %w", cfg.PathTemplate, err)
	}

	// Check duplicate provider names, capturing each provider's type.
	provType := make(map[string]string)
	for _, p := range cfg.Providers {
		if _, ok := provType[p.Name]; ok {
			return fmt.Errorf("duplicate provider name: %q", p.Name)
		}
		sources := 0
		for _, s := range []string{p.Token, p.TokenEnv, p.TokenCommand} {
			if s != "" {
				sources++
			}
		}
		if sources > 1 {
			return fmt.Errorf("provider %q: set one of token, token_env and token_command, not %d", p.Name, sources)
		}
		provType[p.Name] = NormalizeProviderType(p.Type)
	}

	// Check discovery rules reference valid providers and use selectors that are
	// valid for the provider's type.
	for _, r := range cfg.Discovery {
		typ, ok := provType[r.Provider]
		if !ok {
			return fmt.Errorf("discovery rule %q references unknown provider %q (available: %v)",
				r.Name, r.Provider, providerNames(cfg.Providers))
		}
		if err := validateSelector(r, typ); err != nil {
			return err
		}
	}

	if err := validateAccounts(cfg); err != nil {
		return err
	}
	if err := validateRemoteSync(cfg); err != nil {
		return err
	}
	for _, mr := range cfg.Repos {
		if err := checkManualRepo(mr); err != nil {
			return err
		}
	}
	if err := validateWorkspaces(cfg); err != nil {
		return err
	}
	if err := validateClone(cfg); err != nil {
		return err
	}
	if err := validateHooks(cfg); err != nil {
		return err
	}
	if err := validateFleets(cfg); err != nil {
		return err
	}

	return nil
}

// filterRE matches the partial clone filters git-mgr accepts.
var filterRE = regexp.MustCompile(`^(blob:none|blob:limit=[0-9]+[kmg]?|tree:[0-9]+)$`)

// validateClone checks clone filters and sparse directories wherever they
// are set.
func validateClone(cfg *Config) error {
	check := func(where string, filter *string, sparse *[]string) error {
		if filter != nil && *filter != "" && !filterRE.MatchString(*filter) {
			return fmt.Errorf("%s: clone.filter %q: use blob:none, blob:limit=<size> or tree:<depth>", where, *filter)
		}
		if sparse != nil {
			for _, dir := range *sparse {
				if err := checkSparseDir(dir); err != nil {
					return fmt.Errorf("%s: clone.sparse: %w", where, err)
				}
			}
		}
		return nil
	}
	if err := check("config", &cfg.Clone.Filter, &cfg.Clone.Sparse); err != nil {
		return err
	}
	for _, a := range cfg.Accounts {
		if a.Clone != nil {
			if err := check(fmt.Sprintf("account %q", a.Name), a.Clone.Filter, a.Clone.Sparse); err != nil {
				return err
			}
		}
	}
	for _, o := range cfg.Overrides {
		if o.Clone != nil {
			if err := check(fmt.Sprintf("override %v", o.Repos), o.Clone.Filter, o.Clone.Sparse); err != nil {
				return err
			}
		}
	}
	return nil
}

// checkSparseDir accepts a directory a cone-mode sparse checkout can name:
// relative to the repo's top, in clean form, with no wildcards.
func checkSparseDir(dir string) error {
	switch {
	case dir == "" || dir == ".":
		return errors.New("an empty directory checks out everything; leave it out")
	case strings.HasPrefix(dir, "/"):
		return fmt.Errorf("%q: give the directory relative to the repo's top, without a leading /", dir)
	case dir == ".." || strings.HasPrefix(dir, "../"):
		return fmt.Errorf("%q is outside the repo", dir)
	case strings.ContainsAny(dir, "*?[]\\!"):
		return fmt.Errorf("%q: name directories, without wildcards", dir)
	case path.Clean(dir) != dir:
		return fmt.Errorf("%q: write it as %q", dir, path.Clean(dir))
	}
	return nil
}

func formatValidationErrors(errs validator.ValidationErrors) error {
	var msgs []string
	for _, e := range errs {
		field := strings.TrimPrefix(e.Namespace(), "Config.")
		switch e.Tag() {
		case "required":
			msgs = append(msgs, fmt.Sprintf("%s is required", field))
		case "oneof":
			msgs = append(msgs, fmt.Sprintf("%s must be one of [%s] (got %q)", field, e.Param(), e.Value()))
		case "min":
			msgs = append(msgs, fmt.Sprintf("%s must be >= %s (got %v)", field, e.Param(), e.Value()))
		case "max":
			msgs = append(msgs, fmt.Sprintf("%s must be <= %s (got %v)", field, e.Param(), e.Value()))
		case "valid_regex":
			msgs = append(msgs, fmt.Sprintf("%s contains invalid regex %q", field, e.Value()))
		default:
			msgs = append(msgs, fmt.Sprintf("%s: failed %s validation", field, e.Tag()))
		}
	}
	return fmt.Errorf("config validation failed:\n  %s", strings.Join(msgs, "\n  "))
}

func providerNames(providers []Provider) []string {
	names := make([]string, len(providers))
	for i, p := range providers {
		names[i] = p.Name
	}
	return names
}

// NormalizeProviderType maps provider type aliases to their canonical form
// ("forgejo" -> "gitea"). Unknown types are returned unchanged.
func NormalizeProviderType(t string) string {
	if t == "forgejo" {
		return "gitea"
	}
	return t
}

// validateSelector ensures a discovery rule uses selectors valid for the
// provider type: github/gitea use user/org; gitlab uses user/group. At most one
// selector may be set (empty = the authenticated user's own repos).
func validateSelector(r DiscoveryRule, typ string) error {
	// A raw search query owns the whole request: it is mutually exclusive with
	// scope selectors and with the structured filters (which it expresses itself).
	if r.Query != "" {
		if r.User != "" || r.Org != "" || r.Group != "" {
			return fmt.Errorf("discovery rule %q: 'query' cannot be combined with user/org/group", r.Name)
		}
		if r.Visibility != "" || len(r.Languages) > 0 || len(r.Topics) > 0 || r.NamePattern != "" {
			return fmt.Errorf("discovery rule %q: 'query' cannot be combined with visibility/languages/topics/name_pattern", r.Name)
		}
		return nil
	}
	set := 0
	for _, s := range []string{r.User, r.Org, r.Group} {
		if s != "" {
			set++
		}
	}
	if set > 1 {
		return fmt.Errorf("discovery rule %q: set at most one of user/org/group", r.Name)
	}
	if len(r.Affiliation) > 0 && (typ != "github" || set > 0) {
		return fmt.Errorf("discovery rule %q: 'affiliation' only applies to a github rule without user/org/group", r.Name)
	}
	switch typ {
	case "gitlab":
		if r.Org != "" {
			return fmt.Errorf("discovery rule %q: 'org' is not valid for a gitlab provider (use 'group')", r.Name)
		}
	case "github", "gitea":
		if r.Group != "" {
			return fmt.Errorf("discovery rule %q: 'group' is not valid for a %s provider (use 'org')", r.Name, typ)
		}
	}
	return nil
}

// validateAccounts checks account names are unique, every `account:`
// reference names one, and no two accounts claim the same owner on a host.
func validateAccounts(cfg *Config) error {
	names := map[string]bool{}
	claims := map[string]string{} // host + "\x00" + lower-case owner ("" for the whole host) -> account
	for _, a := range cfg.Accounts {
		if names[a.Name] {
			return fmt.Errorf("duplicate account name: %q", a.Name)
		}
		if a.Signing.Mode == "required" && a.Signing.Key == "" && a.Signing.SigningFormat() != "openpgp" {
			return fmt.Errorf("account %q: signing.mode required with format %s needs signing.key", a.Name, a.Signing.SigningFormat())
		}
		names[a.Name] = true
		owners := a.Owners
		if len(owners) == 0 {
			owners = []string{""}
		}
		for _, h := range a.Hosts {
			for _, o := range owners {
				claim := h + "\x00" + strings.ToLower(o)
				if other, ok := claims[claim]; ok {
					what := "all of " + h
					if o != "" {
						what = h + "/" + o
					}
					return fmt.Errorf("accounts %q and %q both cover %s", other, a.Name, what)
				}
				claims[claim] = a.Name
			}
		}
	}
	check := func(where, name string) error {
		if name != "" && !names[name] {
			return fmt.Errorf("%s names unknown account %q", where, name)
		}
		return nil
	}
	for _, p := range cfg.Providers {
		if err := check("provider "+p.Name, p.Account); err != nil {
			return err
		}
	}
	for _, r := range cfg.Discovery {
		if err := check("discovery rule "+r.Name, r.Account); err != nil {
			return err
		}
	}
	for _, mr := range cfg.Repos {
		if err := check("repos entry "+mr.URL, mr.Account); err != nil {
			return err
		}
	}
	for _, o := range cfg.Overrides {
		if err := check(fmt.Sprintf("override %v", o.Repos), o.Account); err != nil {
			return err
		}
	}
	for _, s := range cfg.Sets {
		for _, name := range s.Accounts {
			if err := check("set "+s.Name, name); err != nil {
				return err
			}
		}
		for _, rule := range s.Rules {
			if !slices.ContainsFunc(cfg.Discovery, func(r DiscoveryRule) bool { return r.Name == rule }) {
				return fmt.Errorf("set %s names unknown discovery rule %q", s.Name, rule)
			}
		}
	}
	return nil
}

// validatePatterns checks every glob over repo keys config holds: overrides',
// sets' and workspaces' (see repourl.MatchKey).
func validatePatterns(cfg *Config) error {
	check := func(where string, patterns []string) error {
		for _, p := range patterns {
			if err := repourl.ValidatePattern(p); err != nil {
				return fmt.Errorf("%s: %w", where, err)
			}
		}
		return nil
	}
	for i, o := range cfg.Overrides {
		if err := check(fmt.Sprintf("overrides[%d].repos", i), o.Repos); err != nil {
			return err
		}
	}
	for _, s := range cfg.Sets {
		if err := check("set "+s.Name, s.Repos); err != nil {
			return err
		}
	}
	for _, w := range cfg.Workspaces {
		if err := check("workspace "+w.Name, w.Repos); err != nil {
			return err
		}
	}
	return nil
}
