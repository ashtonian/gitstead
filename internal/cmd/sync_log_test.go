package cmd

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/ashtonian/gitstead/internal/config"
	"github.com/ashtonian/gitstead/internal/logfile"
	"github.com/ashtonian/gitstead/internal/state"
)

// A sync bounds the background sync's output: the file its schedule names,
// the default file, which a schedule another tool installed writes without a
// record, and each recorded schedule's own.
func TestRotateScheduleLogs(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	dir := t.TempDir()
	defaultLog := config.DefaultScheduleLogPath()
	customLog := filepath.Join(dir, "custom.log")
	smallLog := filepath.Join(dir, "small.log")
	namedLog := filepath.Join(dir, "named.log") // another tool's schedule, unrecorded
	sized(t, defaultLog, logfile.MaxBytes+1)
	sized(t, namedLog, logfile.MaxBytes+1)
	sized(t, customLog, logfile.MaxBytes+1)
	sized(t, smallLog, 100)

	st := &state.State{Schedules: map[string]*state.ScheduleState{
		"default": {Name: "default", LogPath: defaultLog},
		"custom":  {Name: "custom", LogPath: customLog},
		"small":   {Name: "small", LogPath: smallLog},
		"systemd": {Name: "systemd"}, // logs to the journal
	}}
	rotateScheduleLogs(st, namedLog, slog.New(slog.DiscardHandler))

	for _, p := range []string{defaultLog, customLog, namedLog} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s is still in place (%v); want it moved aside", p, err)
		}
		if info, err := os.Stat(logfile.Rotated(p)); err != nil || info.Size() != logfile.MaxBytes+1 {
			t.Errorf("%s: older generation %v, %v; want the whole log", p, info, err)
		}
	}
	if info, err := os.Stat(smallLog); err != nil || info.Size() != 100 {
		t.Errorf("%s under its bound: %v, %v; want it left alone", smallLog, info, err)
	}
}

// sized makes path a file of size bytes, sparse so the test writes nothing.
func sized(t *testing.T, path string, size int64) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := f.Truncate(size); err != nil {
		t.Fatal(err)
	}
}
