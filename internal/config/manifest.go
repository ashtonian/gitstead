package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/ashtonian/git-mgr/internal/pathutil"
	"github.com/ashtonian/git-mgr/internal/repourl"
)

// Manifest is a portable list of repos: what `manifest export` writes and
// `manifest import` reads, and the shared file `manifest:` names, which every
// machine reads as more `repos:` entries and git-mgr keeps up to date as
// repos are added and removed by hand.
type Manifest struct {
	Repos []ManualRepo `yaml:"repos"`
}

// manifestHeader starts every manifest git-mgr writes.
const manifestHeader = "# Repos tracked by hand, on any machine that reads this file. git-mgr keeps it up to date:\n# clone, adopt and manifest import add to it, remove takes from it.\n"

// manifestLockWait bounds how long a write waits for another git-mgr's.
const manifestLockWait = 30 * time.Second

// ReadManifest reads the manifest at path. A missing file is an empty
// manifest; a malformed one is an error.
func ReadManifest(path string) (Manifest, error) {
	var m Manifest
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return m, fmt.Errorf("reading manifest %s: %w", path, err)
	}
	if err := yaml.Unmarshal(data, &m); err != nil {
		return m, fmt.Errorf("manifest %s: %w", path, err)
	}
	for _, r := range m.Repos {
		if err := checkManualRepo(r); err != nil {
			return m, fmt.Errorf("manifest %s: %w", path, err)
		}
	}
	return m, nil
}

// UpdateManifest applies change to the manifest at path and writes it, under
// a lock other git-mgr runs on this machine take too, and atomically, so a
// reader -- or a sync tool copying it to another machine -- never sees half
// a file.
func UpdateManifest(path string, change func(*Manifest) error) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	lock, err := manifestLock(path)
	if err != nil {
		return err
	}
	unlock, err := lockFile(lock, manifestLockWait)
	if err != nil {
		return fmt.Errorf("locking manifest %s: %w", path, err)
	}
	defer unlock()
	m, err := ReadManifest(path)
	if err != nil {
		return err
	}
	if err := change(&m); err != nil {
		return err
	}
	var b bytes.Buffer
	b.WriteString(manifestHeader)
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(m); err != nil {
		return err
	}
	return pathutil.WriteFileAtomic(path, b.Bytes(), 0o644)
}

// Upsert adds r, or replaces the entry for the same repo.
func (m *Manifest) Upsert(r ManualRepo) {
	key := manualKey(r)
	if i := slices.IndexFunc(m.Repos, func(e ManualRepo) bool { return manualKey(e) == key }); i >= 0 {
		m.Repos[i] = r
		return
	}
	m.Repos = append(m.Repos, r)
}

// Remove drops the entry for repo key, reporting whether there was one.
func (m *Manifest) Remove(key string) bool {
	n := len(m.Repos)
	m.Repos = slices.DeleteFunc(m.Repos, func(e ManualRepo) bool { return manualKey(e) == key })
	return len(m.Repos) < n
}

// Has reports whether the manifest has an entry for repo key.
func (m Manifest) Has(key string) bool {
	return slices.ContainsFunc(m.Repos, func(e ManualRepo) bool { return manualKey(e) == key })
}

func manualKey(r ManualRepo) string {
	if id, err := repourl.Parse(r.URL); err == nil {
		return id.Key()
	}
	return r.URL
}

// checkManualRepo checks a repos: or manifest entry.
func checkManualRepo(r ManualRepo) error {
	if r.URL == "" {
		return errors.New("an entry has no url")
	}
	if err := repourl.CheckNoCredentials(r.URL); err != nil {
		return fmt.Errorf("repos entry: %w", err)
	}
	if _, err := repourl.Parse(r.URL); err != nil {
		return fmt.Errorf("repos entry %s: %w", r.URL, err)
	}
	return nil
}

// PortablePath is how a manifest says where a repo is: "" where config puts
// it anyway (templated), relative to its base_dir under that, ~-relative
// under your home, else absolute.
func PortablePath(local, templated, baseDir string) string {
	local = filepath.Clean(local)
	if local == filepath.Clean(templated) {
		return ""
	}
	if rel, err := filepath.Rel(baseDir, local); err == nil && !strings.HasPrefix(rel, "..") {
		return rel
	}
	if home, err := os.UserHomeDir(); err == nil {
		if rel, err := filepath.Rel(home, local); err == nil && !strings.HasPrefix(rel, "..") {
			return "~/" + filepath.ToSlash(rel)
		}
	}
	return local
}

// ManualPath is where a repos: or manifest entry's path puts repo key:
// absolute or ~-relative as given, otherwise under the repo's base_dir.
func (c *Config) ManualPath(key, path string) string {
	path = ExpandHome(path)
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Join(ExpandHome(c.ResolveForRepo(key).BaseDir), path)
}

// loadManifest adds the shared manifest's repos to cfg.Repos, checked as
// repos: entries are: an account an entry names must be one of config's.
func loadManifest(cfg *Config) error {
	if cfg.Manifest == "" {
		return nil
	}
	m, err := ReadManifest(cfg.Manifest)
	if err != nil {
		return err
	}
	for _, r := range m.Repos {
		if r.Account != "" && cfg.FindAccount(r.Account) == nil {
			return fmt.Errorf("manifest %s: repos entry %s names unknown account %q", cfg.Manifest, r.URL, r.Account)
		}
	}
	cfg.Repos = append(cfg.Repos, m.Repos...)
	return nil
}

// RepoEntry returns the repos: or shared-manifest entry for repo key.
func (c *Config) RepoEntry(key string) (ManualRepo, bool) {
	for _, r := range c.Repos {
		if manualKey(r) == key {
			return r, true
		}
	}
	return ManualRepo{}, false
}

// MatchesRepo reports whether a repos: entry, as a config file's YAML node,
// is repo key's.
func MatchesRepo(key string) func(*yaml.Node) bool {
	return func(n *yaml.Node) bool {
		var r ManualRepo
		return n.Decode(&r) == nil && manualKey(r) == key
	}
}

// manifestLock is the lock file for the manifest at path: in git-mgr's cache
// directory, named for the manifest's path, never beside it, where it would
// end up committed to the dotfiles repo it sits in.
func manifestLock(path string) (string, error) {
	dir := filepath.Join(CacheDir(), "locks")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(abs))
	return filepath.Join(dir, "manifest-"+hex.EncodeToString(sum[:8])+".lock"), nil
}

// lockFile takes an exclusive flock on path, retrying until wait passes.
func lockFile(path string, wait time.Duration) (func(), error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(wait)
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() {
				syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
				f.Close()
			}, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) || time.Now().After(deadline) {
			f.Close()
			return nil, err
		}
		time.Sleep(100 * time.Millisecond)
	}
}
