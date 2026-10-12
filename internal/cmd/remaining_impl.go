package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ashtonian/git-mgr/internal/config"
	"github.com/ashtonian/git-mgr/internal/gitops"
	"github.com/ashtonian/git-mgr/internal/shellutil"
	"github.com/ashtonian/git-mgr/internal/state"
	"gopkg.in/yaml.v3"
)

func (c *RemoveCmd) Run(ctx *Context) error {
	cfg, err := ctx.LoadConfig()
	if err != nil {
		return err
	}
	sel := selection{repos: c.Pattern, tag: c.Tag, set: c.Set}
	if sel.empty() {
		return fmt.Errorf("specify a repo pattern, --tag, or --set")
	}

	// Unlocked through the prompt and the archiving; the untracking is one
	// locked write at the end.
	st, err := ctx.LoadState()
	if err != nil {
		return err
	}

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
			fmt.Printf("  (will also delete from disk; unique work is archived to %s first)\n", cfg.Backup.ArchiveDir)
		}
		fmt.Print("Continue? (y/n): ")
		var answer string
		fmt.Scanln(&answer)
		if answer != "y" && answer != "Y" {
			fmt.Println("Cancelled.")
			return nil
		}
	}

	var kept []string
	untrack := map[string]string{} // key -> the path it was removed at
	for _, key := range toRemove {
		repo := st.Repos[key]
		if c.Delete && repo.LocalPath != "" {
			archived, err := gitops.RemoveClone(ctx.GoCtx(), repo.LocalPath, cfg.Backup.ArchiveDir, key, false)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  %s: kept and still tracked: %v\n", key, err)
				kept = append(kept, key)
				continue
			}
			if archived != "" {
				fmt.Printf("  Archived unique work of %s to %s\n", key, archived)
			}
			fmt.Printf("  Deleted %s\n", repo.LocalPath)
		}
		untrack[key] = repo.LocalPath
	}

	err = st.Update(cfg.LockTimeout, func(st *state.State) error {
		for key, path := range untrack {
			// A repo moved meanwhile wasn't the one deleted; keep tracking it.
			if repo := st.Repos[key]; repo != nil && (!c.Delete || repo.LocalPath == path) {
				delete(st.Repos, key)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, key := range toRemove {
		if _, ok := untrack[key]; ok {
			logEvent(ctx, "remove", key, "")
			fmt.Printf("  Removed %s\n", key)
		}
	}
	return outcome(len(kept), len(toRemove), "repos could not be deleted and are still tracked: %s", strings.Join(kept, ", "))
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
		rule := ""
		if st, serr := ctx.LoadState(); serr == nil {
			if r, ok := st.Repos[c.ForRepo]; ok {
				rule = r.DiscoveredBy
			}
		}
		resolved := cfg.ResolveForRepo(c.ForRepo)
		fmt.Printf("Resolved config for %s:\n", c.ForRepo)
		if rule != "" {
			fmt.Printf("  discovered_by: %s\n", rule)
		}
		fmt.Printf("  path_template: %s\n", resolved.PathTemplate)
		fmt.Printf("  ssh_key: %s\n", cfg.ResolveSSHKey(c.ForRepo, rule))
		if a := cfg.AccountFor(c.ForRepo, rule); a != nil {
			fmt.Printf("  account: %s\n", a.Name)
			fmt.Printf("  user.name: %s\n", orInherited(a.Git.Name))
			fmt.Printf("  user.email: %s\n", orInherited(a.Git.Email))
		} else {
			fmt.Println("  account: (none; your global git identity applies)")
		}
		fmt.Printf("  sync.timeout_per_repo: %s\n", resolved.Sync.TimeoutPerRepo)
		fmt.Printf("  sync.min_fetch_interval: %s\n", resolved.Sync.MinFetchInterval)
		fmt.Printf("  backup.enabled: %v\n", resolved.Backup.Enabled)
		fmt.Printf("  backup.strategy: %s\n", resolved.Backup.Strategy)
		fmt.Printf("  remote_sync.enabled: %v\n", resolved.RemoteSync.Enabled)
		return nil
	}

	// Print full resolved config (--redact blanks inline provider tokens so the
	// output is safe to share / carry to another machine; token_env and
	// token_command are references, not secrets, so they're kept).
	if c.Redact {
		for i := range cfg.Providers {
			if cfg.Providers[i].Token != "" {
				cfg.Providers[i].Token = "REDACTED"
			}
		}
	}
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

	// If the gh CLI is logged in, prefill a ready GitHub provider + discovery
	// rule so `git-mgr discover --apply` works with zero editing -- the
	// zero-setup path for the GitHub + gh majority.
	detected := ""
	if !c.NoDetect {
		if user, ok := detectGitHubCLI(ctx); ok {
			// The token's own repos, private ones included, with the token
			// pinned to this account so a later `gh auth switch` can't change it.
			cfg.Providers = []config.Provider{{Name: "github.com", Type: "github",
				TokenCommand: "gh auth token --hostname github.com --user " + shellutil.Quote(user)}}
			cfg.Discovery = []config.DiscoveryRule{{Name: user, Provider: "github.com",
				Affiliation: []string{"owner"}, Tags: []string{"personal"}}}
			detected = user
		}
	}

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
	if detected != "" {
		fmt.Printf("Detected GitHub login %q via gh -- added a ready provider + discovery rule.\n", detected)
		fmt.Println("Run 'git-mgr discover --apply' to clone your repos (or 'git-mgr bootstrap').")
	} else {
		fmt.Println("Uncomment and edit the provider + discovery stubs, then run 'git-mgr discover'.")
	}
	return nil
}

// detectGitHubCLI returns the logged-in GitHub user if the gh CLI is installed
// and authenticated (so `gh auth token` works), else ("", false). Used by init
// to prefill a ready GitHub provider.
func detectGitHubCLI(ctx *Context) (string, bool) {
	if _, err := exec.LookPath("gh"); err != nil {
		return "", false
	}
	// Pin to github.com: a user whose gh default host is GitHub Enterprise would
	// otherwise get a github.com provider pointed at the wrong account/host.
	if exec.CommandContext(ctx.GoCtx(), "gh", "auth", "token", "--hostname", "github.com").Run() != nil {
		return "", false
	}
	out, err := exec.CommandContext(ctx.GoCtx(), "gh", "api", "user", "--hostname", "github.com", "--jq", ".login").Output()
	if err != nil {
		return "", false
	}
	login := strings.TrimSpace(string(out))
	if login == "" {
		return "", false
	}
	return login, true
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
