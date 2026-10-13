package cmd

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"golang.org/x/term"

	"github.com/ashtonian/git-mgr/internal/gitops"
	"github.com/ashtonian/git-mgr/internal/pathutil"
	"github.com/ashtonian/git-mgr/internal/state"
)

// ArchivesCmd groups the archive catalog's subcommands.
type ArchivesCmd struct {
	List    ArchivesListCmd    `cmd:"" name:"ls" help:"List the archives saved before copies and repos were taken away: bundles of unique work, and whole clones."`
	Show    ArchivesShowCmd    `cmd:"" help:"Show an archive: what it saved, and how to restore it."`
	Restore ArchivesRestoreCmd `cmd:"" help:"Put an archive back: move an archived clone back, or rebuild a clone from a bundle, and track a repo's again."`
	Remove  ArchivesRemoveCmd  `cmd:"" name:"rm" help:"Delete archives, for good, and drop them from the catalog."`
}

// ArchivesListCmd lists the catalog.
type ArchivesListCmd struct {
	Workspace string `help:"Only this workspace's archives." predictor:"workspace"`
	Repo      string `help:"Only archives of repos matching this glob."`
}

func (c *ArchivesListCmd) Run(ctx *Context) error {
	st, err := ctx.LoadState()
	if err != nil {
		return err
	}
	var rows []state.ArchiveEntry
	for _, a := range st.Archives {
		if c.Workspace != "" && a.Workspace != c.Workspace {
			continue
		}
		if c.Repo != "" {
			if ok, _ := filepath.Match(c.Repo, a.Repo); !ok {
				continue
			}
		}
		rows = append(rows, a)
	}
	slices.SortFunc(rows, func(a, b state.ArchiveEntry) int { return b.Created.Compare(a.Created) })
	if ctx.JSON {
		if rows == nil {
			rows = []state.ArchiveEntry{}
		}
		return ctx.Emit(rows)
	}
	if len(rows) == 0 {
		fmt.Println("No archives on record.")
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tCREATED\tFROM\tREPO\tSAVED")
	for _, a := range rows {
		from := tildify(a.Source)
		if a.Workspace != "" {
			from = a.Workspace + "/" + a.Agent
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", a.ID, a.Created.Local().Format(time.DateTime), from, dashIfEmpty(a.Repo), a.Work)
	}
	return tw.Flush()
}

// ArchivesShowCmd shows one archive.
type ArchivesShowCmd struct {
	ID string `arg:"" help:"Archive ID, from archives ls." predictor:"archive"`
}

func (c *ArchivesShowCmd) Run(ctx *Context) error {
	st, err := ctx.LoadState()
	if err != nil {
		return err
	}
	a, err := catalogEntry(st, c.ID)
	if err != nil {
		return err
	}
	meta, _, err := gitops.ReadArchive(a.Path)
	if err != nil {
		return fmt.Errorf("reading %s: %w (was the archive moved or deleted?)", a.Path, err)
	}
	if ctx.JSON {
		return ctx.Emit(struct {
			state.ArchiveEntry
			Description gitops.ArchiveMeta `json:"description"`
		}{a, meta})
	}
	fmt.Printf("Archive %s, made %s\n", a.ID, a.Created.Local().Format(time.DateTime))
	if a.Workspace != "" {
		fmt.Printf("  from:      %s/%s's copy at %s\n", a.Workspace, a.Agent, tildify(a.Source))
	} else {
		fmt.Printf("  from:      %s\n", tildify(a.Source))
	}
	if a.Repo != "" {
		fmt.Printf("  repo:      %s\n", a.Repo)
	}
	if meta.Branch != "" {
		fmt.Printf("  branch:    %s\n", meta.Branch)
	}
	fmt.Printf("  saved:     %s\n", meta.Work)
	for name, url := range meta.Remotes {
		fmt.Printf("  remote:    %s %s\n", name, url)
	}
	switch {
	case meta.Clone != "":
		fmt.Printf("  clone:     %s\n", tildify(meta.Clone))
		fmt.Printf("\nRestore:\n  git-mgr archives restore %s\n", a.ID)
	case meta.Bundle != "":
		fmt.Printf("  bundle:    %s\n", tildify(meta.Bundle))
		switch {
		case meta.Complete:
			fmt.Println("             all of the repository's history: it restores on its own")
		case meta.Base != "":
			fmt.Printf("             only what %s lacked: it restores into a clone of that, or of origin\n", tildify(meta.Base))
		}
		fmt.Printf("\nRestore:\n  git-mgr archives restore %s --to <dir>\nor by hand:\n  %s\n", a.ID, strings.ReplaceAll(meta.Restore, " && ", " &&\n    "))
	}
	for _, sub := range slices.Sorted(maps.Keys(meta.Submodules)) {
		fmt.Printf("  submodule %s: git-mgr archives show %s\n", sub, archiveID(meta.Submodules[sub]))
	}
	if meta.LFS != "" {
		fmt.Printf("  LFS objects: %s -- copy them into the restored repo's .git/lfs/objects\n", tildify(meta.LFS))
	}
	return nil
}

// ArchivesRestoreCmd puts an archive back.
type ArchivesRestoreCmd struct {
	ID   string `arg:"" help:"Archive ID, from archives ls." predictor:"archive"`
	To   string `help:"Restore it here instead of where it was." type:"path"`
	Base string `help:"For a bundle that holds only what its base lacked: clone this instead of the base it records, such as a restored archive of that repo." type:"path"`
}

func (c *ArchivesRestoreCmd) Run(ctx *Context) error {
	cfg, err := ctx.LoadConfig()
	if err != nil {
		return err
	}
	st, err := ctx.LoadState()
	if err != nil {
		return err
	}
	a, err := catalogEntry(st, c.ID)
	if err != nil {
		return err
	}
	dest := a.Source
	if c.To != "" {
		dest = c.To
	}
	// A workspace copy's archive comes back as a clone of its own; only a
	// repo's is tracked again.
	repoKey := a.Repo
	if a.Workspace != "" {
		repoKey = ""
	}
	tracked := st.Repos[repoKey]
	if tracked != nil && c.To == "" {
		return fmt.Errorf("%s is tracked again, at %s; restore the archive elsewhere with --to", repoKey, tildify(tracked.LocalPath))
	}

	if a.Clone {
		if err := pathutil.Move(a.Path, dest); err != nil {
			return fmt.Errorf("restoring %s: %w", a.ID, err)
		}
		if err := os.Remove(a.Path + ".json"); err != nil && !errors.Is(err, fs.ErrNotExist) {
			ctx.Logger().Warn("restored archive's description left behind", "archive", a.ID, "error", err)
		}
		err = st.Update(cfg.LockTimeout, func(st *state.State) error {
			st.Archives = slices.DeleteFunc(st.Archives, func(e state.ArchiveEntry) bool { return e.ID == a.ID })
			return nil
		})
		if err != nil {
			return fmt.Errorf("restored %s to %s, but dropping it from the catalog: %w", a.ID, dest, err)
		}
	} else if err := gitops.RestoreArchive(ctx.GoCtx(), a.Path, dest, c.Base); err != nil {
		// A bundle stays in the catalog: restoring it copies it out.
		return err
	}
	logEvent(ctx, "restore", a.Repo, dest)
	fmt.Printf("Restored %s to %s\n", a.ID, tildify(dest))
	doc := map[string]any{"id": a.ID, "repo": a.Repo, "path": dest, "tracked": false}
	if tracked != nil {
		fmt.Printf("  Not tracked: %s is already tracked at %s\n", repoKey, tildify(tracked.LocalPath))
		return ctx.finish(doc, nil)
	}
	if repoKey == "" {
		return ctx.finish(doc, nil)
	}
	// Tracked by hand, its repo is never judged stale again: it is gone
	// upstream, and you asked for it back.
	if err := (&AdoptCmd{}).adoptSingle(ctx, cfg, st, dest); err != nil {
		return ctx.finish(doc, fmt.Errorf("restored %s to %s, but tracking it: %w; run git-mgr adopt %s", a.ID, dest, err, dest))
	}
	doc["tracked"] = true
	return ctx.finish(doc, nil)
}

// ArchivesRemoveCmd deletes archives.
type ArchivesRemoveCmd struct {
	IDs []string `arg:"" name:"id" help:"Archive IDs, from archives ls." predictor:"archive"`
	Yes bool     `help:"Skip confirmation."`
}

func (c *ArchivesRemoveCmd) Run(ctx *Context) error {
	cfg, err := ctx.LoadConfig()
	if err != nil {
		return err
	}
	st, err := ctx.LoadState()
	if err != nil {
		return err
	}
	var doomed []state.ArchiveEntry
	for _, id := range c.IDs {
		a, err := catalogEntry(st, id)
		if err != nil {
			return err
		}
		doomed = append(doomed, a)
	}
	if !c.Yes {
		if ctx.JSON || !term.IsTerminal(int(os.Stdin.Fd())) {
			return errors.New("deleting archives needs confirmation; pass --yes")
		}
		fmt.Printf("Will delete %d archives for good:\n", len(doomed))
		for _, a := range doomed {
			fmt.Printf("  %s  %s\n", a.ID, a.Work)
		}
		fmt.Print("Continue? (y/n): ")
		var answer string
		fmt.Scanln(&answer)
		if answer != "y" && answer != "Y" {
			fmt.Println("Cancelled.")
			return nil
		}
	}
	deleted := map[string]bool{}
	var failed []string
	for _, a := range doomed {
		if err := gitops.DeleteArchive(a.Path); err != nil {
			fmt.Fprintf(os.Stderr, "  %s: %v\n", a.ID, err)
			failed = append(failed, a.ID)
			continue
		}
		deleted[a.ID] = true
		fmt.Printf("  Deleted %s\n", a.ID)
	}
	err = st.Update(cfg.LockTimeout, func(st *state.State) error {
		st.Archives = slices.DeleteFunc(st.Archives, func(e state.ArchiveEntry) bool { return deleted[e.ID] })
		return nil
	})
	if err != nil {
		return err
	}
	removed := slices.Sorted(maps.Keys(deleted))
	if removed == nil {
		removed = []string{}
	}
	return ctx.finish(map[string][]string{"deleted": removed, "failed": failed},
		outcome(len(failed), len(doomed), "archives could not be deleted: %s", strings.Join(failed, ", ")))
}

// catalogEntry is the catalog's entry for archive id.
func catalogEntry(st *state.State, id string) (state.ArchiveEntry, error) {
	i := slices.IndexFunc(st.Archives, func(a state.ArchiveEntry) bool { return a.ID == id })
	if i < 0 {
		return state.ArchiveEntry{}, fmt.Errorf("no archive %q on record; git-mgr archives ls lists them", id)
	}
	return st.Archives[i], nil
}

func archiveID(path string) string {
	base := path[strings.LastIndex(path, "/")+1:]
	return strings.TrimSuffix(strings.TrimSuffix(base, ".bundle"), ".json")
}

// archiveEntry is the catalog entry of an archive made of a tracked repo
// before it was deleted or moved.
func archiveEntry(path, repo, source string) state.ArchiveEntry {
	e := state.ArchiveEntry{Path: path, Repo: repo, Source: source, ID: archiveID(path), Created: time.Now()}
	if meta, id, err := gitops.ReadArchive(path); err == nil {
		e.ID, e.Work, e.Branch, e.Created, e.Clone = id, meta.Work, meta.Branch, meta.Created, meta.Clone != ""
	}
	return e
}
