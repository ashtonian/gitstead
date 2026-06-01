package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/ashtonian/git-mgr/internal/state"
)

// InitCmd creates a config file with sensible defaults.
// Run method is in remaining_impl.go
type InitCmd struct{}

// DiscoverCmd discovers repos from configured providers.
// Run method is in discover_impl.go
type DiscoverCmd struct {
	Apply  bool   `help:"Clone new repos and update state." default:"false"`
	Rule   string `help:"Run only a specific discovery rule." default:""`
	Tag    string `help:"Run only rules with this tag." default:""`
	DryRun bool   `help:"Show what would happen without changing state (default)." default:"true" name:"dry-run"`
}

// SyncCmd fetches all tracked repos and backs up uncommitted changes.
// Run method is in sync_impl.go
type SyncCmd struct {
	Repos      string `help:"Filter to repos matching glob." default:""`
	Tag        string `help:"Filter to repos with this tag." default:""`
	NoBackup   bool   `help:"Skip the backup step." default:"false" name:"no-backup"`
	NoFetch    bool   `help:"Skip fetching, only backup." default:"false" name:"no-fetch"`
	NoDiscover bool   `help:"Skip auto-discovery." default:"false" name:"no-discover"`
	Force      bool   `help:"Ignore min_fetch_interval." default:"false"`
	PushBranch bool   `help:"On backup, also push WIP to a recoverable remote branch (git-mgr/wip/<branch>)." default:"false" name:"push-branch"`
}

// BackupCmd backs up uncommitted changes.
// Run method is in backup_impl.go
type BackupCmd struct {
	Repos      string `help:"Filter to specific repos." default:""`
	Tag        string `help:"Filter by tag." default:""`
	List       bool   `help:"List existing backups." default:"false"`
	Restore    string `help:"Show restore instructions for a backup." default:""`
	PushBranch bool   `help:"Also push WIP to a recoverable remote branch (git-mgr/wip/<branch>)." default:"false" name:"push-branch"`
	PushRemote string `help:"Remote for --push-branch (default: config/origin)." default:"" name:"push-remote"`
}

// StatusCmd shows state of all managed repos.
type StatusCmd struct {
	JSON   bool   `help:"JSON output." default:"false"`
	Repos  string `help:"Filter by glob." default:""`
	Tag    string `help:"Filter by tag." default:""`
	Dirty  bool   `help:"Only show dirty repos." default:"false"`
	Stale  bool   `help:"Only show stale repos." default:"false"`
	Errors bool   `help:"Only show repos in error state." default:"false"`
}

func (c *StatusCmd) Run(ctx *Context) error {
	st, err := ctx.LoadState()
	if err != nil {
		return err
	}

	if len(st.Repos) == 0 {
		fmt.Println("No repos tracked. Run 'git-mgr discover --apply' to get started.")
		return nil
	}

	if c.JSON {
		return c.runJSON(st)
	}
	return c.runTable(st)
}

func (c *StatusCmd) matches(key string, repo *state.RepoState) bool {
	if c.Repos != "" {
		matched, _ := filepath.Match(c.Repos, key)
		if !matched {
			return false
		}
	}
	if c.Tag != "" && !repo.HasTag(c.Tag) {
		return false
	}
	if c.Dirty && repo.Status != "dirty" {
		return false
	}
	if c.Stale && repo.Status != "stale" {
		return false
	}
	if c.Errors && repo.Status != "error" {
		return false
	}
	return true
}

func (c *StatusCmd) runJSON(st *state.State) error {
	filtered := make(map[string]*state.RepoState)
	for key, repo := range st.Repos {
		if c.matches(key, repo) {
			filtered[key] = repo
		}
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(filtered)
}

func (c *StatusCmd) runTable(st *state.State) error {
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "REPO\tSTATUS\tBRANCH\tLAST FETCH\tBACKUP\tTAGS")

	var counts struct{ dirty, errors, stale, clean, total int }
	// Collect and sort keys
	keys := make([]string, 0, len(st.Repos))
	for k := range st.Repos {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, key := range keys {
		repo := st.Repos[key]
		if !c.matches(key, repo) {
			continue
		}
		counts.total++
		switch repo.Status {
		case "dirty":
			counts.dirty++
		case "error":
			counts.errors++
		case "stale":
			counts.stale++
		default:
			counts.clean++
		}

		fetchAge := "-"
		if !repo.LastFetch.IsZero() {
			fetchAge = timeAgo(repo.LastFetch)
		}
		if repo.LastFetchError != "" {
			fetchAge = "failed"
		}

		backupAge := "-"
		if !repo.LastBackup.IsZero() {
			backupAge = timeAgo(repo.LastBackup)
		}

		tags := strings.Join(repo.Tags, ",")

		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			key, repo.Status, repo.CurrentBranch, fetchAge, backupAge, tags)
	}
	tw.Flush()

	fmt.Printf("\n%d repos", counts.total)
	parts := []string{}
	if counts.dirty > 0 {
		parts = append(parts, fmt.Sprintf("%d dirty", counts.dirty))
	}
	if counts.errors > 0 {
		parts = append(parts, fmt.Sprintf("%d error", counts.errors))
	}
	if counts.stale > 0 {
		parts = append(parts, fmt.Sprintf("%d stale", counts.stale))
	}
	if counts.clean > 0 {
		parts = append(parts, fmt.Sprintf("%d clean", counts.clean))
	}
	if len(parts) > 0 {
		fmt.Printf(" (%s)", strings.Join(parts, ", "))
	}
	fmt.Println()
	return nil
}

func timeAgo(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// CloneCmd manually clones and tracks a repo.
// Run method is in clone_impl.go
type CloneCmd struct {
	URL  string `arg:"" help:"SSH URL of the repo to clone."`
	Tag  string `help:"Assign tags (comma-separated)." default:""`
	Path string `help:"Override the templated clone path." default:""`
}

// AdoptCmd imports existing local repos into management.
// Run method is in adopt_impl.go
type AdoptCmd struct {
	Path   string `arg:"" help:"Path to an existing git repo, or directory to scan."`
	Tag    string `help:"Assign tags (comma-separated)." default:""`
	Scan   bool   `help:"Recursively scan directory for git repos." default:"false"`
	DryRun bool   `help:"Show what would be adopted." default:"false" name:"dry-run"`
}

// RemoveCmd stops tracking repos.
// Run method is in remaining_impl.go
type RemoveCmd struct {
	Pattern string `arg:"" help:"Repo name pattern to remove."`
	Delete  bool   `help:"Also delete from disk." default:"false"`
	Yes     bool   `help:"Skip confirmation." default:"false"`
}

// RemoteSyncCmd pushes backup refs to remotes.
// Run method is in remaining_impl.go
type RemoteSyncCmd struct {
	Repos  string `help:"Filter by glob." default:""`
	Tag    string `help:"Filter by tag." default:""`
	DryRun bool   `help:"Show what would be pushed." default:"false" name:"dry-run"`
}

// ConfigCmd prints resolved configuration.
// Run method is in remaining_impl.go
type ConfigCmd struct {
	Edit     bool   `help:"Open config in editor." default:"false"`
	Validate bool   `help:"Validate config and exit." default:"false"`
	ShowPath bool   `help:"Print config file path." default:"false" name:"path"`
	ForRepo  string `help:"Show resolved config for a specific repo key." default:"" name:"for-repo"`
}
