package cmd

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/ashtonian/git-mgr/internal/gitops"
	"github.com/ashtonian/git-mgr/internal/state"
)

// ArchivesCmd groups the archive catalog's subcommands.
type ArchivesCmd struct {
	List ArchivesListCmd `cmd:"" name:"ls" help:"List the archives of unique work saved before copies and repos were deleted."`
	Show ArchivesShowCmd `cmd:"" help:"Show an archive: what it saved, and how to restore it."`
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
	if len(rows) == 0 {
		fmt.Println("No archives on record.")
		return nil
	}
	slices.SortFunc(rows, func(a, b state.ArchiveEntry) int { return b.Created.Compare(a.Created) })
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
	ID string `arg:"" help:"Archive ID, from archives ls."`
}

func (c *ArchivesShowCmd) Run(ctx *Context) error {
	st, err := ctx.LoadState()
	if err != nil {
		return err
	}
	i := slices.IndexFunc(st.Archives, func(a state.ArchiveEntry) bool { return a.ID == c.ID })
	if i < 0 {
		return fmt.Errorf("no archive %q on record; git-mgr archives ls lists them", c.ID)
	}
	a := st.Archives[i]
	meta, _, err := gitops.ReadArchive(a.Path)
	if err != nil {
		return fmt.Errorf("reading %s: %w (was the archive moved or deleted?)", a.Path, err)
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
	if meta.Bundle != "" {
		fmt.Printf("  bundle:    %s\n", tildify(meta.Bundle))
		fmt.Printf("\nRestore:\n  %s\n", strings.ReplaceAll(meta.Restore, " && ", " &&\n    "))
	}
	for _, sub := range slices.Sorted(maps.Keys(meta.Submodules)) {
		fmt.Printf("  submodule %s: git-mgr archives show %s\n", sub, archiveID(meta.Submodules[sub]))
	}
	if meta.LFS != "" {
		fmt.Printf("  LFS objects: %s -- copy them into the restored repo's .git/lfs/objects\n", tildify(meta.LFS))
	}
	return nil
}

func archiveID(path string) string {
	base := path[strings.LastIndex(path, "/")+1:]
	return strings.TrimSuffix(strings.TrimSuffix(base, ".bundle"), ".json")
}

// archiveEntry is the catalog entry of an archive made of a tracked repo
// before it was deleted.
func archiveEntry(path, repo, source string) state.ArchiveEntry {
	e := state.ArchiveEntry{Path: path, Repo: repo, Source: source, ID: archiveID(path), Created: time.Now()}
	if meta, id, err := gitops.ReadArchive(path); err == nil {
		e.ID, e.Work, e.Branch, e.Created = id, meta.Work, meta.Branch, meta.Created
	}
	return e
}
