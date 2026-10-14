package cmd

import (
	"fmt"
	"maps"
	"os"
	"slices"

	"github.com/ashtonian/gitstead/internal/config"
	"github.com/ashtonian/gitstead/internal/reconcile"
	"github.com/ashtonian/gitstead/internal/repourl"
	"github.com/ashtonian/gitstead/internal/state"
	"gopkg.in/yaml.v3"
)

func (c *ManifestExportCmd) Run(ctx *Context) error {
	cfg, err := ctx.LoadConfig()
	if err != nil {
		return err
	}
	st, err := ctx.LoadState()
	if err != nil {
		return err
	}
	keys, err := selectRepos(cfg, st, selection{c.Repos, c.Tag, c.Set})
	if err != nil {
		return err
	}

	var m config.Manifest
	for _, key := range keys {
		if e, ok := manifestEntry(cfg, key, st.Repos[key], st.Repos[key].Tags); ok {
			m.Repos = append(m.Repos, e)
		}
	}

	if m.Repos == nil {
		m.Repos = []config.ManualRepo{}
	}
	out, err := yaml.Marshal(m)
	if err != nil {
		return err
	}
	if c.Out == "" {
		if ctx.JSON {
			return ctx.Emit(m)
		}
		_, err = os.Stdout.Write(out)
		return err
	}
	if err := os.WriteFile(config.ExpandHome(c.Out), out, 0o600); err != nil {
		return err
	}
	if !ctx.Quiet {
		fmt.Printf("Exported %d repos to %s\n", len(m.Repos), c.Out)
	}
	if ctx.JSON {
		return ctx.Emit(map[string]any{"path": config.ExpandHome(c.Out), "repos": len(m.Repos)})
	}
	return nil
}

func (c *ManifestImportCmd) Run(ctx *Context) error {
	cfg, err := ctx.LoadConfig()
	if err != nil {
		return err
	}
	data, err := os.ReadFile(config.ExpandHome(c.File))
	if err != nil {
		return fmt.Errorf("reading manifest: %w", err)
	}
	var m config.Manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return fmt.Errorf("parsing manifest: %w", err)
	}

	// Importing locks the state per change, so this reads it unlocked.
	st, err := ctx.LoadState()
	if err != nil {
		return err
	}

	added, skipped := 0, 0
	var errs []error
	for _, mr := range m.Repos {
		if err := repourl.CheckNoCredentials(mr.URL); err != nil {
			errs = append(errs, err)
			continue
		}
		id, perr := repourl.Parse(mr.URL)
		if perr != nil {
			errs = append(errs, fmt.Errorf("%s: %w", mr.URL, perr))
			continue
		}
		if _, exists := st.Repos[id.Key()]; exists {
			skipped++
			continue
		}
		dest := cfg.ManualPath(id.Key(), mr.Path)
		if mr.Path == "" {
			var derr error
			if dest, derr = reconcile.TemplatePath(cfg, id.Key(), id.Host, id.Owner, id.Name, id.FullName); derr != nil {
				errs = append(errs, derr)
				continue
			}
		}
		// An entry's account or key is kept in config before its clone is
		// made, so the clone gets that account's identity and key, and
		// keeps them on every later run.
		if (mr.Account != "" || mr.SSHKey != "") && !c.DryRun {
			if err := keepBinding(ctx, cfg, id.Key(), mr); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", mr.URL, err))
				continue
			}
			if cfg, err = ctx.ReloadConfig(); err != nil {
				return err
			}
		}
		d := reconcile.HandAdded(cfg, id, dest, mr.Tags)
		if err := addByHand(ctx, cfg, st, d, c.DryRun); err != nil {
			errs = append(errs, err)
			continue
		}
		added++
	}
	if !c.DryRun {
		fmt.Printf("Imported %d repos (%d already tracked)\n", added, skipped)
	}
	for _, err := range errs {
		fmt.Fprintf(os.Stderr, "  %v\n", err)
		ctx.noteError(c.File, err)
	}
	return ctx.finishChanges(outcome(len(errs), added+len(errs), "manifest entries could not be imported"))
}

// manifestEntry is repo key's manifest entry: its URL, tags, its path when
// config wouldn't put it there, and the account or key its own repos: or
// manifest entry binds it to. A repo with no URL on record has none.
func manifestEntry(cfg *config.Config, key string, r *state.RepoState, tags []string) (config.ManualRepo, bool) {
	if r == nil || r.CloneURL == "" {
		return config.ManualRepo{}, false
	}
	templated, _ := reconcile.TemplatePath(cfg, key, r.Host, r.Owner, r.Name, r.FullName)
	base := config.ExpandHome(cfg.ResolveForRepo(key).BaseDir)
	e := config.ManualRepo{URL: r.CloneURL, Tags: tags, Path: config.PortablePath(r.LocalPath, templated, base)}
	if listed, ok := cfg.RepoEntry(key); ok {
		e.Account, e.SSHKey = listed.Account, listed.SSHKey
	}
	return e, true
}

// keepBinding saves the account or SSH key a manifest entry binds repo key
// to, where config reads it from: the shared manifest when config names one,
// else config's repos:.
func keepBinding(ctx *Context, cfg *config.Config, key string, mr config.ManualRepo) error {
	if mr.Account != "" && cfg.FindAccount(mr.Account) == nil {
		return fmt.Errorf("names unknown account %q", mr.Account)
	}
	entry := config.ManualRepo{URL: mr.URL, Tags: mr.Tags, Path: mr.Path, Account: mr.Account, SSHKey: mr.SSHKey}
	if cfg.Manifest != "" {
		return config.UpdateManifest(cfg.Manifest, func(m *config.Manifest) error {
			m.Upsert(entry)
			return nil
		})
	}
	doc, err := config.OpenDocument(ctx.ConfigPath())
	if err != nil {
		return err
	}
	if err := doc.Upsert("repos", config.MatchesRepo(key), entry); err != nil {
		return err
	}
	if err := doc.Save(ctx.GoCtx()); err != nil {
		return fmt.Errorf("its account or key must be kept in config, but adding it to %s's repos: failed (%w); add the entry there yourself, or point manifest: at a file to keep it in", ctx.ConfigPath(), err)
	}
	fmt.Printf("  Added %s to %s's repos:, for its account or key\n", key, ctx.ConfigPath())
	return nil
}

// recordInManifest adds the repos with keys, tracked by hand, to the shared
// manifest when config names one.
func recordInManifest(cfg *config.Config, st *state.State, keys ...string) error {
	if cfg.Manifest == "" {
		return nil
	}
	var entries []config.ManualRepo
	for _, key := range keys {
		r := st.Repos[key]
		if r == nil || !r.ManuallyAdded {
			continue
		}
		if e, ok := manifestEntry(cfg, key, r, r.UserTags); ok {
			entries = append(entries, e)
		}
	}
	if len(entries) == 0 {
		return nil
	}
	err := config.UpdateManifest(cfg.Manifest, func(m *config.Manifest) error {
		for _, e := range entries {
			m.Upsert(e)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("tracked, but not saved in the manifest %s: %w", cfg.Manifest, err)
	}
	return nil
}

// dropFromManifest removes the repos with keys from the shared manifest when
// config names one.
func dropFromManifest(cfg *config.Config, keys ...string) error {
	if cfg.Manifest == "" || len(keys) == 0 {
		return nil
	}
	err := config.UpdateManifest(cfg.Manifest, func(m *config.Manifest) error {
		for _, key := range keys {
			m.Remove(key)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("removed, but still in the manifest %s: %w", cfg.Manifest, err)
	}
	return nil
}

// Run saves each repo tracked by hand that neither config's repos: nor the
// manifest lists. With no manifest configured it prints them as repos:
// entries to add to config instead.
func (c *ManifestSaveCmd) Run(ctx *Context) error {
	cfg, err := ctx.LoadConfig()
	if err != nil {
		return err
	}
	st, err := ctx.LoadState()
	if err != nil {
		return err
	}
	listed := config.Manifest{Repos: cfg.Repos} // repos: entries and the manifest's
	var keys []string
	var entries []config.ManualRepo
	for _, key := range slices.Sorted(maps.Keys(st.Repos)) {
		r := st.Repos[key]
		if !r.ManuallyAdded || listed.Has(key) {
			continue
		}
		if e, ok := manifestEntry(cfg, key, r, r.UserTags); ok {
			keys, entries = append(keys, key), append(entries, e)
		}
	}
	doc := struct {
		Manifest string              `json:"manifest,omitempty"`
		Saved    bool                `json:"saved"`
		Repos    []config.ManualRepo `json:"repos"`
	}{cfg.Manifest, false, entries}
	if len(entries) == 0 {
		fmt.Println("Every repo tracked by hand is already in config or the manifest.")
		doc.Repos = []config.ManualRepo{}
		return ctx.finish(doc, nil)
	}
	if cfg.Manifest == "" || c.DryRun {
		if ctx.JSON {
			return ctx.Emit(doc)
		}
		if cfg.Manifest == "" {
			fmt.Fprintln(os.Stderr, "No manifest: in config; these repos: entries make other machines track them too:")
		}
		out, err := yaml.Marshal(config.Manifest{Repos: entries})
		if err != nil {
			return err
		}
		_, err = os.Stdout.Write(out)
		return err
	}
	if err := recordInManifest(cfg, st, keys...); err != nil {
		return err
	}
	fmt.Printf("Saved %d repos in %s\n", len(entries), tildify(cfg.Manifest))
	doc.Saved = true
	return ctx.finish(doc, nil)
}
