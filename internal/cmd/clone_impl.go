package cmd

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"charm.land/huh/v2"
	"golang.org/x/term"

	"github.com/ashtonian/gitten/internal/config"
	"github.com/ashtonian/gitten/internal/discovery"
	"github.com/ashtonian/gitten/internal/reconcile"
	"github.com/ashtonian/gitten/internal/repourl"
	"github.com/ashtonian/gitten/internal/shellutil"
	"github.com/ashtonian/gitten/internal/state"
)

func (c *CloneCmd) Run(ctx *Context) error {
	selecting := c.Org != "" || c.User != "" || c.Group != "" || c.Query != "" || c.Mine
	filtering := len(c.Language) > 0 || len(c.Topic) > 0 || len(c.Name) > 0 || c.NamePattern != "" || c.Visibility != "" || c.Forks || c.Archived ||
		c.Provider != "" || c.Save != "" || c.NoSave || c.DryRun
	switch {
	case c.URL != "" && (selecting || filtering):
		return errors.New("give a repo URL or a selection (--org, --user, --group, --mine, --query), not both")
	case c.URL == "" && !selecting:
		return errors.New("give a repo URL, or select repos with --org, --user, --group, --mine or --query")
	case selecting && c.Path != "":
		return errors.New("--path places one repo; a selection's repos go where config puts each")
	case c.Save != "" && c.NoSave:
		return errors.New("--save and --no-save contradict each other")
	}
	if selecting {
		return c.cloneSelection(ctx)
	}

	cfg, err := ctx.LoadConfig()
	if err != nil {
		return err
	}
	st, err := ctx.LoadState()
	if err != nil {
		return err
	}

	if err := repourl.CheckNoCredentials(c.URL); err != nil {
		return err
	}
	id, err := repourl.Parse(c.URL)
	if err != nil {
		return fmt.Errorf("parsing URL: %w", err)
	}
	if _, exists := st.Repos[id.Key()]; exists {
		return fmt.Errorf("repo %s is already tracked", id.Key())
	}

	dest := c.Path
	if dest == "" {
		if dest, err = reconcile.TemplatePath(cfg, id.Key(), id.Host, id.Owner, id.Name, id.FullName); err != nil {
			return err
		}
	} else if dest, err = filepath.Abs(config.ExpandHome(dest)); err != nil {
		return err
	}
	return ctx.finishChanges(addByHand(ctx, cfg, st, reconcile.HandAdded(cfg, id, dest, splitTags(c.Tag)), false))
}

// addByHand plans tracking one repo added by hand -- cloning it, or adopting
// the clone already at its path -- and applies the plan unless dryRun. A repo
// it tracks goes in the shared manifest, if config names one, so every
// machine reading it has the repo too.
func addByHand(ctx *Context, cfg *config.Config, st *state.State, d reconcile.Repo, dryRun bool) error {
	desired := map[string]reconcile.Repo{d.Key: d}
	seen := reconcile.Observe(ctx.GoCtx(), reconcile.RepoPaths(desired, st), 1, cfg.Sync.InspectTimeout)
	plan := reconcile.PlanRepos(desired, st, seen, false)
	if dryRun {
		printPlan(plan, "all")
		return blockedErr(plan)
	}
	printDriftAndBlocked(plan)
	results := plan.Apply(ctx.GoCtx(), reconcileEnv(cfg, st), reconcile.All)
	ctx.noteChanges(plan, results)
	printResults(results, resultsEach)
	return errors.Join(changesOutcome(results, plan), recordInManifest(cfg, st, d.Key))
}

func splitTags(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}

// cloneSelection clones every repo the selection finds that isn't tracked
// yet. Saved as a discovery rule, the selection keeps working: every sync
// clones what newly matches. Unsaved, its repos are added by hand -- kept
// where they are, never judged stale, and in the shared manifest when there
// is one -- as if each had been cloned by its URL.
func (c *CloneCmd) cloneSelection(ctx *Context) error {
	cfg, err := ctx.LoadConfig()
	if err != nil {
		return err
	}
	st, err := ctx.LoadState()
	if err != nil {
		return err
	}
	provider, inConfig, err := c.provider(ctx, cfg)
	if err != nil {
		return err
	}
	rule := c.rule(cfg, provider)
	trial := withRule(cfg, provider, inConfig, rule)
	if err := config.Check(trial); err != nil {
		return fmt.Errorf("the selection: %w", err)
	}

	if !ctx.Quiet {
		fmt.Printf("Searching %s...\n", provider.Name)
	}
	results, discovered, err := discovery.RunDiscovery(ctx.GoCtx(), trial, rule.Name, "")
	if err != nil {
		return err
	}
	for _, r := range results {
		if r.Err != nil {
			return r.Err
		}
		for _, w := range r.Warnings {
			fmt.Fprintf(os.Stderr, "warning: %s\n", w)
		}
		for _, why := range r.Incomplete {
			fmt.Fprintf(os.Stderr, "warning: the search may be missing repos: %s\n", why)
		}
	}
	var found, tracked []string
	for _, key := range slices.Sorted(maps.Keys(discovered)) {
		if st.Repos[key] != nil {
			tracked = append(tracked, key)
		} else {
			found = append(found, key)
		}
	}
	fmt.Printf("%d %s match", len(discovered), plural(len(discovered), "repo", "repos"))
	if len(tracked) > 0 {
		fmt.Printf(", %d tracked already", len(tracked))
	}
	fmt.Println(":")
	for _, key := range found {
		fmt.Printf("  + %s\n", key)
	}
	if c.DryRun {
		return ctx.finish(map[string]any{"rule": rule.Name, "provider": provider.Name, "new": nonNil(found), "tracked": nonNil(tracked)}, nil)
	}

	save := c.Save != ""
	if !save && !c.NoSave && !ctx.JSON && term.IsTerminal(int(os.Stdin.Fd())) {
		save = true
		err := huh.NewConfirm().
			Title(fmt.Sprintf("Keep this selection, as discovery rule %q?", rule.Name)).
			Description("Every sync then clones what newly matches. Without it, these repos are tracked as cloned by hand.").
			Value(&save).Run()
		if err != nil && !errors.Is(err, huh.ErrUserAborted) {
			return err
		}
		if errors.Is(err, huh.ErrUserAborted) {
			fmt.Println("Cancelled; nothing cloned.")
			return nil
		}
	}
	if save {
		if err := saveRule(ctx, provider, inConfig, rule); err != nil {
			return err
		}
		fmt.Printf("Saved discovery rule %q in %s\n", rule.Name, tildify(ctx.ConfigPath()))
		if cfg, err = ctx.ReloadConfig(); err != nil {
			return err
		}
		trial = cfg
	}
	if len(found) == 0 {
		fmt.Println("Nothing new to clone.")
		return ctx.finishChanges(nil)
	}

	desired, blocked := reconcile.Repos(trial, st, discovered, discovery.StaleScope{})
	for key, d := range desired {
		if !slices.Contains(found, key) {
			delete(desired, key)
			continue
		}
		if !save {
			// Added by hand: the rule goes with this run, so its tags become
			// the repo's own and its path and URL the recorded ones.
			d.Pinned, d.Rule, d.UserTags = true, "", rule.Tags
			desired[key] = d
		}
	}
	seen := reconcile.Observe(ctx.GoCtx(), reconcile.RepoPaths(desired, st), cfg.Concurrency, cfg.Sync.InspectTimeout)
	plan := reconcile.PlanRepos(desired, st, seen, false)
	plan.Blocked = append(plan.Blocked, blocked...)
	printDriftAndBlocked(plan)
	applied := plan.Apply(ctx.GoCtx(), reconcileEnv(cfg, st), reconcile.Safe)
	ctx.noteChanges(plan, applied)
	printResults(applied, resultsEach)
	errs := []error{changesOutcome(applied, plan)}
	if !save {
		for _, key := range found {
			errs = append(errs, recordInManifest(cfg, st, key))
		}
	}
	return ctx.finishChanges(errors.Join(errs...))
}

// provider is the provider a selection searches: --provider's, the only
// configured one, or, with none configured, github.com, reading its token
// from gh when gh is logged in there and GITHUB_TOKEN isn't set. inConfig
// reports whether config has it already.
func (c *CloneCmd) provider(ctx *Context, cfg *config.Config) (p config.Provider, inConfig bool, err error) {
	names := make([]string, len(cfg.Providers))
	for i, p := range cfg.Providers {
		if p.Name == c.Provider {
			return p, true, nil
		}
		names[i] = p.Name
	}
	switch {
	case c.Provider != "":
		return p, false, fmt.Errorf("no provider %q in config (it has: %s)", c.Provider, orDefault(strings.Join(names, ", "), "none"))
	case len(cfg.Providers) == 1:
		return cfg.Providers[0], true, nil
	case len(cfg.Providers) > 1:
		return p, false, fmt.Errorf("config has several providers (%s); pick one with --provider", strings.Join(names, ", "))
	}
	p = config.Provider{Name: "github.com", Type: "github"}
	if os.Getenv("GITHUB_TOKEN") == "" {
		if login, ok := detectGitHubCLI(ctx); ok {
			p.TokenCommand = "gh auth token --hostname github.com --user " + shellutil.Quote(login)
		}
	}
	return p, false, nil
}

// rule is the discovery rule the selection describes, named by --save or
// after what it selects.
func (c *CloneCmd) rule(cfg *config.Config, provider config.Provider) config.DiscoveryRule {
	r := config.DiscoveryRule{
		Provider: provider.Name, Org: c.Org, User: c.User, Group: c.Group, Query: c.Query,
		Languages: c.Language, Topics: c.Topic, Names: c.Name, NamePattern: c.NamePattern, Visibility: c.Visibility,
		IncludeForks: c.Forks, IncludeArchived: c.Archived, Tags: splitTags(c.Tag),
	}
	if c.Mine && config.NormalizeProviderType(provider.Type) == "github" {
		r.Affiliation = []string{"owner"}
	}
	r.Name = c.Save
	if r.Name == "" {
		r.Name = ruleName(cfg, r, c.Mine)
	}
	return r
}

// ruleName names a selection after what it selects -- acme-go-service,
// acme-billing-payments -- unique among config's rules.
func ruleName(cfg *config.Config, r config.DiscoveryRule, mine bool) string {
	var parts []string
	switch {
	case r.Org != "":
		parts = append(parts, r.Org)
	case r.User != "":
		parts = append(parts, r.User)
	case r.Group != "":
		parts = append(parts, r.Group)
	case mine:
		parts = append(parts, "mine")
	default:
		parts = append(parts, "search")
	}
	parts = append(append(append(parts, r.Languages...), r.Topics...), r.Names...)
	base := strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(strings.Join(parts, "-")), "-"), "-")
	taken := func(name string) bool {
		return slices.ContainsFunc(cfg.Discovery, func(d config.DiscoveryRule) bool { return d.Name == name })
	}
	name := base
	for i := 2; taken(name); i++ {
		name = fmt.Sprintf("%s-%d", base, i)
	}
	return name
}

var nonSlug = regexp.MustCompile(`[^a-z0-9/]+`)

// withRule is cfg with rule, and its provider when config lacks it.
func withRule(cfg *config.Config, provider config.Provider, inConfig bool, rule config.DiscoveryRule) *config.Config {
	trial := *cfg
	if !inConfig {
		trial.Providers = append(slices.Clone(cfg.Providers), provider)
	}
	trial.Discovery = append(slices.Clone(cfg.Discovery), rule)
	return &trial
}

// saveRule writes rule, and its provider when config lacks it, into the
// config file, editing it in place as setup does, after checking the result
// loads.
func saveRule(ctx *Context, provider config.Provider, inConfig bool, rule config.DiscoveryRule) error {
	doc, err := config.OpenDocument(ctx.ConfigPath())
	if err != nil {
		return fmt.Errorf("saving discovery rule %q: %w", rule.Name, err)
	}
	if !inConfig {
		if err := doc.Upsert("providers", config.Named(provider.Name), provider); err != nil {
			return err
		}
	}
	if err := doc.Upsert("discovery", config.Named(rule.Name), rule); err != nil {
		return err
	}
	data, err := doc.Bytes()
	if err != nil {
		return err
	}
	if _, err := loadCandidate(ctx.GoCtx(), data); err != nil {
		return fmt.Errorf("saving discovery rule %q would make an invalid config: %w", rule.Name, err)
	}
	if err := doc.Save(ctx.GoCtx()); err != nil {
		return fmt.Errorf("saving discovery rule %q in %s (clone without saving with --no-save): %w", rule.Name, ctx.ConfigPath(), err)
	}
	return nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
