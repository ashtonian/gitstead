package cmd

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ashtonian/gitstead/internal/logfile"
)

// The logger follows log.level, log.format and log.file; --quiet quiets the
// terminal, not the file.
func TestLogger_FollowsLogSettings(t *testing.T) {
	old := slog.Default()
	t.Cleanup(func() { slog.SetDefault(old) })
	dir := t.TempDir()
	logFile := filepath.Join(dir, "logs", "gitstead.log")
	cfg := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfg, []byte("log:\n  level: warn\n  format: text\n  file: "+logFile+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := &Context{Config: cfg, Quiet: true}
	log := ctx.Logger()
	log.Info("left out")
	log.Warn("kept", "repo", "github.com/acme/api")
	slog.Warn("through the default logger")

	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	if strings.Contains(got, "left out") || !strings.Contains(got, "level=WARN msg=kept repo=github.com/acme/api") ||
		!strings.Contains(got, "through the default logger") {
		t.Errorf("log file:\n%s", got)
	}
}

// A log.file past its bound is moved aside when gitstead opens it, and the
// run logs to a new one.
func TestLogger_RotatesLogFile(t *testing.T) {
	old := slog.Default()
	t.Cleanup(func() { slog.SetDefault(old) })
	dir := t.TempDir()
	logFile := filepath.Join(dir, "gitstead.log")
	sized(t, logFile, logfile.MaxBytes+1)
	cfg := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfg, []byte("log:\n  level: warn\n  file: "+logFile+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	(&Context{Config: cfg, Quiet: true}).Logger().Warn("after the rotation")

	if info, err := os.Stat(logfile.Rotated(logFile)); err != nil || info.Size() != logfile.MaxBytes+1 {
		t.Errorf("older generation: %v, %v; want the whole old log", info, err)
	}
	data, err := os.ReadFile(logFile)
	if err != nil || !strings.Contains(string(data), "after the rotation") || len(data) > 1024 {
		t.Errorf("new log: %d bytes, %v; want only this run's line", len(data), err)
	}
}
