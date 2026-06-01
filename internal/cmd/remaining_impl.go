package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/ashtonian/git-mgr/internal/config"
	"github.com/ashtonian/git-mgr/internal/gitops"
	"github.com/ashtonian/git-mgr/internal/pathutil"
	"gopkg.in/yaml.v3"
)

func (c *RemoteSyncCmd) Run(ctx *Context) error {
	cfg, err := ctx.LoadConfig()
	if err != nil {
		return err
	}
	st, err := ctx.LoadState()
	if err != nil {
		return err
	}

	pass, err := selection{c.Repos, c.Tag, c.Set}.predicate(cfg)
	if err != nil {
		return err
	}

	pushed := 0
	for key, repo := range st.Repos {
		if !pass(key, repo) || !repo.Cloned {
			continue
		}

		repoCfg := cfg.ResolveForRepo(key)
		if !repoCfg.RemoteSync.Enabled {
			continue
		}
		// Honor the configured repos_glob (intersected with the CLI filters).
		if g := repoCfg.RemoteSync.ReposGlob; g != "" {
			if matched, _ := filepath.Match(g, key); !matched {
				continue
			}
		}

		remoteName := repoCfg.RemoteSync.RemoteName
		sshKey := cfg.ResolveSSHKey(key, repo.DiscoveredBy)

		if c.DryRun {
			fmt.Printf("  Would push refs for %s to %s\n", key, remoteName)
			continue
		}

		refspec := "refs/git-mgr/*:refs/git-mgr/*"
		_, err := gitops.GitWithSSHKey(ctx.GoCtx(), repo.LocalPath, sshKey, "push", remoteName, refspec)
		if err != nil {
			fmt.Printf("  %s: push failed: %v\n", key, err)
			continue
		}
		pushed++
		if !ctx.Quiet {
			fmt.Printf("  %s: pushed refs\n", key)
		}
	}

	if !ctx.Quiet {
		fmt.Printf("Pushed backup refs for %d repos\n", pushed)
	}
	return nil
}

func (c *RemoveCmd) Run(ctx *Context) error {
	cfg, err := ctx.LoadConfig()
	if err != nil {
		return err
	}
	sel := selection{repos: c.Pattern, tag: c.Tag, set: c.Set}
	if sel.empty() {
		return fmt.Errorf("specify a repo pattern, --tag, or --set")
	}

	st, unlock, err := lockedState(ctx)
	if err != nil {
		return err
	}
	defer unlock()

	toRemove, err := selectRepos(cfg, st, sel)
	if err != nil {
		return err
	}
	if len(toRemove) == 0 {
		return fmt.Errorf("no repos matched")
	}

	if !c.Yes {
		fmt.Printf("Will remove %d repos from tracking:\n", len(toRemove))
		for _, k := range toRemove {
			fmt.Printf("  %s\n", k)
		}
		if c.Delete {
			fmt.Println("  (will also delete from disk)")
		}
		fmt.Print("Continue? (y/n): ")
		var answer string
		fmt.Scanln(&answer)
		if answer != "y" && answer != "Y" {
			fmt.Println("Cancelled.")
			return nil
		}
	}

	for _, key := range toRemove {
		repo := st.Repos[key]
		if c.Delete && repo.LocalPath != "" {
			if err := pathutil.SafeRemoveAll(repo.LocalPath); err != nil {
				fmt.Printf("  Failed to delete %s: %v\n", repo.LocalPath, err)
			} else {
				fmt.Printf("  Deleted %s\n", repo.LocalPath)
			}
		}
		delete(st.Repos, key)
		logEvent(ctx, "remove", key, "")
		fmt.Printf("  Removed %s\n", key)
	}

	return st.Save()
}

func (c *ConfigCmd) Run(ctx *Context) error {
	if c.ShowPath {
		fmt.Println(ctx.Config)
		return nil
	}
	if c.Validate {
		_, err := ctx.LoadConfig()
		if err != nil {
			return fmt.Errorf("config validation failed: %w", err)
		}
		fmt.Println("Config is valid.")
		return nil
	}
	if c.Edit {
		editor := os.Getenv("EDITOR")
		if editor == "" {
			editor = "vi"
		}
		cmd := fmt.Sprintf("%s %s", editor, ctx.Config)
		return runShell(cmd)
	}

	cfg, err := ctx.LoadConfig()
	if err != nil {
		return err
	}

	// Show resolved config for a specific repo
	if c.ForRepo != "" {
		resolved := cfg.ResolveForRepo(c.ForRepo)
		sshKey := cfg.ResolveSSHKey(c.ForRepo, "")
		fmt.Printf("Resolved config for %s:\n", c.ForRepo)
		fmt.Printf("  path_template: %s\n", resolved.PathTemplate)
		fmt.Printf("  ssh_key: %s\n", sshKey)
		fmt.Printf("  sync.timeout_per_repo: %s\n", resolved.Sync.TimeoutPerRepo)
		fmt.Printf("  sync.min_fetch_interval: %s\n", resolved.Sync.MinFetchInterval)
		fmt.Printf("  backup.enabled: %v\n", resolved.Backup.Enabled)
		fmt.Printf("  backup.strategy: %s\n", resolved.Backup.Strategy)
		fmt.Printf("  remote_sync.enabled: %v\n", resolved.RemoteSync.Enabled)
		return nil
	}

	// Print full resolved config
	out, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	fmt.Print(string(out))
	return nil
}

func (c *InitCmd) Run(ctx *Context) error {
	path := config.ExpandHome(ctx.Config)

	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("config file already exists: %s", path)
	}

	cfg := config.Defaults()
	out, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	// Append commented stubs so a first-run user has something to uncomment
	// instead of an empty providers/discovery list. They stay commented, so the
	// written config still validates as-is.
	out = append(out, []byte(initExamples)...)

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("creating config dir: %w", err)
	}

	if err := os.WriteFile(path, out, 0600); err != nil {
		return err
	}

	fmt.Printf("Config written to %s\n", path)
	fmt.Println("Uncomment and edit the provider + discovery stubs, then run 'git-mgr discover'.")
	return nil
}

// initExamples are commented-out config stubs appended by `init` so a fresh
// config is usable by uncommenting rather than authored from scratch. Field
// names mirror config.example.yaml.
const initExamples = `
# --- Examples: uncomment and edit. Full reference: config.example.yaml ---
#
# providers:
#   - name: "github.com"
#     type: "github"
#     token_command: "gh auth token"   # or token_env: GITHUB_TOKEN
#
# discovery:
#   - name: "my-repos"
#     provider: "github.com"
#     user: "your-username"            # or: org: "your-org"
#     tags: ["personal"]
#
# workgroups:                          # clone is the default kind
#   - name: "review"
#     kind: "clone"
#     from_tag: "backend"
`

func runShell(cmd string) error {
	c := commandContext(cmd)
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return c.Run()
}

func commandContext(cmd string) *exec.Cmd {
	return exec.Command("sh", "-c", cmd)
}
