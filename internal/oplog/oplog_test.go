package oplog

import (
	"fmt"
	"path/filepath"
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
