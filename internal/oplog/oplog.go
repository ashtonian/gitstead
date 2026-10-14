// Package oplog is a tiny append-only operation log: one JSON line per gitstead
// mutation (clone, sync, backup, remove, workspace create/remove, each applied
// config change). It exists so
// `gitstead report`/`log` can answer "what did the (often scheduled) tool do
// lately?" without scraping terminal output. Writing is best-effort and never
// fails the command that emitted the event.
package oplog

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/ashtonian/gitstead/internal/pathutil"
	"github.com/ashtonian/gitstead/internal/repourl"
)

// Growth bound: once the log passes maxBytes, trim it to its newest entries,
// at most keepLines of them in at most half of maxBytes, so it grows a while
// before the next trim. Generous enough that a 15-minute sync agent takes
// years to hit it.
const (
	maxBytes  = 4 << 20 // ~4 MiB
	keepLines = 5000
)

// lockWait bounds how long an append waits for another's; a write takes
// microseconds and a rare trim milliseconds, so only a stuck process waits it
// out.
const lockWait = 10 * time.Second

// Event is one logged action.
type Event struct {
	Time   time.Time `json:"time"`
	Action string    `json:"action"` // clone|sync|backup|remove|ws_create|ws_remove, or an applied change's kind
	Repo   string    `json:"repo,omitempty"`
	Detail string    `json:"detail,omitempty"`
}

// Append writes ev as a JSON line to path, creating the parent dir, and
// trims the log when it has grown past its bound. Appends and trims take
// turns under path.lock, so a trim never drops a line another run appended
// meanwhile. Returns an error, but callers generally ignore it so logging
// can't break a command.
func Append(path string, ev Event) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	unlock, err := lock(path+".lock", lockWait)
	if err != nil {
		return err
	}
	defer unlock()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	ev.Repo, ev.Detail = repourl.Redact(ev.Repo), repourl.Redact(ev.Detail)
	line, err := json.Marshal(ev)
	if err != nil {
		f.Close()
		return err
	}
	_, werr := f.Write(append(line, '\n'))
	fi, _ := f.Stat()
	f.Close()
	if fi != nil && fi.Size() > maxBytes {
		trim(path, keepLines) // best-effort; bounds unbounded growth
	}
	return werr
}

// trim rewrites path keeping its newest lines -- at most keep, in at most
// half of maxBytes -- under the caller's lock. Best-effort: an error leaves
// the log as it is.
func trim(path string, keep int) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	start, size := max(len(lines)-keep, 0), 0
	for i := len(lines) - 1; i >= start; i-- {
		if size += len(lines[i]) + 1; size > maxBytes/2 {
			start = i + 1
			break
		}
	}
	if start == 0 {
		return
	}
	_ = pathutil.WriteFileAtomic(path, []byte(strings.Join(lines[start:], "\n")+"\n"), 0o600)
}

// lock takes the exclusive lock on path, retrying until wait has passed.
func lock(path string, wait time.Duration) (unlock func(), err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(wait)
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() { f.Close() }, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) || time.Now().After(deadline) {
			f.Close()
			return nil, fmt.Errorf("locking %s: %w", path, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Read returns events from path with Time >= since (a zero since returns all).
// A missing file yields no events and no error; malformed lines are skipped.
func Read(path string, since time.Time) ([]Event, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Event
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var ev Event
		if json.Unmarshal([]byte(line), &ev) != nil {
			continue
		}
		if since.IsZero() || !ev.Time.Before(since) {
			out = append(out, ev)
		}
	}
	return out, nil
}
