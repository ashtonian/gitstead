# Archives and safe removal

[Documentation](README.md) · [Quick start](../README.md#quick-start)

- [Deleting without losing work](#deleting-without-losing-work)
- [Archiving instead of deleting](#archiving-instead-of-deleting)
- [Retention](#retention)

## Deleting without losing work

Every command that deletes a clone or worktree — `remove --delete`,
`ws rm --delete`, `ws retire`, `ws prune` and the `remove_and_delete`
stale-cleanup policy — first inspects it for work that exists nowhere else:
changed or untracked files, stashes, commits no remote has (on a branch, a
detached HEAD, a tag or any other ref of its own) and an unfinished rebase or
merge. If there is any, it is written to one git bundle in `backup.archive_dir`
(default `~/.local/share/gitstead/archive/`) and verified before anything is
deleted. Each checked-out submodule is inspected the same way, and gets its own
bundle; a clone's Git LFS objects are kept beside its bundle (hardlinked).
Every archive is recorded in the catalog, `gitstead archives ls`.

What a bundle holds depends on what else keeps the history it shares with its
remotes:

| Deleting | The bundle holds | Restores from |
| --- | --- | --- |
| A tracked repo's clone | All of its history: branches, tags, remote-tracking refs, stashes, the working tree | The bundle alone. A clone deleted because its repo went upstream may be the last copy of it. |
| A workspace copy that is a clone | What the repo it was made from lacks, checked against that repo; all of its history when the repo lacks some of it | A clone of that repo, or of origin when it is gone |
| A workspace copy that is a worktree | The working tree, index and detached commits; its branches stay in the repo | The repo |

A tracked repo that workspace copies' archives lean on is bundled whole when it
is deleted, unique work or not, so those archives stay restorable. That counts
every such archive in `backup.archive_dir`, recorded in the catalog or not (a
run interrupted before recording one), and any archive whose description can't
be read, since it might be one.

Nothing is deleted while git is busy in the repo or one of its submodules --
a lock such as `index.lock` or a ref's `.lock` exists, `gc` is running, or a
rebase, merge, cherry-pick or revert is under way -- since a command running
there could be caught half-done. A lock a crashed git command left behind
counts too: delete it once no git command is running there. If gitstead can't
inspect the repo or write and verify the bundle, nothing is deleted and the
repo stays tracked. An archive is synced to disk -- bundle, description, LFS
objects, a whole clone copied across filesystems -- before its source is
deleted, and a run interrupted after deleting but before recording the
archive leaves it findable: `archives ls` lists archives in `archive_dir`
that aren't on record, marked so, and `doctor` warns about them. A repo that workspace copies are made from, or that has
linked worktrees, is never untracked or deleted until they are gone -- including
a copy another command is making at that moment: deleting, archiving or moving
a repo waits for it (see `lock_timeout`) and then keeps the repo. Stale
cleanup lists each repo it kept and why: `discover --apply` prints them, and
`sync` reports each as a failure (exit code 3) until a later run deletes it.

`gitstead archives restore <id> --to <dir>` rebuilds a clone from a bundle:
its branches, tags and remotes, HEAD, the stash list, the index, working tree
and untracked files, its LFS objects, and its submodules' archives in their
places. The bundle stays in the catalog. Each bundle has a `.json` file beside
it recording the source path, remotes, branch, what was saved and how to
restore it by hand:

```bash
git init restored && cd restored     # a bundle that leans on a repo: git clone <that repo> restored
git fetch ~/.local/share/gitstead/archive/<name>-<time>.bundle '+refs/*:refs/restored/*'
git for-each-ref refs/restored   # heads/*, tags/*, gitstead/archive/<id>/{head,worktree,stash-N}
```

The `worktree` ref is a stash-shaped commit of the index, working tree and
untracked files; `git stash apply --index <ref>` brings them back. Archives
are kept for `backup.archive_retention` (180 days by default) and then pruned
(see [Retention](#retention)); `gitstead archives rm <id>` deletes one for
good sooner.

## Archiving instead of deleting

`cleanup.policy: archive` and `remove --archive` keep the whole clone rather
than deleting it, as git-workspace's `archive` does. Past the grace period, a
repo gone upstream stops being tracked and its clone is moved, untouched, to
`backup.archive_dir/<repo>-<time>/`, with a `.json` file beside it recording
where it was, its remotes, its branch and what it held that exists nowhere
else. Moving loses nothing, so nothing has to be bundled first: a clone a
bundle can't hold -- a nested repository, a file too big to snapshot -- is
archived all the same. A clone with linked worktrees, or that workspace
copies are made from, stays where it is and stays tracked until they are
gone. Across filesystems the clone is copied in full, each file synced to
disk, and only then deleted; a failed copy leaves it where it was and
nothing in the archive. Once the clone is in the archive it is recorded
there, even if what follows fails -- syncing the directories a rename
changed, or deleting what a copy left behind -- and that failure is logged as
a warning with the archive's path. `adopt --scan` skips the archive directory.

```bash
gitstead archives ls                    # ID, where it was, what it held
gitstead archives restore <id>          # move it back and track it again (a bundle: rebuild a clone from it)
gitstead archives restore <id> --to ~/scratch/app   # the repo is tracked again meanwhile
gitstead archives rm <id>               # delete it for good
```

A restored clone is tracked by hand, so discovery never judges it stale
again. If its repo is tracked again meanwhile -- found upstream once more and
cloned afresh -- `restore` refuses its old place and `--to` puts it elsewhere,
untracked.

## Retention

Every archive is deleted once it is older than `backup.archive_retention`
(default `4320h`, 180 days), so the archive directory can't grow without
bound. `sync` prunes after its backups, every run; `gitstead archives prune`
does it on demand, and `--dry-run` lists what it would delete, and what past
its retention it keeps and why, without deleting anything. `0` keeps archives
forever, as before there was a retention.

Pruning covers every archive in `backup.archive_dir` -- on record or not --
and removes the whole archive: its bundle or archived clone, its description,
its LFS objects and its submodules' archives. Deleted archives leave the
catalog; a retired copy's tombstone in `ws show` names its archive as
`(deleted)`. Each deletion is written to the operation log as
`archive-prune`. The bundles `remote_sync.unpushed: bundle` keeps in
`archive_dir/unpushed/` are not archives and are left alone.

An archive of a repo's whole history -- a complete bundle, or an archived
clone -- is kept past its retention while an archive that leans on it stays:
a workspace copy's thin bundle of the same repo, or one based on that repo's
clone or a submodule inside it, since restoring the thin one may need it.
Thin archives are pruned first, so a whole-history archive goes in the same
run as the last archive that leaned on it. While any description in the
archive directory can't be read, every whole-history archive is kept, since
the unreadable one might lean on it; `archives prune --dry-run` names it.
An archive that records no creation time, or whose description can't be
read, is kept and listed.

```bash
gitstead archives prune --dry-run       # what retention would delete now, and what it keeps and why
gitstead archives prune                 # delete it
gitstead doctor                         # how many archives, their size, the oldest, the retention
```
