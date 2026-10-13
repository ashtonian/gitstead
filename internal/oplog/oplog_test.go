package oplog

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestTrim(t *testing.T) {
	path := filepath.Join(t.TempDir(), "e.jsonl")
	for i := 0; i < 100; i++ {
		if err := Append(path, Event{Time: time.Now(), Action: "x", Detail: fmt.Sprint(i)}); err != nil {
			t.Fatal(err)
		}
	}
	trim(path, 10)
	evs, _ := Read(path, time.Time{})
	if len(evs) != 10 {
		t.Fatalf("after trim: %d events, want 10", len(evs))
	}
	if evs[9].Detail != "99" || evs[0].Detail != "90" { // newest kept, in order
		t.Errorf("trim kept wrong window: first=%s last=%s", evs[0].Detail, evs[9].Detail)
	}
}

func TestAppendRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "events.jsonl") // parent dir doesn't exist
	t0 := time.Now()
	for _, ev := range []Event{
		{Time: t0.Add(-2 * time.Hour), Action: "clone", Repo: "a"},
		{Time: t0.Add(-30 * time.Minute), Action: "backup", Repo: "b", Detail: "ref"},
		{Time: t0, Action: "sync", Detail: "fetched=3"},
	} {
		if err := Append(path, ev); err != nil {
			t.Fatalf("append: %v", err)
		}
	}

	all, err := Read(path, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("got %d events, want 3", len(all))
	}
	if all[0].Action != "clone" || all[2].Action != "sync" || all[1].Detail != "ref" {
		t.Errorf("round-trip content wrong: %+v", all)
	}

	recent, _ := Read(path, t0.Add(-time.Hour))
	if len(recent) != 2 {
		t.Errorf("since filter: got %d, want 2", len(recent))
	}

	if evs, err := Read(filepath.Join(t.TempDir(), "none.jsonl"), time.Time{}); err != nil || evs != nil {
		t.Errorf("missing file should be empty/no-error: got %v, %v", evs, err)
	}
}

func TestAppend_RedactsURLCredentials(t *testing.T) {
	path := filepath.Join(t.TempDir(), "e.jsonl")
	if err := Append(path, Event{Time: time.Now(), Action: "clone", Detail: "https://me:s3cret@github.com/acme/api.git"}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "s3cret") {
		t.Fatalf("event log holds a credential: %s", data)
	}
}

// Appends from several runs while the log is trimmed lose nothing: a trim
// keeps the newest lines, and these appends are all newer than the rest.
// The log starts past its bound, so the first appends race a trim.
func TestAppend_ConcurrentWithTrims(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	pad := strings.Repeat("x", 800)
	var prefill strings.Builder
	for i := 0; i < keepLines; i++ {
		line, _ := json.Marshal(Event{Time: time.Unix(0, 0), Action: "old", Detail: pad})
		prefill.Write(append(line, '\n'))
	}
	if prefill.Len() <= maxBytes {
		t.Fatalf("fixture: %d bytes doesn't pass the bound", prefill.Len())
	}
	if err := os.WriteFile(path, []byte(prefill.String()), 0o600); err != nil {
		t.Fatal(err)
	}

	const writers, each = 8, 300
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Go(func() {
			for i := 0; i < each; i++ {
				if err := Append(path, Event{Time: time.Now(), Action: "new", Detail: fmt.Sprintf("%d-%d", w, i)}); err != nil {
					t.Error(err)
				}
			}
		})
	}
	wg.Wait()

	events, err := Read(path, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, ev := range events {
		if ev.Action == "new" {
			seen[ev.Detail] = true
		}
	}
	if len(seen) != writers*each {
		t.Errorf("%d of %d appended events survived", len(seen), writers*each)
	}
	if info, err := os.Stat(path); err != nil || info.Size() > maxBytes {
		t.Errorf("the log was never trimmed: %v, %v", info.Size(), err)
	}
}
