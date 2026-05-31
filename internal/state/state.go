package state

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"
)

// State is the top-level state structure persisted to disk.
type State struct {
	Version    int                        `yaml:"version"`
	LastSync   time.Time                  `yaml:"last_sync"`
	Repos      map[string]*RepoState      `yaml:"repos"`
	Workgroups map[string]*WorkgroupState `yaml:"workgroups,omitempty"`

	// Internal: path to the state file for Save()
	path string `yaml:"-"`
	// Internal: file lock
	lockFD *os.File `yaml:"-"`
}

// RepoState tracks the state of a single managed repo.
type RepoState struct {
	// Identity
	CloneURL string `yaml:"clone_url"`
	Host     string `yaml:"host"`
	Owner    string `yaml:"owner"`
	Name     string `yaml:"name"`
	FullName string `yaml:"full_name"`
	Provider string `yaml:"provider"` // provider instance name

	// Local State
	LocalPath     string `yaml:"local_path"`
	Cloned        bool   `yaml:"cloned"`
	DefaultBranch string `yaml:"default_branch"`
	HeadCommit    string `yaml:"head_commit"`
	CurrentBranch string `yaml:"current_branch"`

	// Metadata
	DiscoveredBy string   `yaml:"discovered_by"`
	IsFork       bool     `yaml:"is_fork"`
	IsArchived   bool     `yaml:"is_archived"`
	Language     string   `yaml:"language"`
	Topics       []string `yaml:"topics"`
	Tags         []string `yaml:"tags"`
	Description  string   `yaml:"description"`

	// Sync State
	LastFetch          time.Time `yaml:"last_fetch"`
	LastFetchError     string    `yaml:"last_fetch_error"`
	LastBackup         time.Time `yaml:"last_backup"`
	LastBackupRef      string    `yaml:"last_backup_ref"`
	LastBackupHasPatch bool      `yaml:"last_backup_has_patch"`
	Status             string    `yaml:"status"` // clean, dirty, conflict, error, uncloned, missing, stale
	Error              string    `yaml:"error"`

	// Discovery State
	FirstSeen      time.Time `yaml:"first_seen"`
	LastDiscovered time.Time `yaml:"last_discovered"`
	StaleSince     time.Time `yaml:"stale_since,omitempty"`
	ManuallyAdded  bool      `yaml:"manually_added"`
}

// WorkgroupState is the observed on-disk state of a managed workgroup.
type WorkgroupState struct {
	Name      string                 `yaml:"name"`
	Kind      string                 `yaml:"kind"`
	Root      string                 `yaml:"root"`
	Source    string                 `yaml:"source,omitempty"`
	BaseRef   string                 `yaml:"base_ref,omitempty"`
	Members   []WorkgroupMemberState `yaml:"members"`
	CreatedAt time.Time              `yaml:"created_at"`
	LastCheck time.Time              `yaml:"last_check,omitempty"`
}

// WorkgroupMemberState is one working directory within a workgroup.
type WorkgroupMemberState struct {
	Name     string `yaml:"name"`
	Path     string `yaml:"path"`
	Branch   string `yaml:"branch,omitempty"`
	RepoURL  string `yaml:"repo_url,omitempty"`
	RepoKey  string `yaml:"repo_key,omitempty"`
	Upstream string `yaml:"upstream,omitempty"`
}

// Load reads the state file from disk. Creates a new empty state if the file doesn't exist.
func Load(path string) (*State, error) {
	path = expandHome(path)

	s := &State{
		Version:    1,
		Repos:      make(map[string]*RepoState),
		Workgroups: make(map[string]*WorkgroupState),
		path:       path,
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, fmt.Errorf("reading state file %s: %w", path, err)
	}

	if err := yaml.Unmarshal(data, s); err != nil {
		return nil, fmt.Errorf("parsing state file %s: %w", path, err)
	}
	s.path = path

	if s.Repos == nil {
		s.Repos = make(map[string]*RepoState)
	}
	if s.Workgroups == nil {
		s.Workgroups = make(map[string]*WorkgroupState)
	}

	return s, nil
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
// any held lock. Call it after AcquireLock so a mutation works from the latest
// on-disk data and cannot clobber a concurrent writer's changes.
func (s *State) Reload() error {
	fresh, err := Load(s.path)
	if err != nil {
		return err
	}
	s.Version = fresh.Version
	s.LastSync = fresh.LastSync
	s.Repos = fresh.Repos
	s.Workgroups = fresh.Workgroups
	return nil
}

// AcquireLock acquires an exclusive flock on the state file.
// Returns an error if another instance is already running.
func (s *State) AcquireLock() error {
	lockPath := s.path + ".lock"
	dir := filepath.Dir(lockPath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("creating lock dir: %w", err)
	}

	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return fmt.Errorf("opening lock file: %w", err)
	}

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return fmt.Errorf("another git-mgr instance is running")
	}

	s.lockFD = f
	return nil
}

// ReleaseLock releases the flock.
func (s *State) ReleaseLock() {
	if s.lockFD != nil {
		syscall.Flock(int(s.lockFD.Fd()), syscall.LOCK_UN)
		s.lockFD.Close()
		s.lockFD = nil
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
