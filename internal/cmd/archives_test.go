package cmd

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ashtonian/git-mgr/internal/config"
	"github.com/ashtonian/git-mgr/internal/state"
)

// An archive in the archive directory that isn't on record -- its run was
// interrupted before recording it -- is listed, and a submodule's archive is
// reached only through its parent's.
func TestCatalog_FindsArchivesNotOnRecord(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	sub := filepath.Join(dir, "app-lib-1.json")
	write("recorded-1.json", `{"source": "/r", "work": "x", "created": "2026-10-13T00:00:00Z", "bundle": "`+filepath.Join(dir, "recorded-1.bundle")+`"}`)
	write("app-1.json", `{"source": "/src/app", "branch": "main", "work": "1 unpushed commit", "created": "2026-10-13T00:00:00Z", "bundle": "`+filepath.Join(dir, "app-1.bundle")+`", "submodules": {"lib": "`+sub+`"}}`)
	write("app-lib-1.json", `{"source": "/src/app/lib", "work": "1 untracked file", "created": "2026-10-13T00:00:00Z"}`)
	cfg := &config.Config{Backup: config.BackupCfg{ArchiveDir: dir}}
	st := &state.State{Archives: []state.ArchiveEntry{{ID: "recorded-1", Path: filepath.Join(dir, "recorded-1.bundle"), Created: time.Now()}}}

	all := catalog(cfg, st)
	if len(all) != 2 || all[1].ID != "app-1" || !all[1].Uncatalogued || all[1].Path != filepath.Join(dir, "app-1.bundle") || all[1].Source != "/src/app" {
		t.Fatalf("catalog = %+v", all)
	}
	if n := uncatalogued(cfg, st); n != 1 {
		t.Errorf("uncatalogued = %d", n)
	}
	if a, err := catalogEntry(cfg, st, "app-1"); err != nil || a.Work != "1 unpushed commit" {
		t.Errorf("catalogEntry = %+v, %v", a, err)
	}
}
