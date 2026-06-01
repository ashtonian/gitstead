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
)

// Event is one logged action.
type Event struct {
	Time   time.Time `json:"time"`
	Action string    `json:"action"` // clone|sync|backup|remove|wg_create|wg_remove
	Repo   string    `json:"repo,omitempty"`
	Detail string    `json:"detail,omitempty"`
}

// Append writes ev as a JSON line to path, creating the parent dir. Returns an
// error, but callers generally ignore it so logging can't break a command.
func Append(path string, ev Event) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	line, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	_, err = f.Write(append(line, '\n'))
	return err
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
