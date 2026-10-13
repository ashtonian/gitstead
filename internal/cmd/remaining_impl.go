package cmd

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ashtonian/git-mgr/internal/config"
	"github.com/ashtonian/git-mgr/internal/ctxutil"
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

	if !c.Yes && ctx.JSON {
		return errNeedsYes("remove")
	}
	if !c.Yes {
		fmt.Printf("Will remove %d repos from tracking:\n", len(toRemove))
		for _, k := range toRemove {
			fmt.Printf("  %s\n", k)
		}
		switch {
		case c.Delete:
			fmt.Printf("  (will also delete from disk; unique work is archived to %s first)\n", cfg.Backup.ArchiveDir)
		case c.Archive:
			fmt.Printf("  (will also move each clone into %s)\n", cfg.Backup.ArchiveDir)
		}
		fmt.Print("Continue? (y/n): ")
		var answer string
		fmt.Scanln(&answer)
		if answer != "y" && answer != "Y" {
			fmt.Println("Cancelled.")
			return nil
		}
	}

	var kept []targetError
	keep := func(key string, err error) {
		fmt.Fprintf(os.Stderr, "  %s: kept and still tracked: %v\n", key, err)
		kept = append(kept, targetError{key, err.Error()})
	}
	var archives []state.ArchiveEntry
	untrack := map[string]string{} // key -> the path it was removed at
	take := repoTaker(st.Path(), cfg.LockTimeout)
	// Each clone is taken -- locked, and checked against state as it is
	// then -- for as long as deleting or archiving it takes, so no workspace
	// copy is made from it meanwhile.
	removeOne := func(key string, repo *state.RepoState) error {
		if err := st.CheckRemovable(key); err != nil {
			return err
		}
		if !c.Delete && !c.Archive || repo.LocalPath == "" {
			untrack[key] = repo.LocalPath
			return nil
		}
		taken, err := take(ctx.GoCtx(), key, repo.LocalPath)
		if err != nil {
			return err
		}
		defer taken.release()
		rctx, cancel := ctxutil.WithTimeout(ctx.GoCtx(), cfg.Sync.CloneTimeout)
		defer cancel()
		timedOut := func(err error) error {
			return ctxutil.TimedOut(rctx, err, "archiving "+repo.LocalPath, cfg.Sync.CloneTimeout, "sync.clone_timeout")
		}
		if c.Delete {
			archived, err := gitops.RemoveClone(rctx, repo.LocalPath, cfg.Backup.ArchiveDir, key, gitops.RemoveOptions{KeepHistory: taken.state.LeansOn(key)})
			if err != nil {
				return timedOut(err)
			}
			if archived != "" {
				fmt.Printf("  Archived unique work of %s to %s\n", key, archived)
				archives = append(archives, archiveEntry(archived, key, repo.LocalPath))
			}
			fmt.Printf("  Deleted %s\n", repo.LocalPath)
		} else {
			entry, err := archiveClone(rctx, ctx.Logger(), cfg.Backup.ArchiveDir, key, repo.LocalPath)
			if err != nil {
				return timedOut(err)
			}
			if entry != nil {
				fmt.Printf("  Moved %s to %s (git-mgr archives restore %s)\n", repo.LocalPath, entry.Path, entry.ID)
				archives = append(archives, *entry)
			}
		}
		untrack[key] = repo.LocalPath
		return nil
	}
	for _, key := range toRemove {
		if err := removeOne(key, st.Repos[key]); err != nil {
			keep(key, err)
		}
	}

	tookClones := c.Delete || c.Archive
	var stillTracked []string
	err = st.Update(cfg.LockTimeout, func(st *state.State) error {
		st.Archives = append(st.Archives, archives...)
		stillTracked = nil
		for key, path := range untrack {
			// A repo moved meanwhile wasn't the one deleted, and one a
			// workspace started using meanwhile is still needed.
			if repo := st.Repos[key]; repo != nil && (!tookClones || repo.LocalPath == path) && st.CheckRemovable(key) == nil {
				delete(st.Repos, key)
			} else if repo != nil {
				stillTracked = append(stillTracked, key)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, key := range stillTracked {
		keep(key, errors.New("its clone was taken, but it changed in state meanwhile -- moved, or a workspace copy made from it"))
	}
	removed := []string{}
	for _, key := range toRemove {
		if _, ok := untrack[key]; ok && st.Repos[key] == nil {
			removed = append(removed, key)
			logEvent(ctx, "remove", key, "")
			fmt.Printf("  Removed %s\n", key)
		}
	}
	keptKeys := make([]string, 0, len(kept))
	for _, k := range kept {
		keptKeys = append(keptKeys, k.Target)
	}
	// Other machines reading the manifest no longer clone it; the ones that
	// have it keep it, as with a repos: entry dropped.
	err = errors.Join(
		outcome(len(kept), len(toRemove), "repos could not be deleted or archived and are still tracked: %s", strings.Join(keptKeys, ", ")),
		dropFromManifest(cfg, removed...))
	return ctx.finish(struct {
		Removed  []string             `json:"removed"`
		Kept     []targetError        `json:"kept,omitempty"`
		Archives []state.ArchiveEntry `json:"archives,omitempty"`
	}{removed, kept, archives}, err)
}

func (c *ConfigCmd) Run(ctx *Context) error {
	if c.ShowPath {
		fmt.Println(ctx.ConfigPath())
		return ctx.finish(map[string]string{"path": ctx.ConfigPath()}, nil)
	}
	if c.Validate {
		_, err := ctx.LoadConfig()
		if err != nil {
			return fmt.Errorf("config validation failed: %w", err)
		}
		fmt.Println("Config is valid.")
		return ctx.finish(map[string]any{"path": ctx.ConfigPath(), "valid": true}, nil)
	}
	if c.Edit && ctx.JSON {
		return errNoJSON("config --edit")
	}
	if c.Edit {
		// A config that doesn't load is what you'd be editing to fix.
		cfg, _ := ctx.LoadConfig()
		editor := editorFor(cfg)
		if editor == "" {
			editor = "vi"
		}
		path := ctx.ConfigPath()
		return runEditor(editor, path, filepath.Dir(path))
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
		remoteSync := cfg.RemoteSyncFor(c.ForRepo, rule)
		if ctx.JSON {
			doc := map[string]any{"repo": c.ForRepo, "discovered_by": rule, "path_template": resolved.PathTemplate,
				"ssh_key": cfg.ResolveSSHKey(c.ForRepo, rule)}
			// Each section as the config file spells it: its keys, and
			// durations such as "5m0s".
			for key, section := range map[string]any{
				"sync": resolved.Sync, "backup": resolved.Backup, "remote_sync": remoteSync,
				"clone": cfg.CloneFor(c.ForRepo, rule), "hooks": cfg.HooksFor(c.ForRepo, rule), "mirror": cfg.MirrorFor(c.ForRepo, rule),
			} {
				v, err := asConfigValue(section)
				if err != nil {
					return err
				}
				doc[key] = v
			}
			if a := cfg.AccountFor(c.ForRepo, rule); a != nil {
				doc["account"] = a.Name
				doc["user.name"], doc["user.email"] = a.Git.Name, a.Git.Email
			}
			return ctx.Emit(doc)
		}
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
		fmt.Printf("  remote_sync.enabled: %v\n", remoteSync.Enabled)
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
	if ctx.JSON {
		// The config file's own keys, not Go's field names.
		var doc map[string]any
		if err := yaml.Unmarshal(out, &doc); err != nil {
			return err
		}
		return ctx.Emit(doc)
	}
	fmt.Print(string(out))
	return nil
}

func (c *InitCmd) Run(ctx *Context) error {
	path := ctx.ConfigPath()

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
	return ctx.finish(map[string]string{"path": path, "github_login": detected}, nil)
}

// detectGitHubCLI returns the logged-in GitHub user if the gh CLI is installed
// and authenticated (so `gh auth token` works), else ("", false). Used by init
// to prefill a ready GitHub provider.
func detectGitHubCLI(ctx *Context) (string, bool) {
	if _, err := exec.LookPath("gh"); err != nil {
		return "", false
	}
	// Each call is bounded as a provider's API requests are.
	timeout := config.Defaults().Sync.APITimeout
	if cfg, err := ctx.LoadConfig(); err == nil {
		timeout = cfg.Sync.APITimeout
	}
	gh := func(args ...string) ([]byte, error) {
		cctx, cancel := ctxutil.WithTimeout(ctx.GoCtx(), timeout)
		defer cancel()
		return exec.CommandContext(cctx, "gh", args...).Output()
	}
	// Pin to github.com: a user whose gh default host is GitHub Enterprise would
	// otherwise get a github.com provider pointed at the wrong account/host.
	if _, err := gh("auth", "token", "--hostname", "github.com"); err != nil {
		return "", false
	}
	out, err := gh("api", "user", "--hostname", "github.com", "--jq", ".login")
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
# workspaces:                          # agents' copies; see config.example.yaml
#   - name: "app"
#     repos: ["github.com/your-username/app"]
#     agents: 4
`

// asConfigValue is v as config spells it, for JSON: marshalled by its YAML
// keys, so a duration reads "5m0s" and a field its config key.
func asConfigValue(v any) (any, error) {
	data, err := yaml.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out any
	err = yaml.Unmarshal(data, &out)
	return out, err
}
