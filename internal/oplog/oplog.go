// Package oplog is a tiny append-only operation log: one JSON line per git-mgr
// mutation (clone, sync, backup, remove, workgroup create/remove). It exists so
// `git-mgr report`/`log` can answer "what did the (often scheduled) tool do
// lately?" without scraping terminal output. Writing is best-effort and never
// fails the command that emitted the event.
package oplog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ashtonian/git-mgr/internal/repourl"
)

// Growth bound: once the log passes maxBytes, trim it to the newest keepLines
// entries. Generous enough that a 15-minute sync agent takes years to hit it.
const (
	maxBytes  = 4 << 20 // ~4 MiB
	keepLines = 5000
)

// Event is one logged action.
type Event struct {
	Time   time.Time `json:"time"`
	Action string    `json:"action"` // clone|sync|backup|remove|wg_create|wg_remove
	Repo   string    `json:"repo,omitempty"`
	Detail string    `json:"detail,omitempty"`
}

// Append writes ev as a JSON line to path, creating the parent dir. O_APPEND
// makes the single-line write atomic, so concurrent writers don't interleave.
// Returns an error, but callers generally ignore it so logging can't break a
// command.
func Append(path string, ev Event) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
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

// trim rewrites path keeping only the newest keep lines. Best-effort: any error
// (or a rare race with a concurrent append) just leaves the log as-is.
func trim(path string, keep int) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) <= keep {
		return
	}
	out := strings.Join(lines[len(lines)-keep:], "\n") + "\n"
	tmp := path + ".tmp"
	if os.WriteFile(tmp, []byte(out), 0o600) == nil {
		_ = os.Rename(tmp, path)
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
