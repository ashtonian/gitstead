package cmd

import (
	"fmt"
	"sort"
	"strings"

	"github.com/ashtonian/git-mgr/internal/config"
	"github.com/ashtonian/git-mgr/internal/gitops"
	"github.com/ashtonian/git-mgr/internal/repourl"
	"github.com/ashtonian/git-mgr/internal/shellutil"
	"github.com/ashtonian/git-mgr/internal/state"
)

// printRestoreSteps prints the commands that restore backup b by hand onto the
// copy at localPath.
func printRestoreSteps(localPath, label string, b gitops.BackupInfo) {
	fmt.Printf("To restore backup %s of %s (onto a clean tree):\n", b.ID, label)
	fmt.Printf("  cd %s\n", shellutil.Quote(localPath))
	for _, cmd := range b.RestoreCommands() {
		fmt.Printf("  %s\n", cmd)
	}
	if b.Meta != nil && len(b.Meta.Skipped) > 0 {
		fmt.Printf("  (not in this backup: %s)\n", skippedSummary(b.Meta.Skipped))
	}
	fmt.Println()
}

// restoreBackup restores b for the copy at localPath into a scratch directory,
// onto the copy itself, or prints how to, and says which.
func restoreBackup(ctx *Context, localPath, label string, b gitops.BackupInfo, into string, apply, force bool) (restoreJSON, error) {
	doc := restoreJSON{Target: label, Path: localPath, Backup: backupInfoJSON(b)}
	err := func() error {
		switch {
		case into != "":
			into = config.ExpandHome(into)
			if err := gitops.RestoreBackupInto(ctx.GoCtx(), localPath, b, into); err != nil {
				return fmt.Errorf("restoring backup %s of %s into %s: %w", b.ID, label, into, err)
			}
			doc.Into = into
			fmt.Printf("Restored backup %s of %s into %s.\n", b.ID, label, into)
		case apply:
			if !force {
				rs, err := gitops.GetStatus(ctx.GoCtx(), localPath)
				if err != nil {
					return fmt.Errorf("reading status of %s: %w", label, err)
				}
				if rs.StatusString() != "clean" {
					return fmt.Errorf("%s has uncommitted changes; commit or stash them, use --into, or pass --force", label)
				}
			}
			if err := gitops.ApplyBackup(ctx.GoCtx(), localPath, b); err != nil {
				return fmt.Errorf("applying backup %s onto %s: %w", b.ID, label, err)
			}
			doc.Applied = true
			fmt.Printf("Applied backup %s onto %s.\n", b.ID, label)
		default:
			printRestoreSteps(localPath, label, b)
		}
		return nil
	}()
	doc.Error = errString(err)
	return doc, err
}

func (c *RestoreCmd) Run(ctx *Context) error {
	cfg, err := ctx.LoadConfig()
	if err != nil {
		return err
	}
	st, err := ctx.LoadState()
	if err != nil {
		return err
	}

	key, repo, err := resolveOneRepo(st, c.Repo)
	if err != nil {
		return err
	}
	b, err := gitops.FindBackup(ctx.GoCtx(), repo.LocalPath, cfg.ResolveForRepo(key).Backup.PatchDir, c.ID)
	if err != nil {
		return err
	}
	doc, err := restoreBackup(ctx, repo.LocalPath, key, b, c.Into, c.Apply, c.Force)
	return ctx.finish(doc, err)
}

// resolveOneRepo finds the single tracked repo matching ref (exact key or glob).
func resolveOneRepo(st *state.State, ref string) (string, *state.RepoState, error) {
	if r, ok := st.Repos[ref]; ok {
		return ref, r, nil
	}
	var matches []string
	for key := range st.Repos {
		if repourl.MatchKey(ref, key) {
			matches = append(matches, key)
		}
	}
	switch len(matches) {
	case 0:
		return "", nil, fmt.Errorf("no tracked repo matches %q", ref)
	case 1:
		return matches[0], st.Repos[matches[0]], nil
	default:
		sort.Strings(matches)
		return "", nil, fmt.Errorf("%q matches %d repos; be more specific: %s", ref, len(matches), strings.Join(matches, ", "))
	}
}
