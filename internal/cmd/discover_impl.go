package cmd

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/ashtonian/git-mgr/internal/discovery"
	"github.com/ashtonian/git-mgr/internal/gitops"
	"github.com/ashtonian/git-mgr/internal/pathtemplate"
)

func (c *DiscoverCmd) Run(ctx *Context) error {
	cfg, err := ctx.LoadConfig()
	if err != nil {
		return err
	}
	st, err := ctx.LoadState()
	if err != nil {
		return err
	}

	results, discovered, err := discovery.RunDiscovery(context.Background(), cfg, c.Rule, c.Tag)
	if err != nil {
		return err
	}

	// Classify repos
	var newRepos []discovery.Repo
	existing := 0
	for key, repo := range discovered {
		if _, ok := st.Repos[key]; ok {
			existing++
		} else {
			newRepos = append(newRepos, repo)
		}
	}

	// Print summary
	for _, r := range results {
		fmt.Printf("  [%s] %s: found %d repos\n", r.Provider, r.Rule, r.Total)
	}
	fmt.Printf("\n%d new, %d existing\n", len(newRepos), existing)

	if len(newRepos) > 0 {
		fmt.Println("\nNew repos:")
		for _, r := range newRepos {
			fmt.Printf("  + %s\n", r.Key())
		}
	}

	// Detect stale
	discoveredKeys := make(map[string]bool)
	for k := range discovered {
		discoveredKeys[k] = true
	}
	staleCount := discovery.DetectStale(st, discoveredKeys, cfg.Cleanup)
	if staleCount > 0 {
		fmt.Printf("\n%d newly stale repos\n", staleCount)
	}

	if !c.Apply {
		if len(newRepos) > 0 {
			fmt.Println("\nUse --apply to clone new repos and update state.")
		}
		return nil
	}

	// Clone new repos
	if len(newRepos) > 0 {
		fmt.Printf("\nCloning %d new repos...\n", len(newRepos))
		for i, repo := range newRepos {
			repoCfg := cfg.ResolveForRepo(repo.Key())
			tmpl := repoCfg.PathTemplate

			relPath, err := pathtemplate.Resolve(tmpl, pathtemplate.RepoData{
				Host:     repo.Host,
				Owner:    repo.Owner,
				Name:     repo.Name,
				FullName: repo.FullName,
			})
			if err != nil {
				fmt.Printf("  [%d/%d] %s: path template error: %v\n", i+1, len(newRepos), repo.Key(), err)
				continue
			}

			destPath := filepath.Join(cfg.BaseDir, relPath)
			sshKey := cfg.ResolveSSHKey(repo.Key(), "")

			fmt.Printf("  [%d/%d] %s ...", i+1, len(newRepos), repo.Key())
			start := time.Now()

			if err := gitops.Clone(context.Background(), repo.SSHURL, destPath, sshKey); err != nil {
				fmt.Printf(" error: %v\n", err)
				continue
			}
			fmt.Printf(" done (%.1fs)\n", time.Since(start).Seconds())

			addRepoToState(st, repo, destPath)
		}
	}

	// Update existing repos' metadata
	for key, repo := range discovered {
		if rs, ok := st.Repos[key]; ok {
			rs.LastDiscovered = time.Now()
			rs.IsArchived = repo.IsArchived
			rs.IsFork = repo.IsFork
			rs.Description = repo.Description
			rs.Language = repo.Language
			rs.Topics = repo.Topics
			rs.DefaultBranch = repo.DefaultBranch
		}
	}

	if err := st.Save(); err != nil {
		return fmt.Errorf("saving state: %w", err)
	}

	fmt.Printf("State updated: %d repos tracked.\n", len(st.Repos))
	return nil
}
