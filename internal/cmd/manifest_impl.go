package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ashtonian/git-mgr/internal/config"
	"github.com/ashtonian/git-mgr/internal/reconcile"
	"github.com/ashtonian/git-mgr/internal/repourl"
	"github.com/ashtonian/git-mgr/internal/state"
	"gopkg.in/yaml.v3"
)

// manifest is the portable description of a tracked repo set: enough to rehydrate
// it on a new machine, with no machine-local fields (timestamps, head commits).
type manifest struct {
	Repos []manifestRepo `yaml:"repos"`
}

type manifestRepo struct {
	URL  string   `yaml:"url"`
	Tags []string `yaml:"tags,omitempty"`
	Path string   `yaml:"path,omitempty"` // relative to base_dir, or absolute if outside it
}

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

	base := config.ExpandHome(cfg.BaseDir)
	var m manifest
	for _, key := range keys {
		r := st.Repos[key]
		if r.CloneURL == "" {
			continue
		}
		m.Repos = append(m.Repos, manifestRepo{URL: r.CloneURL, Tags: r.Tags, Path: relToBase(r.LocalPath, base)})
	}

	out, err := yaml.Marshal(m)
	if err != nil {
		return err
	}
	if c.Out == "" {
		_, err = os.Stdout.Write(out)
		return err
	}
	if err := os.WriteFile(config.ExpandHome(c.Out), out, 0o600); err != nil {
		return err
	}
	if !ctx.Quiet {
		fmt.Printf("Exported %d repos to %s\n", len(m.Repos), c.Out)
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
	var m manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return fmt.Errorf("parsing manifest: %w", err)
	}

	// Read-only when --dry-run; otherwise mutate under the state lock.
	var st *state.State
	var unlock func()
	if c.DryRun {
		st, err = ctx.LoadState()
	} else {
		st, unlock, err = lockedState(ctx)
		if err == nil {
			defer unlock()
		}
	}
	if err != nil {
		return err
	}

	added, skipped := 0, 0
	var errs []error
	for _, mr := range m.Repos {
		id, perr := repourl.Parse(mr.URL)
		if perr != nil {
			errs = append(errs, fmt.Errorf("%s: %w", mr.URL, perr))
			continue
		}
		if _, exists := st.Repos[id.Key()]; exists {
			skipped++
			continue
		}
		dest, derr := manifestDest(cfg, mr, id)
		if derr != nil {
			errs = append(errs, derr)
			continue
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
	return errors.Join(errs...)
}

// relToBase returns localPath relative to base when it lives under base, else
// the absolute path (so repos outside base_dir round-trip too).
func relToBase(localPath, base string) string {
	if rel, err := filepath.Rel(base, localPath); err == nil && !strings.HasPrefix(rel, "..") {
		return rel
	}
	return localPath
}

// manifestDest resolves the clone destination for a manifest entry: an explicit
// path (relative to the repo's base_dir, or absolute), otherwise where config
// puts the repo.
func manifestDest(cfg *config.Config, mr manifestRepo, id *repourl.Identity) (string, error) {
	if mr.Path == "" {
		return reconcile.TemplatePath(cfg, id.Key(), id.Host, id.Owner, id.Name, id.FullName)
	}
	if filepath.IsAbs(mr.Path) {
		return mr.Path, nil
	}
	return filepath.Join(config.ExpandHome(cfg.ResolveForRepo(id.Key()).BaseDir), mr.Path), nil
}
