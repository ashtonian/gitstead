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

	pushed := 0
	for key, repo := range st.Repos {
		if c.Repos != "" {
			matched, _ := filepath.Match(c.Repos, key)
			if !matched {
				continue
			}
		}
		if c.Tag != "" && !repo.HasTag(c.Tag) {
			continue
		}
		if !repo.Cloned {
			continue
		}

		repoCfg := cfg.ResolveForRepo(key)
		if !repoCfg.RemoteSync.Enabled {
			continue
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
	st, err := ctx.LoadState()
	if err != nil {
		return err
	}

	var toRemove []string
	for key := range st.Repos {
		matched, _ := filepath.Match(c.Pattern, key)
		if matched || key == c.Pattern {
			toRemove = append(toRemove, key)
		}
	}

	if len(toRemove) == 0 {
		return fmt.Errorf("no repos matching %q", c.Pattern)
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

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("creating config dir: %w", err)
	}

	if err := os.WriteFile(path, out, 0600); err != nil {
		return err
	}

	fmt.Printf("Config written to %s\n", path)
	fmt.Println("Edit the file to add providers and discovery rules.")
	return nil
}

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
