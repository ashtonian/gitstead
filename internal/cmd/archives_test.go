package cmd

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ashtonian/gitten/internal/config"
	"github.com/ashtonian/gitten/internal/state"
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

// A repo is leant on by any thin bundle of a copy made from it, or of a
// submodule of one, on record or not, and by any archive whose description
// can't be read.
func TestLeansOn(t *testing.T) {
	const key, path = "github.com/o/app", "/src/app"
	tests := []struct {
		name        string
		description string
		recorded    []state.ArchiveEntry
		want        bool
	}{
		{name: "nothing", want: false},
		{name: "on record", recorded: []state.ArchiveEntry{{ID: "a-1", Repo: key, Workspace: "w"}}, want: true},
		{name: "thin bundle naming the repo", description: `{"bundle": "/x.bundle", "repo": "` + key + `", "base": "/elsewhere"}`, want: true},
		{name: "thin bundle based on its clone", description: `{"bundle": "/x.bundle", "base": "/src/app/"}`, want: true},
		{name: "thin bundle of a submodule", description: `{"bundle": "/x.bundle", "base": "/src/app/lib"}`, want: true},
		{name: "complete bundle", description: `{"bundle": "/x.bundle", "complete": true, "repo": "` + key + `", "base": "/src/app"}`, want: false},
		{name: "another repo", description: `{"bundle": "/x.bundle", "repo": "github.com/o/other", "base": "/src/app-2"}`, want: false},
		{name: "archived clone", description: `{"clone": "/x", "repo": "` + key + `"}`, want: false},
		{name: "unreadable", description: `{`, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if tt.description != "" {
				if err := os.WriteFile(filepath.Join(dir, "a-1.json"), []byte(tt.description), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if got := leansOn(&state.State{Archives: tt.recorded}, dir, key, path); got != tt.want {
				t.Errorf("leansOn = %v, want %v", got, tt.want)
			}
		})
	}
}
