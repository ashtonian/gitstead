package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ashtonian/git-mgr/internal/config"
	"github.com/ashtonian/git-mgr/internal/gitops"
	"github.com/ashtonian/git-mgr/internal/pathtemplate"
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

	cloned, skipped := 0, 0
	for _, mr := range m.Repos {
		id, perr := gitops.ParseURL(mr.URL)
		if perr != nil {
			fmt.Fprintf(os.Stderr, "skip %s: %v\n", mr.URL, perr)
			continue
		}
		key := id.Host + "/" + id.FullName
		if _, exists := st.Repos[key]; exists {
			skipped++
			continue
		}
		dest := manifestDest(cfg.ResolveForRepo(key), mr, id)

		if c.DryRun {
			fmt.Printf("  would clone %s -> %s\n", key, dest)
			continue
		}

		cloneURL, cloneKey := mr.URL, ""
		if !isHTTPURL(mr.URL) {
			cloneKey = cfg.ResolveSSHKey(key, "")
		}
		if err := gitops.Clone(ctx.GoCtx(), cloneURL, dest, cloneKey); err != nil {
			fmt.Fprintf(os.Stderr, "clone %s failed: %v\n", key, err)
			continue
		}
		now := time.Now()
		st.Repos[key] = &state.RepoState{
			CloneURL: mr.URL, Host: id.Host, Owner: id.Owner, Name: id.Name, FullName: id.FullName,
			Provider: id.Host, LocalPath: dest, Cloned: true, Tags: mr.Tags, ManuallyAdded: true,
			Status: "clean", FirstSeen: now, LastDiscovered: now,
		}
		logEvent(ctx, "clone", key, mr.URL)
		cloned++
		if !ctx.Quiet {
			fmt.Printf("  cloned %s\n", key)
		}
	}

	if !c.DryRun {
		if err := st.Save(); err != nil {
			return fmt.Errorf("saving state: %w", err)
		}
	}
	fmt.Printf("Imported %d repos (%d already tracked)\n", cloned, skipped)
	return nil
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
// path (relative to base_dir, or absolute), otherwise the configured template.
func manifestDest(cfg *config.Config, mr manifestRepo, id *gitops.RepoIdentity) string {
	base := config.ExpandHome(cfg.BaseDir)
	if mr.Path != "" {
		if filepath.IsAbs(mr.Path) {
			return mr.Path
		}
		return filepath.Join(base, mr.Path)
	}
	rel, err := pathtemplate.Resolve(cfg.PathTemplate, pathtemplate.RepoData{
		Host: id.Host, Owner: id.Owner, Name: id.Name, FullName: id.FullName,
	})
	if err != nil {
		rel = filepath.Join(id.Host, id.FullName)
	}
	return filepath.Join(base, rel)
}
