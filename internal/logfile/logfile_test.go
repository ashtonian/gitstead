package logfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRotate(t *testing.T) {
	const bound = 16
	tests := []struct {
		name        string
		log, older  string // "" leaves the file out
		wantRotated bool
		wantLog     string // "" means path is gone
		wantOlder   string
	}{
		{name: "missing", wantRotated: false},
		{name: "at the bound", log: strings.Repeat("a", bound), wantLog: strings.Repeat("a", bound)},
		{name: "past the bound", log: strings.Repeat("b", bound+1), wantRotated: true, wantOlder: strings.Repeat("b", bound+1)},
		{name: "replaces the older generation", log: strings.Repeat("c", bound+1), older: "old\n", wantRotated: true, wantOlder: strings.Repeat("c", bound+1)},
		{name: "older generation alone", older: "old\n", wantOlder: "old\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "gitstead.log")
			write(t, path, tt.log)
			write(t, Rotated(path), tt.older)

			rotated, err := Rotate(path, bound)
			if err != nil {
				t.Fatal(err)
			}
			if rotated != tt.wantRotated {
				t.Errorf("rotated = %v, want %v", rotated, tt.wantRotated)
			}
			if got := read(t, path); got != tt.wantLog {
				t.Errorf("log = %q, want %q", got, tt.wantLog)
			}
			if got := read(t, Rotated(path)); got != tt.wantOlder {
				t.Errorf("older generation = %q, want %q", got, tt.wantOlder)
			}
		})
	}
}

// A writer that opened the log before the rotation -- launchd's for the run
// in progress -- keeps appending to the rotated file, and the next open
// starts a new log: no line is lost.
func TestRotate_OpenWriterLosesNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gitstead.log")
	w, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if _, err := w.WriteString("before the rotation\n"); err != nil {
		t.Fatal(err)
	}

	if rotated, err := Rotate(path, 4); err != nil || !rotated {
		t.Fatalf("Rotate = %v, %v; want a rotation", rotated, err)
	}
	if _, err := w.WriteString("after it\n"); err != nil {
		t.Fatal(err)
	}
	next, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := next.WriteString("next run\n"); err != nil {
		t.Fatal(err)
	}
	next.Close()

	if got, want := read(t, Rotated(path)), "before the rotation\nafter it\n"; got != want {
		t.Errorf("older generation = %q, want %q", got, want)
	}
	if got, want := read(t, path), "next run\n"; got != want {
		t.Errorf("log = %q, want %q", got, want)
	}
}

// While another process rotates the log, Rotate leaves it to that one.
func TestRotate_AnotherRotating(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gitstead.log")
	write(t, path, strings.Repeat("d", 32))
	unlock, err := tryLock(path + ".lock")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()

	if rotated, err := Rotate(path, 16); err != nil || rotated {
		t.Errorf("Rotate = %v, %v; want it to leave the log to the lock holder", rotated, err)
	}
	if got := read(t, path); len(got) != 32 {
		t.Errorf("log has %d bytes, want 32", len(got))
	}
}

func write(t *testing.T, path, data string) {
	t.Helper()
	if data == "" {
		return
	}
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

// read is path's content, or "" when it doesn't exist.
func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
