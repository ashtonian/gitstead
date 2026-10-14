package cmd

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The logger follows log.level, log.format and log.file; --quiet quiets the
// terminal, not the file.
func TestLogger_FollowsLogSettings(t *testing.T) {
	old := slog.Default()
	t.Cleanup(func() { slog.SetDefault(old) })
	dir := t.TempDir()
	logFile := filepath.Join(dir, "logs", "gitten.log")
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
