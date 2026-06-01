package config

import (
	"time"
)

// Config is the top-level configuration.
type Config struct {
	BaseDir       string           `koanf:"base_dir"       yaml:"base_dir"       default:"~/repos"                        validate:"required"`
	PathTemplate  string           `koanf:"path_template"  yaml:"path_template"  default:"{{.Host}}/{{.Owner}}/{{.Name}}"  validate:"required"`
	CloneProtocol string           `koanf:"clone_protocol" yaml:"clone_protocol" default:"ssh"                            validate:"oneof=ssh https"`
	Concurrency   int              `koanf:"concurrency"   yaml:"concurrency"   default:"8"                                    validate:"min=1,max=128"`
	Providers     []Provider       `koanf:"providers"     yaml:"providers"      validate:"dive"`
	Discovery     []DiscoveryRule  `koanf:"discovery"     yaml:"discovery"      validate:"dive"`
	Repos         []ManualRepo     `koanf:"repos"         yaml:"repos,omitempty" validate:"dive"`
	Exclude       []ExcludeRule    `koanf:"exclude"       yaml:"exclude"        validate:"dive"`
	Sets          []RepoSet        `koanf:"sets"          yaml:"sets,omitempty" validate:"dive"`
	Overrides     []Override       `koanf:"overrides"     yaml:"overrides"      validate:"dive"`
	Sync          SyncConfig       `koanf:"sync"          yaml:"sync"`
	Cleanup       CleanupConfig    `koanf:"cleanup"       yaml:"cleanup"`
	Backup        BackupCfg        `koanf:"backup"        yaml:"backup"`
	RemoteSync    RemoteSyncConfig `koanf:"remote_sync"   yaml:"remote_sync"`
	Notify        NotifyConfig     `koanf:"notify"        yaml:"notify"`
	Log           LogConfig        `koanf:"log"           yaml:"log"`

	WorkgroupBaseDir string      `koanf:"workgroup_base_dir" yaml:"workgroup_base_dir" default:"~/workgroups"`
	Workgroups       []Workgroup `koanf:"workgroups" yaml:"workgroups,omitempty" validate:"dive"`
}

// ManualRepo allows specifying individual repos to manage without discovery rules.
type ManualRepo struct {
	URL    string   `koanf:"url"     yaml:"url"              validate:"required"`
	Tags   []string `koanf:"tags"    yaml:"tags,omitempty"`
	SSHKey string   `koanf:"ssh_key" yaml:"ssh_key,omitempty"`
}

// Provider represents a GitHub, GitLab, or Gitea/Forgejo instance.
type Provider struct {
	Name         string `koanf:"name"          yaml:"name"                    validate:"required"`
	Type         string `koanf:"type"          yaml:"type"                    validate:"required,oneof=github gitlab gitea forgejo"`
	URL          string `koanf:"url"           yaml:"url,omitempty"`
	Token        string `koanf:"token"         yaml:"token,omitempty"         sensitive:"true"`
	TokenEnv     string `koanf:"token_env"     yaml:"token_env,omitempty"`
	TokenCommand string `koanf:"token_command" yaml:"token_command,omitempty"`
	SSHKey       string `koanf:"ssh_key"       yaml:"ssh_key,omitempty"`
}

// DiscoveryRule defines how to find repos from a provider.
type DiscoveryRule struct {
	Name            string   `koanf:"name"             yaml:"name"                    validate:"required"`
	Provider        string   `koanf:"provider"         yaml:"provider"                validate:"required"`
	User            string   `koanf:"user"             yaml:"user,omitempty"`
	Org             string   `koanf:"org"              yaml:"org,omitempty"`
	Group           string   `koanf:"group"            yaml:"group,omitempty"`
	Query           string   `koanf:"query"            yaml:"query,omitempty"`
	IncludeForks    bool     `koanf:"include_forks"    yaml:"include_forks"`
	IncludeArchived bool     `koanf:"include_archived" yaml:"include_archived"`
	Visibility      string   `koanf:"visibility"       yaml:"visibility,omitempty"    validate:"omitempty,oneof=all public private internal"`
	Languages       []string `koanf:"languages"        yaml:"languages,omitempty"`
	Topics          []string `koanf:"topics"           yaml:"topics,omitempty"`
	NamePattern     string   `koanf:"name_pattern"     yaml:"name_pattern,omitempty"  validate:"omitempty,valid_regex"`
	Tags            []string `koanf:"tags"             yaml:"tags,omitempty"`
	SSHKey          string   `koanf:"ssh_key"          yaml:"ssh_key,omitempty"`
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
	Name            string   `koanf:"name"             yaml:"name"                      validate:"required"`
	Repos           []string `koanf:"repos"            yaml:"repos,omitempty"`
	Tags            []string `koanf:"tags"             yaml:"tags,omitempty"`
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
	SSHKey        string              `koanf:"ssh_key"        yaml:"ssh_key,omitempty"`
	PathTemplate  string              `koanf:"path_template"  yaml:"path_template,omitempty"`
	CloneProtocol string              `koanf:"clone_protocol" yaml:"clone_protocol,omitempty" validate:"omitempty,oneof=ssh https"`
	Sync          *SyncOverride       `koanf:"sync"          yaml:"sync,omitempty"`
	Backup        *BackupOverride     `koanf:"backup"        yaml:"backup,omitempty"`
	RemoteSync    *RemoteSyncOverride `koanf:"remote_sync"   yaml:"remote_sync,omitempty"`
}

// SyncOverride overrides SyncConfig fields per repo. A nil pointer means "not set".
type SyncOverride struct {
	AutoDiscover     *bool          `koanf:"auto_discover"      yaml:"auto_discover,omitempty"`
	MinFetchInterval *time.Duration `koanf:"min_fetch_interval" yaml:"min_fetch_interval,omitempty"`
	TimeoutPerRepo   *time.Duration `koanf:"timeout_per_repo"   yaml:"timeout_per_repo,omitempty"`
	Retries          *int           `koanf:"retries"            yaml:"retries,omitempty"`
	RetryBackoff     *time.Duration `koanf:"retry_backoff"      yaml:"retry_backoff,omitempty"`
}

// BackupOverride overrides BackupCfg fields per repo. A nil pointer means "not set".
type BackupOverride struct {
	Enabled          *bool   `koanf:"enabled"           yaml:"enabled,omitempty"`
	Strategy         *string `koanf:"strategy"          yaml:"strategy,omitempty"          validate:"omitempty,oneof=stash-ref patch both"`
	IncludeUntracked *bool   `koanf:"include_untracked" yaml:"include_untracked,omitempty"`
	MaxPerRepo       *int    `koanf:"max_per_repo"      yaml:"max_per_repo,omitempty"      validate:"omitempty,min=0"`
	PatchDir         *string `koanf:"patch_dir"         yaml:"patch_dir,omitempty"`
	PushBranch       *bool   `koanf:"push_branch"       yaml:"push_branch,omitempty"`
	PushRemote       *string `koanf:"push_remote"       yaml:"push_remote,omitempty"`
}

// RemoteSyncOverride overrides RemoteSyncConfig fields per repo. A nil pointer means "not set".
type RemoteSyncOverride struct {
	Enabled    *bool   `koanf:"enabled"     yaml:"enabled,omitempty"`
	RemoteName *string `koanf:"remote_name" yaml:"remote_name,omitempty"`
	ReposGlob  *string `koanf:"repos_glob"  yaml:"repos_glob,omitempty"`
}

// Workgroup declares a set of working directories (members) managed as a unit
// -- either N worktrees of one source repo (kind "worktree") or N independent
// clones (kind "clone"). Group commands operate on members uniformly.
type Workgroup struct {
	Name    string            `koanf:"name"     yaml:"name"               validate:"required"`
	Kind    string            `koanf:"kind"     yaml:"kind"               validate:"required,oneof=worktree clone"`
	BaseDir string            `koanf:"base_dir" yaml:"base_dir,omitempty"`
	Source  string            `koanf:"source"   yaml:"source,omitempty"`
	BaseRef string            `koanf:"base_ref" yaml:"base_ref,omitempty"`
	FromTag string            `koanf:"from_tag" yaml:"from_tag,omitempty"`
	Members []WorkgroupMember `koanf:"members"  yaml:"members,omitempty"`
	// Sync, when true, registers the workgroup's underlying repo(s) -- the
	// worktree source, or each clone member -- as tracked repos so the normal
	// `git-mgr sync` keeps them fetched. Fetch-only; it never touches member
	// working trees.
	Sync bool `koanf:"sync" yaml:"sync,omitempty"`
}

// WorkgroupMember is one working directory within a workgroup. For "worktree"
// groups Branch is the branch to create/check out; for "clone" groups Repo is
// the SSH URL (or a tracked repo key) to clone.
type WorkgroupMember struct {
	Name   string `koanf:"name"   yaml:"name,omitempty"`
	Branch string `koanf:"branch" yaml:"branch,omitempty"`
	Repo   string `koanf:"repo"   yaml:"repo,omitempty"`
}

// SyncConfig controls sync behavior.
type SyncConfig struct {
	AutoDiscover     bool          `koanf:"auto_discover"      yaml:"auto_discover"      default:"true"`
	MinFetchInterval time.Duration `koanf:"min_fetch_interval" yaml:"min_fetch_interval" default:"10m"    validate:"min=0"`
	TimeoutPerRepo   time.Duration `koanf:"timeout_per_repo"   yaml:"timeout_per_repo"   default:"5m"     validate:"min=0"`
	Retries          int           `koanf:"retries"            yaml:"retries"            default:"2"      validate:"min=0,max=10"`
	RetryBackoff     time.Duration `koanf:"retry_backoff"      yaml:"retry_backoff"      default:"2s"     validate:"min=0"`
}

// CleanupConfig controls stale repo handling.
type CleanupConfig struct {
	Policy        string        `koanf:"policy"         yaml:"policy"         default:"mark_stale" validate:"required,oneof=mark_stale remove remove_and_delete ignore"`
	GracePeriod   time.Duration `koanf:"grace_period"   yaml:"grace_period"   default:"720h"       validate:"min=0"`
	ProtectManual bool          `koanf:"protect_manual" yaml:"protect_manual" default:"true"`
}

// BackupCfg controls backup behavior (renamed to avoid collision with gitops.BackupConfig).
type BackupCfg struct {
	Enabled          bool   `koanf:"enabled"           yaml:"enabled"           default:"true"`
	Strategy         string `koanf:"strategy"          yaml:"strategy"          default:"both"  validate:"required,oneof=stash-ref patch both"`
	IncludeUntracked bool   `koanf:"include_untracked" yaml:"include_untracked" default:"true"`
	MaxPerRepo       int    `koanf:"max_per_repo"      yaml:"max_per_repo"      default:"10"    validate:"min=0"`
	PatchDir         string `koanf:"patch_dir"         yaml:"patch_dir"`
	// PushBranch also force-pushes uncommitted tracked changes to a recoverable
	// branch (git-mgr/wip/<branch>) on PushRemote, so dirty work is saved
	// offsite as a normal, visible branch in addition to the local backup.
	PushBranch bool   `koanf:"push_branch" yaml:"push_branch" default:"false"`
	PushRemote string `koanf:"push_remote" yaml:"push_remote" default:"origin"`
}

// RemoteSyncConfig controls pushing backup refs.
type RemoteSyncConfig struct {
	Enabled    bool   `koanf:"enabled"     yaml:"enabled"`
	RemoteName string `koanf:"remote_name" yaml:"remote_name" default:"origin"`
	ReposGlob  string `koanf:"repos_glob"  yaml:"repos_glob"`
}

// NotifyConfig controls OS notifications.
type NotifyConfig struct {
	Enabled      bool `koanf:"enabled"       yaml:"enabled"`
	OnError      bool `koanf:"on_error"      yaml:"on_error"      default:"true"`
	OnCompletion bool `koanf:"on_completion" yaml:"on_completion"`
	OnStale      bool `koanf:"on_stale"      yaml:"on_stale"      default:"true"`
}

// LogConfig controls logging.
type LogConfig struct {
	Level  string `koanf:"level"  yaml:"level"  default:"info"  validate:"oneof=debug info warn error"`
	Format string `koanf:"format" yaml:"format" default:"json"  validate:"oneof=json text"`
	File   string `koanf:"file"   yaml:"file"`
}
