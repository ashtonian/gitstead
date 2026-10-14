package cmd

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/ashtonian/gitten/internal/state"
)

// DiscoverCmd discovers repos from configured providers.
// Run method is in discover_impl.go
type DiscoverCmd struct {
	Apply bool   `help:"Make the changes that move and delete nothing: clone new repos, update URLs, config and tags (default: preview only)." default:"false"`
	Rule  string `help:"Run only a specific discovery rule." default:""`
	Tag   string `help:"Run only rules with this tag." default:""`
}

// SyncCmd fetches all tracked repos and backs up uncommitted changes.
// Run method is in sync_impl.go
type SyncCmd struct {
	Repos      string `help:"Filter to repos matching glob." default:"" predictor:"repo"`
	Tag        string `help:"Filter to repos with this tag." default:"" predictor:"tag"`
	Set        string `help:"Filter by named set (from config 'sets:')." default:"" predictor:"set"`
	NoBackup   bool   `help:"Skip the backup step." default:"false" name:"no-backup"`
	NoFetch    bool   `help:"Skip fetching and mirrors, only back up." default:"false" name:"no-fetch"`
	NoDiscover bool   `help:"Skip auto-discovery." default:"false" name:"no-discover"`
	Force      bool   `help:"Ignore min_fetch_interval and mirror.interval." default:"false"`
}

// BackupCmd backs up uncommitted changes.
// Run method is in backup_impl.go
type BackupCmd struct {
	Repos   string `help:"Filter to specific repos." default:"" predictor:"repo"`
	Tag     string `help:"Filter by tag." default:"" predictor:"tag"`
	Set     string `help:"Filter by named set (from config 'sets:')." default:"" predictor:"set"`
	List    bool   `help:"List existing backups." default:"false"`
	Restore string `help:"Show restore instructions for a backup." default:""`
}

// RestoreCmd recovers a backup for a repo.
// Run method is in restore_impl.go
type RestoreCmd struct {
	Repo  string `arg:"" help:"Repo key (or glob matching exactly one repo)." predictor:"repo"`
	ID    string `arg:"" optional:"" default:"latest" help:"Backup id (timestamp) or 'latest'."`
	Into  string `help:"Clone + apply the backup into this scratch dir (safe inspection)." default:""`
	Apply bool   `help:"Apply the backup onto the repo's working tree (refuses a dirty tree without --force)."`
	Force bool   `help:"With --apply, apply even onto a dirty working tree."`
}

// StatusCmd shows state of all managed repos.
type StatusCmd struct {
	Live   bool   `help:"Inspect working trees live (dirty + ahead/behind), not the cached state." default:"false"`
	Repos  string `help:"Filter by glob." default:"" predictor:"repo"`
	Tag    string `help:"Filter by tag." default:"" predictor:"tag"`
	Set    string `help:"Filter by named set (from config 'sets:')." default:"" predictor:"set"`
	Dirty  bool   `help:"Only show dirty repos." default:"false"`
	Stale  bool   `help:"Only show stale repos." default:"false"`
	Errors bool   `help:"Only show repos in error state." default:"false"`
}

func (c *StatusCmd) Run(ctx *Context) error {
	st, err := ctx.LoadState()
	if err != nil {
		return err
	}

	if len(st.Repos) == 0 && !ctx.JSON {
		fmt.Println("No repos tracked. Adopt clones you already have ('gitten adopt <path>'),")
		fmt.Println("clone one ('gitten clone <url>'), or discover from a provider ('gitten discover --apply').")
		return nil
	}

	cfg, err := ctx.LoadConfig()
	if err != nil {
		return err
	}
	pass, err := selection{c.Repos, c.Tag, c.Set}.predicate(cfg)
	if err != nil {
		return err
	}

	if c.Live {
		return c.runLive(ctx, st, pass)
	}
	if ctx.JSON {
		return c.runJSON(ctx, st, pass)
	}
	return c.runTable(st, pass)
}

// statusFilter applies the cached-status filters (--dirty/--stale/--errors);
// the glob/tag/set selection is handled separately by a selection predicate.
func (c *StatusCmd) statusFilter(repo *state.RepoState) bool {
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

func (c *StatusCmd) runJSON(ctx *Context, st *state.State, pass func(string, *state.RepoState) bool) error {
	filtered := make(map[string]*state.RepoState)
	for key, repo := range st.Repos {
		if pass(key, repo) && c.statusFilter(repo) {
			filtered[key] = repo
		}
	}
	return ctx.Emit(filtered)
}

func (c *StatusCmd) runTable(st *state.State, pass func(string, *state.RepoState) bool) error {
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "REPO\tSTATUS\tBRANCH\tLAST FETCH\tBACKUP\tTAGS")

	var counts struct{ dirty, errors, stale, clean, total, mirrorFailing int }
	// Collect and sort keys
	keys := make([]string, 0, len(st.Repos))
	for k := range st.Repos {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, key := range keys {
		repo := st.Repos[key]
		if !pass(key, repo) || !c.statusFilter(repo) {
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
		if repo.LastBackupError != "" {
			backupAge = "failed"
		}

		tags := strings.Join(repo.Tags, ",")
		if repo.LastMirrorError != "" {
			counts.mirrorFailing++
		}

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
	if counts.mirrorFailing > 0 {
		fmt.Printf("%d mirrors failing to update; `gitten mirror` retries them and says why\n", counts.mirrorFailing)
	}
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

// BootstrapCmd sets up a fresh machine in one command.
// Run method is in bootstrap_impl.go
type BootstrapCmd struct {
	Profile  string        `help:"Scope discovery rules + sync to this tag." default:""`
	Interval time.Duration `help:"Sync interval for the installed agent." default:"15m"`
	NoAgent  bool          `help:"Skip installing the periodic sync agent." default:"false" name:"no-agent"`
	DryRun   bool          `help:"Print the steps without running them." default:"false" name:"dry-run"`
}

// ManifestCmd groups portable export/import of the tracked repo set.
type ManifestCmd struct {
	Export ManifestExportCmd `cmd:"" help:"Export tracked repos to a portable manifest (stdout or --out)."`
	Import ManifestImportCmd `cmd:"" help:"Clone + track repos listed in a manifest (idempotent)."`
	Save   ManifestSaveCmd   `cmd:"" help:"Save repos tracked by hand on this machine in the shared manifest (config 'manifest:'), so every machine has them."`
}

// ManifestSaveCmd saves this machine's hand-added repos in the shared
// manifest. Run method is in manifest_impl.go
type ManifestSaveCmd struct {
	DryRun bool `help:"List what would be saved." name:"dry-run"`
}

// ManifestExportCmd writes a portable manifest of tracked repos.
// Run method is in manifest_impl.go
type ManifestExportCmd struct {
	Out   string `help:"Write to this file instead of stdout." default:""`
	Repos string `help:"Filter by glob." default:"" predictor:"repo"`
	Tag   string `help:"Filter by tag." default:"" predictor:"tag"`
	Set   string `help:"Filter by named set." default:"" predictor:"set"`
}

// ManifestImportCmd clones + tracks repos from a manifest.
// Run method is in manifest_impl.go
type ManifestImportCmd struct {
	File   string `arg:"" help:"Manifest file to import."`
	DryRun bool   `help:"Show what would be cloned without doing it." default:"false" name:"dry-run"`
}

// ReportCmd summarizes recent activity from the operation log.
// Run method is in report_impl.go
type ReportCmd struct {
	Since time.Duration `help:"Window to summarize." default:"24h"`
}

// LogCmd prints recent raw operation-log events.
// Run method is in report_impl.go
type LogCmd struct {
	Lines int `help:"Show the last N events (0 = all)." default:"50" short:"n"`
}

// RunCmd runs a command in each matching tracked repo.
// Run method is in run_impl.go
type RunCmd struct {
	Repos    string   `help:"Filter by glob." default:"" predictor:"repo"`
	Tag      string   `help:"Filter by tag." default:"" predictor:"tag"`
	Set      string   `help:"Filter by named set (from config 'sets:')." default:"" predictor:"set"`
	FailFast bool     `help:"Stop at the first repo whose command fails." default:"false" name:"fail-fast"`
	Command  []string `arg:"" help:"Command to run in each repo directory (after --)."`
}

// CloneCmd clones and tracks repos by hand: one by its URL, or every repo a
// selection finds -- --org, --user, --group, --mine or --query, narrowed by
// the filters. Run method is in clone_impl.go.
type CloneCmd struct {
	URL  string `arg:"" optional:"" help:"URL of one repo to clone. Or select many with --org, --user, --group, --mine or --query."`
	Tag  string `help:"Assign tags (comma-separated)." default:""`
	Path string `help:"Clone the one repo here instead of where config puts it." default:""`

	Org         string   `help:"Select the repos of this organization (GitHub, Gitea/Forgejo)." group:"Selecting many"`
	User        string   `help:"Select this user's public repos." group:"Selecting many"`
	Group       string   `help:"Select the projects of this GitLab group." group:"Selecting many"`
	Mine        bool     `help:"Select the repos your token's account owns, private ones included." group:"Selecting many"`
	Query       string   `help:"Select what this provider search finds, e.g. \"org:acme language:go stars:>50\"; no other filter applies." group:"Selecting many"`
	Language    []string `help:"Only repos in one of these languages." group:"Selecting many"`
	Topic       []string `help:"Only repos with all of these topics." group:"Selecting many"`
	Name        []string `help:"Only repos whose name matches one of these globs, ignoring case: 'billing-*', '*payments*'. Repeat or comma-separate." group:"Selecting many"`
	NamePattern string   `help:"Only repos whose name matches this regular expression." name:"name-pattern" group:"Selecting many"`
	Visibility  string   `help:"Only public, private or internal repos." enum:",all,public,private,internal" default:"" group:"Selecting many"`
	Forks       bool     `help:"Include forks." group:"Selecting many"`
	Archived    bool     `help:"Include archived repos." group:"Selecting many"`
	Provider    string   `help:"The configured provider to search; the only one by default, github.com when none is configured." group:"Selecting many"`
	Save        string   `help:"Save the selection as a discovery rule with this name, so every sync clones what newly matches." group:"Selecting many"`
	NoSave      bool     `help:"Don't offer to save the selection as a discovery rule." name:"no-save" group:"Selecting many"`
	DryRun      bool     `help:"List what the selection finds; clone nothing." name:"dry-run" group:"Selecting many"`
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
	Pattern string `arg:"" optional:"" help:"Repo key glob to remove (or use --tag/--set)." predictor:"repo"`
	Tag     string `help:"Remove repos with this tag." default:"" predictor:"tag"`
	Set     string `help:"Remove repos in this named set (from config 'sets:')." default:"" predictor:"set"`
	Delete  bool   `help:"Also delete the clone; unique work is archived first." xor:"disk"`
	Archive bool   `help:"Also move the clone, whole, into backup.archive_dir; gitten archives restore puts it back." xor:"disk"`
	Yes     bool   `help:"Skip confirmation." default:"false"`
}

// RemoteSyncCmd takes working copies offsite now, under remote_sync's policy.
// Run method is in remaining_impl.go
type RemoteSyncCmd struct {
	Repos  string `help:"Filter by glob." default:"" predictor:"repo"`
	Tag    string `help:"Filter by tag." default:"" predictor:"tag"`
	Set    string `help:"Filter by named set (from config 'sets:')." default:"" predictor:"set"`
	DryRun bool   `help:"Show what would be pushed, from the latest backups; change nothing." default:"false" name:"dry-run"`
	List   bool   `help:"List what every machine and copy left offsite for the selected repos, and how to restore it."`
}

// ConfigCmd prints resolved configuration.
// Run method is in remaining_impl.go
type ConfigCmd struct {
	Edit     bool   `help:"Open config in editor." default:"false"`
	Validate bool   `help:"Validate config and exit." default:"false"`
	ShowPath bool   `help:"Print config file path." default:"false" name:"path"`
	ForRepo  string `help:"Show resolved config for a specific repo key." default:"" name:"for-repo"`
	Redact   bool   `help:"When printing the full config, blank inline provider tokens." default:"false"`
}
