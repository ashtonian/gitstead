package cmd

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/ashtonian/git-mgr/internal/discovery"
	"github.com/ashtonian/git-mgr/internal/gitops"
	"github.com/ashtonian/git-mgr/internal/pathtemplate"
	"github.com/ashtonian/git-mgr/internal/state"
)

func (c *DiscoverCmd) Run(ctx *Context) error {
	cfg, err := ctx.LoadConfig()
	if err != nil {
		return err
	}

	// --apply mutates and saves state, so hold the lock (and re-read under it)
	// for that path; a dry-run preview stays lock-free.
	var st *state.State
	if c.Apply {
		var unlock func()
		st, unlock, err = lockedState(ctx)
		if err == nil {
			defer unlock()
		}
	} else {
		st, err = ctx.LoadState()
	}
	if err != nil {
		return err
	}

	results, discovered, err := discovery.RunDiscovery(ctx.GoCtx(), cfg, c.Rule, c.Tag)
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
		if r.Err != nil {
			fmt.Printf("  [%s] %s: ERROR: %v\n", r.Provider, r.Rule, r.Err)
			continue
		}
		fmt.Printf("  [%s] %s: found %d repos\n", r.Provider, r.Rule, r.Total)
		for _, w := range r.Warnings {
			fmt.Printf("      warning: %s\n", w)
		}
	}
	fmt.Printf("\n%d new, %d existing\n", len(newRepos), existing)

	if len(newRepos) > 0 {
		fmt.Println("\nNew repos:")
		for _, r := range newRepos {
			fmt.Printf("  + %s\n", r.Key())
		}
	}

	// Detect stale — but only on a fully successful run. If any rule failed,
	// the discovered set is incomplete and marking the missing repos stale
	// (or deleting them under remove_and_delete) would be destructive.
	if discovery.AnyFailed(results) {
		fmt.Println("\nSkipping stale detection: one or more discovery rules failed.")
	} else {
		discoveredKeys := make(map[string]bool)
		for k := range discovered {
			discoveredKeys[k] = true
		}
		outcome := discovery.DetectStale(st, discoveredKeys, discovery.StaleScopeOf(results, c.Rule, c.Tag), cfg.Cleanup, c.Apply)
		if outcome.NewlyStale > 0 {
			fmt.Printf("\n%d newly stale repos\n", outcome.NewlyStale)
		}
		if !c.Apply && outcome.Reapable > 0 {
			fmt.Printf("%d stale repos are past the grace period; run with --apply to apply cleanup policy %q\n",
				outcome.Reapable, cfg.Cleanup.Policy)
		}
		if c.Apply && outcome.Reaped > 0 {
			fmt.Printf("cleaned up %d stale repos (policy %q)\n", outcome.Reaped, cfg.Cleanup.Policy)
		}
		if c.Apply && outcome.Failed > 0 {
			fmt.Printf("warning: %d stale repos could not be deleted (left tracked)\n", outcome.Failed)
		}
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

			destPath := filepath.Join(repoCfg.BaseDir, relPath)
			sshKey := cfg.ResolveSSHKey(repo.Key(), "")
			cloneURL, cloneKey := cloneTarget(repo, cfg.CloneProtocolFor(repo.Key(), repo.DiscoveredBy), sshKey)

			fmt.Printf("  [%d/%d] %s ...", i+1, len(newRepos), repo.Key())
			start := time.Now()

			if err := gitops.Clone(ctx.GoCtx(), cloneURL, destPath, cloneKey); err != nil {
				fmt.Printf(" error: %v\n", err)
				continue
			}
			fmt.Printf(" done (%.1fs)\n", time.Since(start).Seconds())

			addRepoToState(st, repo, destPath, cloneURL)
			logEvent(ctx, "clone", repo.Key(), cloneURL)
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
			rs.Languages = repo.Languages
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
