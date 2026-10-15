# Transparent backups

[Documentation](README.md) · [Quick start](../README.md#quick-start)

- [Offsite: surviving a lost disk](#offsite-surviving-a-lost-disk)
- [Mirrors: surviving the host](#mirrors-surviving-the-host)

Each backup is a snapshot of one working copy: the index, the tracked files in
the working tree, and the untracked files (ignored files never). It is built in
temporary index files, so it never touches your working tree, index, branches or
stash, and never takes `index.lock`: a backup taken while an agent's
`git commit` is running neither fails nor gets in its way.

`backup.strategy` picks where snapshots are kept:

- **Snapshot refs** (`stash-ref`) — a commit in `git stash`'s own layout at
  `refs/gitten/backups/<copy-id>/<backup-id>`, invisible to `git branch`,
  `git stash list`, `git log` and IDEs. Restores with
  `git stash apply --index <ref>`.
- **Patch backups** (`patch`) — the same snapshot as `staged.patch`,
  `unstaged.patch` and `untracked.tar.gz` under
  `~/.local/share/gitten/patches/<copy-id>/<backup-id>/`. They outlive the repo:
  a fresh clone at the same path finds the old copy's patch backups.
- **`both`** (default).

Guarantees:

- **Exact restore.** Staged and unstaged changes stay separate; deletions,
  binary files, files without a final newline, symlinks, executable bits and
  non-ASCII names come back as they were.
- **Per copy.** Every clone and every worktree has its own copy ID (kept in its
  git directory), so agents in worktrees of one repo never overwrite or prune
  each other's backups.
- **No duplicates.** A state identical to the latest backup isn't saved again;
  the run reports `unchanged since <id>`.
- **Submodules too.** Each checked-out submodule with uncommitted work is
  backed up as a working copy of its own, and its parent's backup records
  which of the submodule's backups goes with it: `restore --apply` restores
  them in place, `restore --into` as clones of their own in their places --
  inside a clone of each submodule they are in that has no backup of its own
  (its status ignoring them, say), at the commit its parent records.
- **Nothing silently skipped.** Untracked files over `max_untracked_file_mb`
  (50) or past `max_untracked_total_mb` (500), unreadable files and nested git
  repositories are left out *and reported* with the reason, in the command's
  output and in the backup's metadata.
- **Failures are loud.** A backup that fails, in whole or in part, makes
  `backup`, `sync` and `ws backup` exit non-zero, is stored as
  `last_backup_error`, and shows as `failed` in `status`. A patch backup is
  written to a temporary folder and published only when complete, and pruning
  runs only after a backup succeeds.
- **Retention by age.** Per copy: every backup from the last 24 hours
  (`keep_all`), then the newest of each day for 14 days (`keep_daily`), capped at
  100 (`max_per_copy`). The newest backup is always kept.

| Key | Default | Meaning |
| --- | --- | --- |
| `backup.enabled` | `true` | Back up uncommitted work during `sync` (and `backup`, `ws backup`). |
| `backup.strategy` | `both` | `stash-ref`, `patch` or `both` (above). |
| `backup.include_untracked` | `true` | Snapshot untracked files too, within the limits below. |
| `backup.max_untracked_file_mb` | `50` | Untracked files bigger than this are left out, and reported. |
| `backup.max_untracked_total_mb` | `500` | Untracked files past this total are left out, and reported. |
| `backup.keep_all` | `24h` | Keep every backup younger than this. |
| `backup.keep_daily` | `336h` | Then the newest backup of each day younger than this. |
| `backup.max_per_copy` | `100` | And at most this many per working copy. |
| `backup.patch_dir` | `~/.local/share/gitten/patches` | Where patch backups go. |
| `backup.archive_dir` | `~/.local/share/gitten/archive` | Where what is saved before deleting goes (see [Deleting without losing work](ARCHIVES.md#deleting-without-losing-work)). |

Each can be set per repo in `overrides[].backup`, except `archive_dir`.

```bash
gitten backup --list --repos "*/my-repo"
gitten restore github.com/me/my-repo                    # prints the restore commands
gitten restore github.com/me/my-repo --into /tmp/look   # clone + restore into a scratch dir
gitten restore github.com/me/my-repo --apply            # onto the clean working tree
```

## Offsite: surviving a lost disk

Local backups live in the repo; a lost or wiped disk takes them along, with
every commit you never pushed. With `remote_sync.enabled: true`, each sync
also takes two things offsite for every working copy, repos and workspace
copies alike:

- **Uncommitted work** (`wip: ref`, the default): each new backup, untracked
  files and the staged/unstaged split included, is pushed to a hidden ref
  `<namespace>/wip/<backup id>`. Unchanged work isn't pushed again; refs older
  than `keep` (30 days) are deleted, the current one never.
- **Commits no remote has** (`unpushed: push`, the default): each branch with
  such commits is saved as `<namespace>/heads/<branch>`; your branches are
  never pushed. Once a remote has the commits (you pushed for real), the
  saved copy is deleted.

Each checked-out submodule goes offsite too, as a working copy of its own:
its uncommitted work (from the backup of it that went with its parent's) and
its commits no remote has -- a commit on its detached HEAD is saved as
`<namespace>/heads/HEAD` -- go to the submodule's own remote (the remote
`remote` names, in the submodule, or the template filled in from its origin),
under its own copy's namespace. `remote-sync --list` lists them under the
repo, by submodule; restore from inside a clone of the submodule's remote.

`<namespace>` is `refs/gitten/<machine>/<copy>`: every machine and every copy
-- each agent's clone or worktree -- owns its refs, so copies on one branch
never overwrite each other, and gitten force-pushes nowhere else. Refs
outside `refs/heads` and `refs/tags` don't show in the host's branch lists and
don't trigger CI, but anyone who can read the remote can fetch them; point
`remote` at a private backup remote to keep WIP off a shared one.

| Key | Default | Meaning |
| --- | --- | --- |
| `remote_sync.enabled` | `false` | Sync takes copies offsite on its own. `gitten remote-sync` does it on demand either way. |
| `remote_sync.remote` | `origin` | A remote name, or a URL template (`git@backup:{{.Host}}/{{.FullName}}.git`) for a separate backup remote. A submodule uses its own remote of that name, or the template filled in from its origin. |
| `remote_sync.wip` | `ref` | `ref` (hidden refs), `branch` (a visible branch per copy, `gitten/wip/<machine>/<copy>`: tracked changes only, on top of HEAD) or `off`. |
| `remote_sync.unpushed` | `push` | `push` (hidden refs), `bundle` (a local bundle in `backup.archive_dir/unpushed/`, which protects against a deleted branch but not a lost disk) or `off`. |
| `remote_sync.namespace` | `refs/gitten/{{.Machine}}/{{.Copy}}` | Must stay under `refs/`, outside branches and tags, and include `{{.Copy}}`. |
| `remote_sync.keep` | `720h` | How long a pushed WIP ref stays. |

Any of these can be set per account (`accounts[].remote_sync`) and per repo
(`overrides[].remote_sync`); a workspace copy follows the repo it is a copy of,
then its workspace's `remote_sync`. The machine name is chosen once (host name plus a random suffix) and kept
in the state file. The push runs after the fetch, on `sync.timeout_per_repo`.

```bash
gitten remote-sync --dry-run          # what would go offsite, from the latest backups
gitten remote-sync --repos "*/my-repo"
gitten remote-sync --list             # what every machine left offsite, with restore commands
gitten ws rm app --delete --offsite   # take each copy offsite before deleting it
```

Restoring on another machine needs only the remote:

```bash
git clone <remote> restored && cd restored
git fetch origin refs/gitten/<machine>/<copy>/heads/main:refs/heads/recovered
git checkout recovered
git fetch origin refs/gitten/<machine>/<copy>/wip/<id>
git stash apply --index FETCH_HEAD     # the uncommitted work, staged and unstaged as it was
```

## Mirrors: surviving the host

Offsite refs protect your work; a mirror protects the repo itself. With
`mirror` on, gitten keeps a full mirror of each repo's upstream -- every
branch, tag and other ref the host has, pull request refs included -- as a
bare repository in `mirror.dir`, so a repo or account the host loses, or a
history rewritten there, is still on your disk, NAS or backup volume.

```yaml
mirror:
  enabled: true
  dir: /Volumes/backup/git-mirrors     # <dir>/<host>/<owner>/<name>.git
  interval: 24h                        # how often sync updates each
overrides:
  - repos: ["github.com/acme/huge-assets"]
    mirror: {enabled: false}           # or enable for some repos only
```

| Key | Default | Meaning |
| --- | --- | --- |
| `mirror.enabled` | `false` | Mirror every tracked repo; `accounts[].mirror` and `overrides[].mirror` turn it on or off for some. |
| `mirror.dir` | `~/.local/share/gitten/mirrors` | Holds `<host>/<owner>/<name>.git`. |
| `mirror.interval` | `24h` | How long `sync` leaves a mirror before updating it again; a failing one is retried every sync. |
| `mirror.lfs` | `false` | Also mirror Git LFS content (`git lfs fetch --all`); per account or repo too. |
| `mirror.keep_rewritten` | `2160h` (90 days) | How long a mirror keeps the old tip of a ref its upstream rewrote (a force-pushed branch), under `refs/gitten-rewritten/<time>/`; `0s` keeps none. |

A mirror is made with `git clone --mirror`, staged like any clone, from the
repo's upstream URL with its SSH key -- not from your clone, which may be
partial, shallow or behind. Updates fetch every ref and never prune, whatever
`fetch.prune` says: a branch deleted upstream stays in the mirror. A branch
moved upstream follows the remote; when the move rewrote its history -- a
force-push -- the old tip is kept as `refs/gitten-rewritten/<time>/heads/<branch>`
for `mirror.keep_rewritten`, so the rewritten commits stay too. `sync` updates the mirrors that are due after its
fetches (`--force` all of them, `--no-fetch` none); `gitten mirror` does it
now (`--repos`, `--tag`, `--set`, `--force`). A stale repo's mirror is left as
it was: its upstream is gone, which is what the mirror is for. A renamed repo's
mirror moves with it; gitten never deletes a mirror. Failing mirrors show in
`gitten status`, `gitten top` and `doctor`.

To use one: `git clone /Volumes/backup/git-mirrors/github.com/acme/app.git`
for a working copy, or `git push --mirror <new remote>` from it to re-host.
