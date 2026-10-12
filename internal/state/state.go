package state

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"
)

// CurrentVersion is the state schema this build reads and writes.
const CurrentVersion = 2

// ErrLocked reports that another git-mgr run holds the state lock.
var ErrLocked = errors.New("another git-mgr instance is running")

// ErrNewerState reports a state file written by a newer git-mgr. It is never
// read or rewritten, so an older binary can't drop fields it doesn't know.
var ErrNewerState = errors.New("state file is newer than this git-mgr")

// State is the top-level state structure persisted to disk.
type State struct {
	Version    int                        `yaml:"version"`
	LastSync   time.Time                  `yaml:"last_sync"`
	Repos      map[string]*RepoState      `yaml:"repos"`
	Workgroups map[string]*WorkgroupState `yaml:"workgroups,omitempty"`
	Schedules  map[string]*ScheduleState  `yaml:"schedules,omitempty"`

	// Internal: path to the state file for Save()
	path string `yaml:"-"`
	// Internal: file lock
	lockFD *os.File `yaml:"-"`
}

// RepoState tracks the state of a single managed repo. Each field carries a
// json tag matching its yaml tag so `git-mgr status --json` emits the same
// snake_case keys used in config and the on-disk state file.
type RepoState struct {
	// Identity. CloneURL and LocalPath, below, are the origin URL and path
	// git-mgr last applied; the reconciler compares them with what config wants.
	CloneURL string `yaml:"clone_url" json:"clone_url"`
	Host     string `yaml:"host" json:"host"`
	Owner    string `yaml:"owner" json:"owner"`
	Name     string `yaml:"name" json:"name"`
	FullName string `yaml:"full_name" json:"full_name"`
	Provider string `yaml:"provider" json:"provider"` // provider instance name
	// ProviderID is the hosting provider's ID for the repository, stable across
	// renames and transfers on the same host; "" until discovery has seen it.
	ProviderID string `yaml:"provider_id,omitempty" json:"provider_id,omitempty"`
	// CopyID identifies the working copy on disk (see gitops.CopyID).
	CopyID string `yaml:"copy_id,omitempty" json:"copy_id,omitempty"`

	// Local State
	LocalPath     string `yaml:"local_path" json:"local_path"`
	Cloned        bool   `yaml:"cloned" json:"cloned"`
	DefaultBranch string `yaml:"default_branch" json:"default_branch"`
	HeadCommit    string `yaml:"head_commit" json:"head_commit"`
	CurrentBranch string `yaml:"current_branch" json:"current_branch"`

	// Metadata
	DiscoveredBy string   `yaml:"discovered_by" json:"discovered_by"`
	IsFork       bool     `yaml:"is_fork" json:"is_fork"`
	IsArchived   bool     `yaml:"is_archived" json:"is_archived"`
	Language     string   `yaml:"language" json:"language"`
	Languages    []string `yaml:"languages,omitempty" json:"languages,omitempty"`
	Topics       []string `yaml:"topics" json:"topics"`
	Tags         []string `yaml:"tags" json:"tags"`
	// UserTags are the tags given by hand (clone/adopt --tag, a manifest,
	// workgroup tracking). Tags is them plus the tags of the rules that find
	// the repo.
	UserTags    []string `yaml:"user_tags,omitempty" json:"user_tags,omitempty"`
	Description string   `yaml:"description" json:"description"`

	// Sync State
	LastFetch      time.Time `yaml:"last_fetch" json:"last_fetch"`
	LastFetchError string    `yaml:"last_fetch_error" json:"last_fetch_error"`
	BackupStatus   `yaml:",inline"`
	Status         string `yaml:"status" json:"status"` // clean, dirty, conflict, error, uncloned, missing, stale
	Error          string `yaml:"error" json:"error"`

	// Discovery State
	FirstSeen      time.Time `yaml:"first_seen" json:"first_seen"`
	LastDiscovered time.Time `yaml:"last_discovered" json:"last_discovered"`
	StaleSince     time.Time `yaml:"stale_since,omitempty" json:"stale_since,omitempty"`
	// ManuallyAdded marks a repo added by hand (clone, adopt, manifest import,
	// workgroup tracking): its path and URL are the ones recorded, never
	// derived from config.
	ManuallyAdded bool `yaml:"manually_added" json:"manually_added"`

	// ManagedConfig holds the repo-local git config keys git-mgr set, with the
	// values it set, keyed by git's lower-case name (core.sshcommand).
	ManagedConfig map[string]string `yaml:"managed_config,omitempty" json:"managed_config,omitempty"`
}

// WorkgroupState is the observed on-disk state of a managed workgroup. Like
// RepoState, each field carries a json tag mirroring its yaml tag so JSON output
// uses the same snake_case keys as the config and state file.
type WorkgroupState struct {
	Name    string                 `yaml:"name"                json:"name"`
	Kind    string                 `yaml:"kind"                json:"kind"`
	Root    string                 `yaml:"root"                json:"root"`
	Source  string                 `yaml:"source,omitempty"    json:"source,omitempty"`
	BaseRef string                 `yaml:"base_ref,omitempty"  json:"base_ref,omitempty"`
	Members []WorkgroupMemberState `yaml:"members"             json:"members"`
	// Declared marks a workgroup created from a config declaration; removing
	// the declaration then means removing the workgroup. Workgroups created
	// from flags or adopted have no declaration and are kept as recorded.
	Declared  bool      `yaml:"declared,omitempty" json:"declared,omitempty"`
	CreatedAt time.Time `yaml:"created_at"          json:"created_at"`
	LastCheck time.Time `yaml:"last_check,omitempty" json:"last_check,omitempty"`
}

// WorkgroupMemberState is one working directory within a workgroup.
type WorkgroupMemberState struct {
	Name     string `yaml:"name"               json:"name"`
	Path     string `yaml:"path"               json:"path"`
	Branch   string `yaml:"branch,omitempty"   json:"branch,omitempty"`
	RepoURL  string `yaml:"repo_url,omitempty" json:"repo_url,omitempty"`
	RepoKey  string `yaml:"repo_key,omitempty" json:"repo_key,omitempty"`
	Upstream string `yaml:"upstream,omitempty" json:"upstream,omitempty"`
	CopyID   string `yaml:"copy_id,omitempty"  json:"copy_id,omitempty"`
	// ManagedConfig holds the git config keys git-mgr set in a clone member,
	// as RepoState.ManagedConfig does for a repo.
	ManagedConfig map[string]string `yaml:"managed_config,omitempty" json:"managed_config,omitempty"`
	BackupStatus  `yaml:",inline"`
}

// ScheduleState is a background sync git-mgr installed, kept so it can be
// listed, checked by doctor and removed by name.
type ScheduleState struct {
	Name     string            `yaml:"name"               json:"name"`
	Kind     string            `yaml:"kind"               json:"kind"` // launchd | systemd | cron
	Daemon   bool              `yaml:"daemon,omitempty"   json:"daemon,omitempty"`
	Interval time.Duration     `yaml:"interval"           json:"interval"`
	Bin      string            `yaml:"bin"                json:"bin"`
	Global   []string          `yaml:"global,omitempty"   json:"global,omitempty"` // --config, --state
	Sync     []string          `yaml:"sync,omitempty"     json:"sync,omitempty"`   // the repo selection
	Env      map[string]string `yaml:"env,omitempty"      json:"env,omitempty"`
	LogPath  string            `yaml:"log_path,omitempty" json:"log_path,omitempty"`
	// Installed is when it was last installed.
	Installed time.Time `yaml:"installed" json:"installed"`
}

// BackupStatus is what the last backup of a working copy did.
type BackupStatus struct {
	// LastBackup is when the working copy's state was last known to be saved,
	// by a new backup or one identical to it; LastBackupID names that backup.
	LastBackup      time.Time `yaml:"last_backup,omitempty" json:"last_backup,omitzero"`
	LastBackupID    string    `yaml:"last_backup_id,omitempty" json:"last_backup_id,omitempty"`
	LastBackupError string    `yaml:"last_backup_error,omitempty" json:"last_backup_error,omitempty"`
}

// RecordBackup stores a backup run's outcome. id is the backup that holds the
// working copy's current state, "" when there is none (a clean tree, or a
// failure); LastBackup moves only when there is one.
func (b *BackupStatus) RecordBackup(id string, err error, now time.Time) {
	b.LastBackupError = ""
	if err != nil {
		b.LastBackupError = err.Error()
	}
	if id != "" {
		b.LastBackup, b.LastBackupID = now, id
	}
}

// Load reads the state file from disk. Creates a new empty state if the file doesn't exist.
func Load(path string) (*State, error) {
	path = expandHome(path)

	s := &State{
		Version:    CurrentVersion,
		Repos:      make(map[string]*RepoState),
		Workgroups: make(map[string]*WorkgroupState),
		Schedules:  make(map[string]*ScheduleState),
		path:       path,
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, fmt.Errorf("reading state file %s: %w", path, err)
	}

	s.Version = 0 // a file without a version predates versioning
	if err := yaml.Unmarshal(data, s); err != nil {
		return nil, fmt.Errorf("parsing state file %s: %w", path, err)
	}
	s.path = path
	if s.Version > CurrentVersion {
		return nil, fmt.Errorf("%w: %s is version %d, this build supports up to %d; upgrade git-mgr", ErrNewerState, path, s.Version, CurrentVersion)
	}

	if s.Repos == nil {
		s.Repos = make(map[string]*RepoState)
	}
	if s.Workgroups == nil {
		s.Workgroups = make(map[string]*WorkgroupState)
	}
	if s.Schedules == nil {
		s.Schedules = make(map[string]*ScheduleState)
	}
	s.migrate()
	return s, nil
}

// migrate upgrades a state read from an older schema in memory; Save writes
// it back as CurrentVersion.
func (s *State) migrate() {
	if s.Version < 2 {
		// Version 1 kept one tag list. Tags on hand-added repos were given by
		// hand; tags on discovered repos came from their rules.
		for _, r := range s.Repos {
			if r.ManuallyAdded && len(r.UserTags) == 0 {
				r.UserTags = slices.Clone(r.Tags)
			}
		}
	}
	s.Version = CurrentVersion
}

// Save writes the state file atomically (write to tmp + rename).
func (s *State) Save() error {
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("creating state dir: %w", err)
	}

	data, err := yaml.Marshal(s)
	if err != nil {
		return fmt.Errorf("marshaling state: %w", err)
	}

	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return fmt.Errorf("writing state tmp: %w", err)
	}

	if err := os.Rename(tmp, s.path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("renaming state tmp: %w", err)
	}

	return nil
}

// Reload re-reads the state file from disk into s, preserving the file path and
// any held lock. Update calls it under the lock, so a change works from the
// latest on-disk data and cannot clobber a concurrent writer's.
func (s *State) Reload() error {
	fresh, err := Load(s.path)
	if err != nil {
		return err
	}
	lock := s.lockFD
	*s = *fresh
	s.lockFD = lock
	return nil
}

// lockPoll is how often a waiting Lock retries.
const lockPoll = 100 * time.Millisecond

// Lock takes the exclusive state lock, waiting up to wait for another run to
// release it (0 means don't wait). It returns ErrLocked if the lock stays
// held. Hold it only around reading, changing and saving state; Update does
// all three.
func (s *State) Lock(wait time.Duration) error {
	f, err := flock(s.path+".lock", wait)
	if err != nil {
		return err
	}
	s.lockFD = f
	return nil
}

// ReleaseLock releases the state lock.
func (s *State) ReleaseLock() {
	if s.lockFD != nil {
		syscall.Flock(int(s.lockFD.Fd()), syscall.LOCK_UN)
		s.lockFD.Close()
		s.lockFD = nil
	}
}

// Update applies change to the latest state as one atomic step: it takes the
// lock (waiting up to wait), re-reads the file, calls change and saves. A
// change that returns an error saves nothing.
func (s *State) Update(wait time.Duration, change func(*State) error) error {
	if err := s.Lock(wait); err != nil {
		return err
	}
	defer s.ReleaseLock()
	if err := s.Reload(); err != nil {
		return err
	}
	if err := change(s); err != nil {
		return err
	}
	return s.Save()
}

// RunLock takes the exclusive lock for a long activity, such as a sync, that
// must not run twice at once. It is separate from the state lock, so other
// commands keep working while the activity runs, and it never waits: a second
// run gets ErrLocked. Call the returned func to release it.
func (s *State) RunLock(name string) (release func(), err error) {
	f, err := flock(s.path+"."+name+".lock", 0)
	if err != nil {
		return nil, err
	}
	return func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}

// flock takes an exclusive lock on path, retrying until wait has passed.
func flock(path string, wait time.Duration) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("creating lock dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opening lock file: %w", err)
	}
	deadline := time.Now().Add(wait)
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		switch {
		case err == nil:
			return f, nil
		case !errors.Is(err, syscall.EWOULDBLOCK):
			f.Close()
			return nil, fmt.Errorf("locking %s: %w", path, err)
		case time.Now().After(deadline):
			f.Close()
			if wait <= 0 {
				return nil, fmt.Errorf("%w (%s is held)", ErrLocked, path)
			}
			return nil, fmt.Errorf("%w (%s still held after %s)", ErrLocked, path, wait)
		}
		time.Sleep(lockPoll)
	}
}

// HasTag checks if a repo has a specific tag.
func (r *RepoState) HasTag(tag string) bool {
	return slices.Contains(r.Tags, tag)
}

func expandHome(path string) string {
	if strings.HasPrefix(path, "~/") || path == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		return filepath.Join(home, path[1:])
	}
	return path
}
