package config

import (
	"strings"
	"time"
)

// Config is the top-level configuration.
type Config struct {
	BaseDir      string `koanf:"base_dir"       yaml:"base_dir"       default:"~/repos"                        validate:"required"`
	PathTemplate string `koanf:"path_template"  yaml:"path_template"  default:"{{.Host}}/{{.Owner}}/{{.Name}}"  validate:"required"`
	// Layout names a common path_template: owner/name, host/owner/name or name.
	Layout        string `koanf:"layout" yaml:"layout,omitempty" validate:"omitempty,oneof=owner/name host/owner/name name"`
	CloneProtocol string `koanf:"clone_protocol" yaml:"clone_protocol" default:"ssh"                            validate:"oneof=ssh https"`
	Concurrency   int    `koanf:"concurrency"   yaml:"concurrency"   default:"8"                                    validate:"min=1,max=128"`
	// LockTimeout is how long a command waits for another gitten run to
	// finish changing state before giving up.
	LockTimeout time.Duration   `koanf:"lock_timeout" yaml:"lock_timeout" default:"2m" validate:"min=0"`
	Accounts    []Account       `koanf:"accounts"      yaml:"accounts,omitempty" validate:"dive"`
	Providers   []Provider      `koanf:"providers"     yaml:"providers"      validate:"dive"`
	Discovery   []DiscoveryRule `koanf:"discovery"     yaml:"discovery"      validate:"dive"`
	Repos       []ManualRepo    `koanf:"repos"         yaml:"repos,omitempty" validate:"dive"`
	// Manifest is a shared file of repos tracked by hand -- in a dotfiles
	// repo or a synced folder -- that every machine reads as more repos:
	// entries and that clone, adopt, manifest import and remove keep up to
	// date. Empty for none.
	Manifest   string           `koanf:"manifest" yaml:"manifest,omitempty"`
	Exclude    []ExcludeRule    `koanf:"exclude"       yaml:"exclude"        validate:"dive"`
	Sets       []RepoSet        `koanf:"sets"          yaml:"sets,omitempty" validate:"dive"`
	Overrides  []Override       `koanf:"overrides"     yaml:"overrides"      validate:"dive"`
	Clone      CloneConfig      `koanf:"clone"         yaml:"clone,omitempty"`
	Sync       SyncConfig       `koanf:"sync"          yaml:"sync"`
	Cleanup    CleanupConfig    `koanf:"cleanup"       yaml:"cleanup"`
	Backup     BackupCfg        `koanf:"backup"        yaml:"backup"`
	RemoteSync RemoteSyncConfig `koanf:"remote_sync"   yaml:"remote_sync"`
	Reconcile  ReconcileConfig  `koanf:"reconcile"     yaml:"reconcile"`
	Notify     NotifyConfig     `koanf:"notify"        yaml:"notify"`
	Hooks      HooksConfig      `koanf:"hooks"         yaml:"hooks"`
	Mirror     MirrorConfig     `koanf:"mirror"        yaml:"mirror"`
	// Editor is the command `gitten open` opens a path with; empty for
	// $VISUAL, then $EDITOR.
	Editor string    `koanf:"editor" yaml:"editor,omitempty"`
	Log    LogConfig `koanf:"log"           yaml:"log"`

	Agents            []AgentProfile    `koanf:"agents"             yaml:"agents,omitempty"     validate:"dive"`
	WorkspaceDefaults WorkspaceDefaults `koanf:"workspace_defaults" yaml:"workspace_defaults"`
	Workspaces        []Workspace       `koanf:"workspaces"         yaml:"workspaces,omitempty" validate:"dive"`
	Fleets            []Fleet           `koanf:"fleets"             yaml:"fleets,omitempty"     validate:"dive"`
	Launchers         LaunchersConfig   `koanf:"launchers"          yaml:"launchers"`
}

// Account is one identity on one or more hosts: the SSH key and commit
// identity used for the repos it covers. It covers a repo when an override,
// the repo's `repos:` entry, its discovery rule or that rule's provider names
// it, or else when the repo's host is in Hosts and its owner in Owners (an
// account with no Owners covers every owner on its hosts that no other
// account claims).
type Account struct {
	Name    string      `koanf:"name"    yaml:"name"              validate:"required"`
	Hosts   []string    `koanf:"hosts"   yaml:"hosts,omitempty"`
	Owners  []string    `koanf:"owners"  yaml:"owners,omitempty"`
	SSHKey  string      `koanf:"ssh_key" yaml:"ssh_key,omitempty"`
	Git     GitIdentity `koanf:"git"     yaml:"git,omitempty"`
	Signing Signing     `koanf:"signing" yaml:"signing,omitempty"`
	// RemoteSync sets remote_sync for the account's repos, under any
	// override's.
	RemoteSync *RemoteSyncOverride `koanf:"remote_sync" yaml:"remote_sync,omitempty"`
	// Clone shapes new clones of the account's repos, under any override's.
	Clone *CloneOverride `koanf:"clone" yaml:"clone,omitempty"`
	// Hooks sets hooks for the account's repos, under any override's.
	Hooks *HooksOverride `koanf:"hooks" yaml:"hooks,omitempty"`
	// Mirror turns mirroring on or off for the account's repos, under any
	// override's.
	Mirror *MirrorOverride `koanf:"mirror" yaml:"mirror,omitempty"`
}

// Signing is how commits and tags in an account's repos are signed.
type Signing struct {
	// Mode "inherit" leaves signing to your global git config, "required"
	// signs every commit and tag, "off" turns signing off in these repos, so
	// an agent never stalls on a passphrase prompt.
	Mode string `koanf:"mode" yaml:"mode,omitempty" default:"inherit" validate:"omitempty,oneof=inherit required off"`
	// Format is git's gpg.format: ssh, openpgp or x509. Empty means ssh for a
	// key ending in .pub or given as key::..., openpgp otherwise.
	Format string `koanf:"format" yaml:"format,omitempty" validate:"omitempty,oneof=ssh openpgp x509"`
	// Key is user.signingkey: an SSH key path or key::<public key>, a GPG key
	// ID, or an X.509 certificate ID.
	Key string `koanf:"key" yaml:"key,omitempty"`
	// Program signs in place of ssh-keygen, gpg or gpgsm, such as 1Password's
	// op-ssh-sign.
	Program string `koanf:"program" yaml:"program,omitempty"`
	// AllowedSigners is gpg.ssh.allowedSignersFile, for verifying SSH
	// signatures.
	AllowedSigners string `koanf:"allowed_signers" yaml:"allowed_signers,omitempty"`
}

// SigningFormat is the gpg.format the account signs with.
func (s Signing) SigningFormat() string {
	switch {
	case s.Format != "":
		return s.Format
	case strings.HasSuffix(s.Key, ".pub") || strings.HasPrefix(s.Key, "key::"):
		return "ssh"
	default:
		return "openpgp"
	}
}

// GitIdentity is the commit identity written into each repo an account
// covers, as user.name and user.email. Empty fields inherit your global git
// config.
type GitIdentity struct {
	Name  string `koanf:"name"  yaml:"name,omitempty"`
	Email string `koanf:"email" yaml:"email,omitempty"`
}

// DefaultPathTemplate is path_template's default.
const DefaultPathTemplate = "{{.Host}}/{{.Owner}}/{{.Name}}"

// Layouts maps each layout preset to its path_template.
var Layouts = map[string]string{
	"owner/name":      "{{.Owner}}/{{.Name}}",
	"host/owner/name": DefaultPathTemplate,
	"name":            "{{.Name}}",
}

// ManualRepo allows specifying individual repos to manage without discovery rules.
type ManualRepo struct {
	URL     string   `koanf:"url"     yaml:"url"              validate:"required"`
	Tags    []string `koanf:"tags"    yaml:"tags,omitempty"`
	SSHKey  string   `koanf:"ssh_key" yaml:"ssh_key,omitempty"`
	Account string   `koanf:"account" yaml:"account,omitempty"`
	// Path puts the repo somewhere other than where path_template does:
	// absolute or ~-relative, or relative to its base_dir.
	Path string `koanf:"path" yaml:"path,omitempty"`
}

// Provider represents a GitHub, GitLab, or Gitea/Forgejo instance.
type Provider struct {
	Name         string `koanf:"name"          yaml:"name"                    validate:"required"`
	Type         string `koanf:"type"          yaml:"type"                    validate:"required,oneof=github gitlab gitea forgejo"`
	URL          string `koanf:"url"           yaml:"url,omitempty"`
	Token        string `koanf:"token"          yaml:"token,omitempty"          sensitive:"true"`
	TokenEnv     string `koanf:"token_env"      yaml:"token_env,omitempty"`
	TokenCommand string `koanf:"token_command"  yaml:"token_command,omitempty"`
	SSHKey       string `koanf:"ssh_key"        yaml:"ssh_key,omitempty"`
	Account      string `koanf:"account"        yaml:"account,omitempty"`
	// CloneProtocol sets the transport for repos discovered from this provider
	// ("ssh" or "https"); empty falls back to the top-level clone_protocol.
	CloneProtocol string `koanf:"clone_protocol" yaml:"clone_protocol,omitempty" validate:"omitempty,oneof=ssh https"`
}

// DiscoveryRule defines how to find repos from a provider.
type DiscoveryRule struct {
	Name            string   `koanf:"name"             yaml:"name"                    validate:"required"`
	Provider        string   `koanf:"provider"         yaml:"provider"                validate:"required"`
	User            string   `koanf:"user"             yaml:"user,omitempty"`
	Org             string   `koanf:"org"              yaml:"org,omitempty"`
	Group           string   `koanf:"group"            yaml:"group,omitempty"`
	Query           string   `koanf:"query"            yaml:"query,omitempty"`
	IncludeForks    bool     `koanf:"include_forks"    yaml:"include_forks,omitempty"`
	IncludeArchived bool     `koanf:"include_archived" yaml:"include_archived,omitempty"`
	Visibility      string   `koanf:"visibility"       yaml:"visibility,omitempty"    validate:"omitempty,oneof=all public private internal"`
	Languages       []string `koanf:"languages"        yaml:"languages,omitempty"`
	Topics          []string `koanf:"topics"           yaml:"topics,omitempty"`
	NamePattern     string   `koanf:"name_pattern"     yaml:"name_pattern,omitempty"  validate:"omitempty,valid_regex"`
	// Affiliation narrows a GitHub rule with no user, org or query -- the
	// token's own repos -- to repos the account owns, collaborates on, or
	// sees through an organization. Empty means all three, GitHub's default.
	Affiliation []string `koanf:"affiliation" yaml:"affiliation,omitempty" validate:"dive,oneof=owner collaborator organization_member"`
	Tags        []string `koanf:"tags"             yaml:"tags,omitempty"`
	SSHKey      string   `koanf:"ssh_key"          yaml:"ssh_key,omitempty"`
	Account     string   `koanf:"account"          yaml:"account,omitempty"`
}

// ExcludeRule defines repos to skip.
type ExcludeRule struct {
	NamePattern string   `koanf:"name_pattern" yaml:"name_pattern,omitempty" validate:"omitempty,valid_regex"`
	Repos       []string `koanf:"repos"        yaml:"repos,omitempty"`
}

// RepoSet is a named, reusable selection of tracked repos, usable via --set on
// repo-operating commands. A repo is in the set if its key matches any Repos
// glob OR it carries any of Tags (both empty = all repos); ExcludeArchived then
// drops archived repos.
type RepoSet struct {
	Name  string   `koanf:"name"  yaml:"name"            validate:"required"`
	Repos []string `koanf:"repos" yaml:"repos,omitempty"`
	Tags  []string `koanf:"tags"  yaml:"tags,omitempty"`
	// Accounts selects the repos these accounts cover; Rules the repos these
	// discovery rules found.
	Accounts        []string `koanf:"accounts"         yaml:"accounts,omitempty"`
	Rules           []string `koanf:"rules"            yaml:"rules,omitempty"`
	ExcludeArchived bool     `koanf:"exclude_archived" yaml:"exclude_archived,omitempty"`
}

// FindSet returns the named set, or false if no set has that name.
func (c *Config) FindSet(name string) (*RepoSet, bool) {
	for i := range c.Sets {
		if c.Sets[i].Name == name {
			return &c.Sets[i], true
		}
	}
	return nil, false
}

// Override allows per-repo config overrides. The Sync/Backup/RemoteSync
// blocks use dedicated override types with pointer fields so that an unset
// field is distinguishable from one explicitly set to its zero value.
// (Using the plain *SyncConfig/*BackupCfg here meant a bare `backup:` block
// silently forced enabled=false, disabling backups for matched repos.)
type Override struct {
	Repos         []string            `koanf:"repos"          yaml:"repos"                    validate:"required,min=1"`
	BaseDir       string              `koanf:"base_dir"       yaml:"base_dir,omitempty"`
	SSHKey        string              `koanf:"ssh_key"        yaml:"ssh_key,omitempty"`
	Account       string              `koanf:"account"        yaml:"account,omitempty"`
	PathTemplate  string              `koanf:"path_template"  yaml:"path_template,omitempty"`
	CloneProtocol string              `koanf:"clone_protocol" yaml:"clone_protocol,omitempty" validate:"omitempty,oneof=ssh https"`
	Sync          *SyncOverride       `koanf:"sync"          yaml:"sync,omitempty"`
	Backup        *BackupOverride     `koanf:"backup"        yaml:"backup,omitempty"`
	RemoteSync    *RemoteSyncOverride `koanf:"remote_sync"   yaml:"remote_sync,omitempty"`
	Clone         *CloneOverride      `koanf:"clone"         yaml:"clone,omitempty"`
	Hooks         *HooksOverride      `koanf:"hooks"         yaml:"hooks,omitempty"`
	Mirror        *MirrorOverride     `koanf:"mirror" yaml:"mirror,omitempty"`
}

// SyncOverride overrides SyncConfig fields per repo. A nil pointer means "not set".
type SyncOverride struct {
	Enabled          *bool          `koanf:"enabled"            yaml:"enabled,omitempty"`
	AutoDiscover     *bool          `koanf:"auto_discover"      yaml:"auto_discover,omitempty"`
	MinFetchInterval *time.Duration `koanf:"min_fetch_interval" yaml:"min_fetch_interval,omitempty" validate:"omitempty,min=0"`
	TimeoutPerRepo   *time.Duration `koanf:"timeout_per_repo"   yaml:"timeout_per_repo,omitempty"   validate:"omitempty,min=0"`
	Retries          *int           `koanf:"retries"            yaml:"retries,omitempty"            validate:"omitempty,min=0,max=10"`
	RetryBackoff     *time.Duration `koanf:"retry_backoff"      yaml:"retry_backoff,omitempty"      validate:"omitempty,min=0"`
	Update           *string        `koanf:"update"             yaml:"update,omitempty"             validate:"omitempty,oneof=off ff-only-clean"`
}

// BackupOverride overrides BackupCfg fields per repo. A nil pointer means "not set".
type BackupOverride struct {
	Enabled             *bool          `koanf:"enabled"           yaml:"enabled,omitempty"`
	Strategy            *string        `koanf:"strategy"          yaml:"strategy,omitempty"          validate:"omitempty,oneof=stash-ref patch both"`
	IncludeUntracked    *bool          `koanf:"include_untracked" yaml:"include_untracked,omitempty"`
	PatchDir            *string        `koanf:"patch_dir"         yaml:"patch_dir,omitempty"`
	KeepAll             *time.Duration `koanf:"keep_all"               yaml:"keep_all,omitempty"               validate:"omitempty,min=0"`
	KeepDaily           *time.Duration `koanf:"keep_daily"             yaml:"keep_daily,omitempty"             validate:"omitempty,min=0"`
	MaxPerCopy          *int           `koanf:"max_per_copy"           yaml:"max_per_copy,omitempty"           validate:"omitempty,min=0"`
	MaxUntrackedFileMB  *int           `koanf:"max_untracked_file_mb"  yaml:"max_untracked_file_mb,omitempty"  validate:"omitempty,min=0"`
	MaxUntrackedTotalMB *int           `koanf:"max_untracked_total_mb" yaml:"max_untracked_total_mb,omitempty" validate:"omitempty,min=0"`
}

// RemoteSyncOverride overrides RemoteSyncConfig fields for an account or a
// repo. A nil pointer means "not set".
type RemoteSyncOverride struct {
	Enabled   *bool          `koanf:"enabled"   yaml:"enabled,omitempty"`
	Remote    *string        `koanf:"remote"    yaml:"remote,omitempty"    validate:"omitempty,min=1"`
	WIP       *string        `koanf:"wip"       yaml:"wip,omitempty"       validate:"omitempty,oneof=off ref branch"`
	Unpushed  *string        `koanf:"unpushed"  yaml:"unpushed,omitempty"  validate:"omitempty,oneof=off bundle push"`
	Namespace *string        `koanf:"namespace" yaml:"namespace,omitempty"`
	Keep      *time.Duration `koanf:"keep"      yaml:"keep,omitempty"      validate:"omitempty,min=0"`
}

// apply sets the fields o sets.
func (o *RemoteSyncOverride) apply(r *RemoteSyncConfig) {
	if o == nil {
		return
	}
	if o.Enabled != nil {
		r.Enabled = *o.Enabled
	}
	if o.Remote != nil {
		r.Remote = *o.Remote
	}
	if o.WIP != nil {
		r.WIP = *o.WIP
	}
	if o.Unpushed != nil {
		r.Unpushed = *o.Unpushed
	}
	if o.Namespace != nil {
		r.Namespace = *o.Namespace
	}
	if o.Keep != nil {
		r.Keep = *o.Keep
	}
}

// CloneConfig shapes new clones of tracked repos. Workspace worktrees share
// their repo's clone, and so its filter and depth.
type CloneConfig struct {
	// Filter is a partial clone filter: blob:none, blob:limit=<n>[kmg] or
	// tree:<depth>. Empty clones every object.
	Filter string `koanf:"filter" yaml:"filter,omitempty"`
	// Depth limits history to that many commits on every branch; 0 is all.
	Depth int `koanf:"depth" yaml:"depth,omitempty" validate:"min=0"`
	// Submodules checks out each new checkout's submodules, recursively,
	// cloned with Filter and Depth too.
	Submodules bool `koanf:"submodules" yaml:"submodules,omitempty"`
	// LFS is "auto", where Git LFS downloads what each checkout needs, or
	// "skip", where LFS files stay pointers and their content is never
	// downloaded -- kept as lfs.fetchexclude=* in the repo's git config.
	LFS string `koanf:"lfs" yaml:"lfs,omitempty" default:"auto" validate:"omitempty,oneof=auto skip"`
	// Sparse lists the only directories new checkouts hold (cone mode: the
	// files at the top level, and directly in each listed directory's
	// parents, come too). Empty checks out everything.
	Sparse []string `koanf:"sparse" yaml:"sparse,omitempty"`
}

// SkipLFS reports whether Git LFS content is never downloaded.
func (c CloneConfig) SkipLFS() bool { return c.LFS == "skip" }

// CloneOverride sets CloneConfig fields for an account or repos. A nil
// pointer means "not set"; an empty sparse list checks out everything.
type CloneOverride struct {
	Filter     *string   `koanf:"filter"     yaml:"filter,omitempty"`
	Depth      *int      `koanf:"depth"      yaml:"depth,omitempty"      validate:"omitempty,min=0"`
	Submodules *bool     `koanf:"submodules" yaml:"submodules,omitempty"`
	LFS        *string   `koanf:"lfs"        yaml:"lfs,omitempty"        validate:"omitempty,oneof=auto skip"`
	Sparse     *[]string `koanf:"sparse"     yaml:"sparse,omitempty"`
}

func (o *CloneOverride) apply(c *CloneConfig) {
	if o == nil {
		return
	}
	if o.Filter != nil {
		c.Filter = *o.Filter
	}
	if o.Depth != nil {
		c.Depth = *o.Depth
	}
	if o.Submodules != nil {
		c.Submodules = *o.Submodules
	}
	if o.LFS != nil {
		c.LFS = *o.LFS
	}
	if o.Sparse != nil {
		c.Sparse = *o.Sparse
	}
}

// SyncConfig controls sync behavior.
type SyncConfig struct {
	Enabled          bool          `koanf:"enabled"            yaml:"enabled"            default:"true"`
	AutoDiscover     bool          `koanf:"auto_discover"      yaml:"auto_discover"      default:"true"`
	MinFetchInterval time.Duration `koanf:"min_fetch_interval" yaml:"min_fetch_interval" default:"10m"    validate:"min=0"`
	TimeoutPerRepo   time.Duration `koanf:"timeout_per_repo"   yaml:"timeout_per_repo"   default:"5m"     validate:"min=0"` // one repo's fetch
	BackupTimeout    time.Duration `koanf:"backup_timeout"     yaml:"backup_timeout"     default:"2m"     validate:"min=0"` // one repo's backup
	CloneTimeout     time.Duration `koanf:"clone_timeout"      yaml:"clone_timeout"      default:"30m"    validate:"min=0"` // one clone, or one archive of a clone before it is deleted
	InspectTimeout   time.Duration `koanf:"inspect_timeout"    yaml:"inspect_timeout"    default:"1m"     validate:"min=0"` // one look at a working copy while planning
	APITimeout       time.Duration `koanf:"api_timeout"        yaml:"api_timeout"        default:"60s"    validate:"min=0"` // one provider API request attempt
	DiscoveryTimeout time.Duration `koanf:"discovery_timeout"  yaml:"discovery_timeout"  default:"5m"     validate:"min=0"` // one discovery rule, retries and rate-limit waits included
	Retries          int           `koanf:"retries"            yaml:"retries"            default:"2"      validate:"min=0,max=10"`
	RetryBackoff     time.Duration `koanf:"retry_backoff"      yaml:"retry_backoff"      default:"2s"     validate:"min=0"`
	// Update is what sync does to a tracked clone after fetching it: "off"
	// leaves its branch where it is; "ff-only-clean" fast-forwards it to its
	// upstream when it is behind and not ahead, has no tracked changes and
	// git isn't busy in it.
	Update string `koanf:"update" yaml:"update" default:"off" validate:"oneof=off ff-only-clean"`
}

// CleanupConfig controls stale repo handling.
type CleanupConfig struct {
	Policy        string        `koanf:"policy"         yaml:"policy"         default:"mark_stale" validate:"required,oneof=mark_stale remove remove_and_delete archive ignore"`
	GracePeriod   time.Duration `koanf:"grace_period"   yaml:"grace_period"   default:"720h"       validate:"min=0"`
	ProtectManual bool          `koanf:"protect_manual" yaml:"protect_manual" default:"true"`
}

// Untracks reports whether the policy stops tracking repos stale past the
// grace period.
func (c CleanupConfig) Untracks() bool {
	return c.Policy == "remove" || c.TakesClones()
}

// TakesClones reports whether the policy also takes their clones away:
// remove_and_delete deletes them, archive moves them into the archive.
func (c CleanupConfig) TakesClones() bool {
	return c.Policy == "remove_and_delete" || c.Policy == "archive"
}

// BackupCfg controls backup behavior (renamed to avoid collision with gitops.BackupConfig).
type BackupCfg struct {
	Enabled          bool   `koanf:"enabled"           yaml:"enabled"           default:"true"`
	Strategy         string `koanf:"strategy"          yaml:"strategy"          default:"both"  validate:"required,oneof=stash-ref patch both"`
	IncludeUntracked bool   `koanf:"include_untracked" yaml:"include_untracked" default:"true"`
	PatchDir         string `koanf:"patch_dir"         yaml:"patch_dir"`
	// Retention per working copy: every backup younger than KeepAll, then the
	// newest of each day younger than KeepDaily, then at most MaxPerCopy.
	KeepAll    time.Duration `koanf:"keep_all"     yaml:"keep_all"     default:"24h"  validate:"min=0"`
	KeepDaily  time.Duration `koanf:"keep_daily"   yaml:"keep_daily"   default:"336h" validate:"min=0"`
	MaxPerCopy int           `koanf:"max_per_copy" yaml:"max_per_copy" default:"100"  validate:"min=0"`
	// Untracked files over these limits are left out of a backup and reported.
	MaxUntrackedFileMB  int `koanf:"max_untracked_file_mb"  yaml:"max_untracked_file_mb"  default:"50"  validate:"min=0"`
	MaxUntrackedTotalMB int `koanf:"max_untracked_total_mb" yaml:"max_untracked_total_mb" default:"500" validate:"min=0"`
	// ArchiveDir receives what gitten keeps of a clone or worktree before
	// deleting it -- a verified bundle of its unique work: uncommitted
	// changes, untracked files, stashes, commits no remote has -- and whole
	// clones archived instead of deleted. A clone it can't inspect or archive
	// isn't deleted, unless asked to discard its work.
	ArchiveDir string `koanf:"archive_dir" yaml:"archive_dir"`
}

// ReconcileConfig controls how config changes reach repos already on disk.
type ReconcileConfig struct {
	// Auto is what sync and discover --apply change on their own: "safe" applies
	// changes that move or delete nothing (clone, set the remote URL, write
	// managed git config, retag), "all" also moves repos, "none" only reports.
	// `gitten apply` always applies everything.
	Auto string `koanf:"auto" yaml:"auto" default:"safe" validate:"oneof=safe all none"`
	// Drift is what happens when a value gitten set (a remote URL, a managed
	// git config key) was changed by hand: "report" leaves it, "overwrite"
	// puts gitten's value back.
	Drift string `koanf:"drift" yaml:"drift" default:"report" validate:"oneof=report overwrite"`
}

// RemoteSyncConfig is what of a working copy goes offsite, and where. An
// account or an override can set it for the repos it covers.
type RemoteSyncConfig struct {
	// Enabled makes sync push offsite on its own; `gitten remote-sync`
	// pushes on demand either way.
	Enabled bool `koanf:"enabled" yaml:"enabled"`
	// Remote is a remote name, or a URL template ({{.Host}}, {{.Owner}},
	// {{.Name}}, {{.FullName}}) for a separate backup remote.
	Remote string `koanf:"remote" yaml:"remote" default:"origin" validate:"required"`
	// WIP is how uncommitted work goes offsite: "ref" pushes each new backup
	// under Namespace, hidden from branch lists and CI; "branch" pushes a
	// visible branch per copy; "off" pushes none.
	WIP string `koanf:"wip" yaml:"wip" default:"ref" validate:"oneof=off ref branch"`
	// Unpushed protects commits no remote has: "push" saves each such branch
	// under Namespace, "bundle" writes them to a local bundle in
	// backup.archive_dir, "off" does neither. The branches themselves are
	// never pushed.
	Unpushed string `koanf:"unpushed" yaml:"unpushed" default:"push" validate:"oneof=off bundle push"`
	// Namespace is the hidden ref prefix each copy owns on the remote. It must
	// include {{.Copy}} so copies never overwrite each other.
	Namespace string `koanf:"namespace" yaml:"namespace" default:"refs/gitten/{{.Machine}}/{{.Copy}}"`
	// Keep is how long a pushed WIP ref stays; the current one always stays.
	Keep time.Duration `koanf:"keep" yaml:"keep" default:"720h" validate:"min=0"`
}

// NotifyConfig controls OS notifications.
type NotifyConfig struct {
	Enabled      bool `koanf:"enabled"       yaml:"enabled"`
	OnError      bool `koanf:"on_error"      yaml:"on_error"      default:"true"`
	OnCompletion bool `koanf:"on_completion" yaml:"on_completion"`
	OnStale      bool `koanf:"on_stale"      yaml:"on_stale"      default:"true"`
	// OnWaiting notifies when an agent is waiting for you.
	OnWaiting bool `koanf:"on_waiting" yaml:"on_waiting" default:"true"`
	// WaitingAfter is how long an agent in a tmux pane must be quiet, after
	// working, to count as waiting for you; 0 stops watching panes, leaving
	// only what agents say themselves (`gitten ws waiting`).
	WaitingAfter time.Duration `koanf:"waiting_after" yaml:"waiting_after" default:"30s" validate:"min=0"`
	// Timeout bounds sending one notification.
	Timeout time.Duration `koanf:"timeout" yaml:"timeout" default:"10s" validate:"min=0"`
}

// LogConfig controls logging.
type LogConfig struct {
	Level  string `koanf:"level"  yaml:"level"  default:"info"  validate:"oneof=debug info warn error"`
	Format string `koanf:"format" yaml:"format" default:"json"  validate:"oneof=json text"`
	File   string `koanf:"file"   yaml:"file"`
	Events bool   `koanf:"events" yaml:"events" default:"true"` // write the operation log (events.jsonl)
}
