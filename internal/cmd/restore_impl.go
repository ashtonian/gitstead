package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ashtonian/git-mgr/internal/config"
	"github.com/ashtonian/git-mgr/internal/gitops"
	"github.com/ashtonian/git-mgr/internal/state"
)

// backupArtifacts is what a backup left behind for a working tree + id: a stash
// ref and/or patch files. The stash ref captures staged+unstaged tracked
// changes; the patch set captures the same plus untracked files.
type backupArtifacts struct {
	id        string
	ref       string // refs/git-mgr/backups/<id>
	hasRef    bool
	staged    string // patch file path, "" if absent
	unstaged  string
	untracked string
}

func (a backupArtifacts) any() bool {
	return a.hasRef || a.staged != "" || a.unstaged != "" || a.untracked != ""
}

// patchBaseFor returns the patch directory base for a working tree, mirroring
// how createPatchBackup lays them out (<patch_dir>/<parent>/<repo>).
func patchBaseFor(localPath, patchDir string) string {
	return filepath.Join(patchDir, filepath.Base(filepath.Dir(localPath)), filepath.Base(localPath))
}

// locateBackup finds the stash ref and patch files for a working tree's backup.
func locateBackup(ctx *Context, localPath, patchDir, id string) backupArtifacts {
	a := backupArtifacts{id: id, ref: "refs/git-mgr/backups/" + id}
	a.hasRef = gitops.GitExitCode(ctx.GoCtx(), localPath, "rev-parse", "--verify", "--quiet", a.ref) == 0
	pd := filepath.Join(patchBaseFor(localPath, patchDir), id)
	if p := filepath.Join(pd, "staged.patch"); fileExists(p) {
		a.staged = p
	}
	if p := filepath.Join(pd, "unstaged.patch"); fileExists(p) {
		a.unstaged = p
	}
	if p := filepath.Join(pd, "untracked.tar.gz"); fileExists(p) {
		a.untracked = p
	}
	return a
}

// latestBackupID returns the newest backup id (timestamps sort lexicographically)
// across both stash refs and patch dirs, or "" if there are none.
func latestBackupID(ctx *Context, localPath, patchDir string) string {
	ids := map[string]bool{}
	refs, _ := gitops.ListBackupRefs(ctx.GoCtx(), localPath)
	for _, line := range refs {
		if fields := strings.Fields(line); len(fields) > 0 {
			ids[filepath.Base(fields[0])] = true
		}
	}
	if entries, err := os.ReadDir(patchBaseFor(localPath, patchDir)); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				ids[e.Name()] = true
			}
		}
	}
	var all []string
	for id := range ids {
		all = append(all, id)
	}
	if len(all) == 0 {
		return ""
	}
	sort.Strings(all)
	return all[len(all)-1]
}

// printRestoreSteps prints the exact commands to recover a backup by hand --
// only the steps whose artifacts actually exist.
func printRestoreSteps(localPath, label string, a backupArtifacts) {
	fmt.Printf("To restore backup %s for %s (apply onto a clean tree):\n", a.id, label)
	fmt.Printf("  cd %s\n", localPath)
	if a.hasRef {
		fmt.Printf("  git stash apply %s\n", a.ref)
	}
	if a.staged != "" {
		fmt.Printf("  git apply --cached %s\n", a.staged)
	}
	if a.unstaged != "" {
		fmt.Printf("  git apply %s\n", a.unstaged)
	}
	if a.untracked != "" {
		fmt.Printf("  tar -xzf %s   # restores untracked files (may overwrite)\n", a.untracked)
	}
	fmt.Println()
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
	patchDir := cfg.ResolveForRepo(key).Backup.PatchDir

	id := c.ID
	if id == "" || id == "latest" {
		if id = latestBackupID(ctx, repo.LocalPath, patchDir); id == "" {
			return fmt.Errorf("no backups found for %s", key)
		}
	}
	a := locateBackup(ctx, repo.LocalPath, patchDir, id)
	if !a.any() {
		return fmt.Errorf("no backup %q found for %s", id, key)
	}

	switch {
	case c.Into != "":
		return restoreBackupInto(ctx, repo.LocalPath, config.ExpandHome(c.Into), key, a)
	case c.Apply:
		return applyBackupToTree(ctx, repo.LocalPath, key, c.Force, a)
	default:
		printRestoreSteps(repo.LocalPath, key, a)
		return nil
	}
}

// resolveOneRepo finds the single tracked repo matching ref (exact key or glob).
func resolveOneRepo(st *state.State, ref string) (string, *state.RepoState, error) {
	if r, ok := st.Repos[ref]; ok {
		return ref, r, nil
	}
	var matches []string
	for key := range st.Repos {
		if m, _ := filepath.Match(ref, key); m {
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

// restoreBackupInto clones srcPath into a scratch dir and applies the backup
// there, for safe inspection without touching the original working tree.
func restoreBackupInto(ctx *Context, srcPath, into, label string, a backupArtifacts) error {
	if entries, err := os.ReadDir(into); err == nil && len(entries) > 0 {
		return fmt.Errorf("--into target %s exists and is not empty", into)
	}
	if _, err := gitops.GitClone(ctx.GoCtx(), srcPath, into, ""); err != nil {
		return fmt.Errorf("cloning into %s: %w", into, err)
	}
	if a.hasRef {
		// The stash commit lives in the source object store; fetch it, then apply.
		if _, err := gitops.Git(ctx.GoCtx(), into, "fetch", srcPath, a.ref); err != nil {
			return fmt.Errorf("fetching backup ref: %w", err)
		}
		if _, err := gitops.Git(ctx.GoCtx(), into, "stash", "apply", "FETCH_HEAD"); err != nil {
			return fmt.Errorf("applying stash: %w", err)
		}
	} else if err := applyPatches(ctx, into, a); err != nil {
		return err
	}
	if a.untracked != "" {
		_ = untar(ctx, a.untracked, into)
	}
	fmt.Printf("Restored backup %s of %s into %s (for inspection).\n", a.id, label, into)
	return nil
}

// applyBackupToTree applies the backup onto a working tree, refusing a dirty tree
// without force (so it never silently clobbers current work).
func applyBackupToTree(ctx *Context, localPath, label string, force bool, a backupArtifacts) error {
	if !force {
		if rs, err := gitops.GetStatus(ctx.GoCtx(), localPath); err == nil && rs.StatusString() != "clean" {
			return fmt.Errorf("%s has uncommitted changes; commit/stash them or pass --force", label)
		}
	}
	if a.hasRef {
		if _, err := gitops.Git(ctx.GoCtx(), localPath, "stash", "apply", a.ref); err != nil {
			return fmt.Errorf("applying stash: %w", err)
		}
	} else if err := applyPatches(ctx, localPath, a); err != nil {
		return err
	}
	if a.untracked != "" {
		_ = untar(ctx, a.untracked, localPath)
	}
	fmt.Printf("Applied backup %s onto %s.\n", a.id, label)
	return nil
}

func applyPatches(ctx *Context, dir string, a backupArtifacts) error {
	if a.staged != "" {
		if _, err := gitops.Git(ctx.GoCtx(), dir, "apply", "--cached", a.staged); err != nil {
			return fmt.Errorf("applying staged patch: %w", err)
		}
	}
	if a.unstaged != "" {
		if _, err := gitops.Git(ctx.GoCtx(), dir, "apply", a.unstaged); err != nil {
			return fmt.Errorf("applying unstaged patch: %w", err)
		}
	}
	return nil
}

func untar(ctx *Context, tarPath, dir string) error {
	return exec.CommandContext(ctx.GoCtx(), "tar", "-xzf", tarPath, "-C", dir).Run()
}
