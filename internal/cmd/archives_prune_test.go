package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/ashtonian/gitstead/internal/config"
	"github.com/ashtonian/gitstead/internal/gitops"
	"github.com/ashtonian/gitstead/internal/state"
)

// archiveDir is an archive directory as gitstead leaves it: each archive a
// description beside its bundle, or its archived clone.
type archiveDir struct {
	t   *testing.T
	dir string
	now time.Time
}

// add writes archive id, made age ago: meta's bundle as 1 KiB, or its clone
// as a directory holding 1 KiB. It returns the path the catalog knows it
// by.
func (d archiveDir) add(id string, age time.Duration, meta gitops.ArchiveMeta) string {
	d.t.Helper()
	base := filepath.Join(d.dir, id)
	meta.Created = d.now.Add(-age)
	path := base + ".json"
	switch {
	case meta.Clone != "":
		meta.Clone, path = base, base
		d.write(filepath.Join(base, ".git", "packed"), 1024)
	case meta.Bundle != "":
		meta.Bundle, path = base+".bundle", base+".bundle"
		d.write(meta.Bundle, 1024)
	}
	desc, err := json.Marshal(meta)
	if err != nil {
		d.t.Fatal(err)
	}
	if err := os.WriteFile(base+".json", desc, 0o600); err != nil {
		d.t.Fatal(err)
	}
	return path
}

func (d archiveDir) write(path string, size int) {
	d.t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		d.t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0o600); err != nil {
		d.t.Fatal(err)
	}
}

func (d archiveDir) exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func TestPruneArchives(t *testing.T) {
	const day = 24 * time.Hour
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	const app, lib, tool = "github.com/acme/app", "github.com/acme/lib", "github.com/acme/tool"

	setup := func(t *testing.T) (*Context, *config.Config, *state.State, archiveDir, map[string]string) {
		d := archiveDir{t: t, dir: t.TempDir(), now: now}
		paths := map[string]string{
			// A copy's thin archive past retention, and a young one.
			"copy-old":   d.add("copy-old", 200*day, gitops.ArchiveMeta{Source: "/ws/a/app", Bundle: "x", Repo: lib, Base: "/repos/lib"}),
			"copy-young": d.add("copy-young", 10*day, gitops.ArchiveMeta{Source: "/ws/b/app", Bundle: "x", Repo: app, Base: "/repos/app"}),
			// app's whole history, which copy-young may need.
			"app-whole": d.add("app-whole", 300*day, gitops.ArchiveMeta{Source: "/repos/app", Bundle: "x", Complete: true, Repo: app}),
			// lib's whole history, needed only by copy-old, which goes too.
			"lib-whole": d.add("lib-whole", 300*day, gitops.ArchiveMeta{Source: "/repos/lib", Bundle: "x", Complete: true, Repo: lib}),
			// tool's archived clone, needed by nothing.
			"tool-clone": d.add("tool-clone", 400*day, gitops.ArchiveMeta{Source: "/repos/tool", Clone: "x", Repo: tool}),
			// Left by a run interrupted before recording it.
			"stray": d.add("stray", 250*day, gitops.ArchiveMeta{Source: "/ws/c/app", Bundle: "x", Complete: true}),
		}
		ctx := &Context{State: filepath.Join(t.TempDir(), "state.yaml"), Config: filepath.Join(t.TempDir(), "config.yaml"), Ctx: t.Context()}
		st, err := ctx.LoadState()
		if err != nil {
			t.Fatal(err)
		}
		for _, id := range []string{"copy-old", "copy-young", "app-whole", "lib-whole", "tool-clone"} {
			st.Archives = append(st.Archives, archiveEntry(paths[id], "", ""))
		}
		// On record, its files deleted by hand.
		st.Archives = append(st.Archives, state.ArchiveEntry{ID: "gone", Path: filepath.Join(d.dir, "gone.bundle"), Created: now.Add(-500 * day)})
		if err := st.Save(); err != nil {
			t.Fatal(err)
		}
		cfg := &config.Config{Backup: config.BackupCfg{ArchiveDir: d.dir, ArchiveRetention: 180 * day}, LockTimeout: time.Second}
		return ctx, cfg, st, d, paths
	}
	ids := func(res archivePrune) []string {
		var out []string
		for _, a := range res.Deleted {
			out = append(out, a.ID)
		}
		slices.Sort(out)
		return out
	}
	wantGone := []string{"copy-old", "gone", "lib-whole", "stray", "tool-clone"}

	t.Run("deletes what retention passed, keeping what a kept archive needs", func(t *testing.T) {
		ctx, cfg, st, d, paths := setup(t)
		res, err := pruneArchives(ctx, cfg, st, now, false)
		if err != nil {
			t.Fatal(err)
		}
		if got := ids(res); !slices.Equal(got, wantGone) {
			t.Errorf("deleted %v, want %v", got, wantGone)
		}
		if len(res.Kept) != 1 || res.Kept[0].ID != "app-whole" {
			t.Errorf("kept %+v, want app-whole, which copy-young needs", res.Kept)
		}
		if want := int64(4 * 1024); res.Bytes < want {
			t.Errorf("bytes = %d, want at least the four archives' %d", res.Bytes, want)
		}
		for _, id := range wantGone {
			if p := paths[id]; p != "" && (d.exists(p) || d.exists(filepath.Join(d.dir, id+".json"))) {
				t.Errorf("%s is still on disk", id)
			}
		}
		for _, id := range []string{"copy-young", "app-whole"} {
			if !d.exists(paths[id]) {
				t.Errorf("%s was deleted", id)
			}
		}
		fresh, err := state.Load(ctx.StatePath())
		if err != nil {
			t.Fatal(err)
		}
		var left []string
		for _, a := range fresh.Archives {
			left = append(left, a.ID)
		}
		slices.Sort(left)
		if !slices.Equal(left, []string{"app-whole", "copy-young"}) {
			t.Errorf("catalog = %v, want the kept archives only", left)
		}
	})

	t.Run("a dry run deletes nothing", func(t *testing.T) {
		ctx, cfg, st, d, paths := setup(t)
		res, err := pruneArchives(ctx, cfg, st, now, true)
		if err != nil {
			t.Fatal(err)
		}
		if got := ids(res); !slices.Equal(got, wantGone) {
			t.Errorf("would delete %v, want %v", got, wantGone)
		}
		for id, p := range paths {
			if !d.exists(p) {
				t.Errorf("a dry run deleted %s", id)
			}
		}
		if fresh, _ := state.Load(ctx.StatePath()); len(fresh.Archives) != 6 {
			t.Errorf("a dry run changed the catalog: %d entries", len(fresh.Archives))
		}
	})

	t.Run("an unreadable description keeps every whole-history archive", func(t *testing.T) {
		ctx, cfg, st, d, _ := setup(t)
		d.write(filepath.Join(d.dir, "broken.json"), 0)
		res, err := pruneArchives(ctx, cfg, st, now, false)
		if err != nil {
			t.Fatal(err)
		}
		if got, want := ids(res), []string{"copy-old", "gone"}; !slices.Equal(got, want) {
			t.Errorf("deleted %v, want only %v", got, want)
		}
	})
}
