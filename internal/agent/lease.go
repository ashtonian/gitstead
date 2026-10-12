package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// ErrRunning reports an agent whose lease another run holds.
var ErrRunning = errors.New("the agent is already running")

// Holder is what a lease file says about the run holding it.
type Holder struct {
	PID      int       `json:"pid"`
	Profile  string    `json:"profile,omitempty"`
	Command  string    `json:"command,omitempty"`
	Launcher string    `json:"launcher,omitempty"`
	Session  string    `json:"session,omitempty"`
	Started  time.Time `json:"started"`
}

// Lease is an agent's claim on its replica while it runs: an exclusive
// flock on a file under the state directory. The kernel releases it when
// the holder exits, however it exits, so a held lease always means a live
// run.
type Lease struct {
	f *os.File
}

// LeasePath is where the lease of an agent in a workspace lives, under dir
// (the state file's directory).
func LeasePath(dir, workspace, agent string) string {
	return filepath.Join(dir, "runs", workspace, agent+".lease")
}

// Acquire takes the lease at path for holder, without waiting. It returns
// ErrRunning, with what the lease says of the run holding it, when another
// run holds it.
func Acquire(path string, holder Holder) (*Lease, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := flockRetry(f); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			if h, ok := Read(path); ok {
				return nil, fmt.Errorf("%w (pid %d, since %s)", ErrRunning, h.PID, h.Started.Local().Format(time.DateTime))
			}
			return nil, ErrRunning
		}
		return nil, fmt.Errorf("locking %s: %w", path, err)
	}
	data, err := json.Marshal(holder)
	if err == nil {
		if err = f.Truncate(0); err == nil {
			_, err = f.WriteAt(data, 0)
		}
	}
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("writing %s: %w", path, err)
	}
	return &Lease{f: f}, nil
}

// flockRetry takes an exclusive lock on f, retrying briefly: a Read checking
// whether the lease is held takes it for a moment.
func flockRetry(f *os.File) error {
	var err error
	for range 5 {
		if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); !errors.Is(err, syscall.EWOULDBLOCK) {
			return err
		}
		time.Sleep(20 * time.Millisecond)
	}
	return err
}

// Release gives the lease up.
func (l *Lease) Release() {
	if l == nil || l.f == nil {
		return
	}
	_ = l.f.Truncate(0)
	_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	l.f.Close()
	l.f = nil
}

// Read returns the run holding the lease at path; false when none does.
func Read(path string) (Holder, bool) {
	f, err := os.Open(path)
	if err != nil {
		return Holder{}, false
	}
	defer f.Close()
	// A lease we can take is held by no one.
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); err == nil {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		return Holder{}, false
	}
	var h Holder
	data, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(data, &h) != nil {
		return Holder{PID: 0}, true
	}
	return h, true
}

// Wait waits up to timeout for the lease at path to be released, checking
// every poll. It reports whether it was.
func Wait(path string, timeout, poll time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if _, held := Read(path); !held {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(poll)
	}
}

// Forget removes the lease file of an agent no longer running, such as one
// whose replica was removed.
func Forget(path string) {
	if _, held := Read(path); !held {
		if err := os.Remove(path); err == nil || errors.Is(err, fs.ErrNotExist) {
			_ = os.Remove(filepath.Dir(path)) // the workspace's, once empty
		}
	}
}
