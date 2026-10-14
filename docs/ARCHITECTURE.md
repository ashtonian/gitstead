# How gitten works

This is the map for someone changing gitten: what the parts are, how data
moves through them, what each lock protects, and what happens when something
fails. [README.md](../README.md) covers using it; [SPEC.md](../SPEC.md) is the
command-by-command reference and the record of each design detail.

- [Problem and constraints](#problem-and-constraints)
- [Components](#components)
- [Where things live](#where-things-live)
- [A sync run](#a-sync-run)
- [Config changes: plan and apply](#config-changes-plan-and-apply)
- [Backups](#backups)
- [Deleting without losing work](#deleting-without-losing-work)
- [Workspaces](#workspaces)
- [Offsite copies](#offsite-copies)
- [Concurrency and locks](#concurrency-and-locks)
- [Durability](#durability)
- [Failure modes](#failure-modes)
- [Decisions and trade-offs](#decisions-and-trade-offs)

## Problem and constraints

People who work across many repositories -- several forges, several accounts
on each, several machines, and now several coding agents at once -- end up
with clones scattered across the disk, the wrong identity in some of them,
uncommitted work nothing backs up, and agents' copies nobody cleans up
safely. gitten keeps all of it in one layout and keeps it safe, under these
constraints:

- **Never lose work.** Backups never touch the working tree, the index,
  `index.lock`, branches or the stash. Nothing is deleted until what it holds
  that exists nowhere else has been archived, verified and synced to disk.
- **Run unattended.** The same binary runs from launchd, systemd or cron:
  no prompts (git, ssh and credential helpers are all told not to ask), a
  deadline on every external call, one failing repo or provider never stops
  the rest, and runs that overlap with each other or with you are safe.
- **Use git itself.** gitten drives the `git` CLI, so linked worktrees,
  partial clones, bundles, credential helpers, `includeIf` and your own git
  and SSH config work as they do for you.
- **No daemon, no database.** One binary and two files you can read: the
  config (what you want) and the state (what gitten did and found).
- **Declarative config.** Changing config changes nothing until a run brings
  disk in line with it; `plan` shows what that would do.

## Components

```
                                main.go           kong command line, signals, exit code
                                   |
                              internal/cmd        every command: wiring, output, --json
                                   |
     +------------+--------------+-+------------+--------------+------------+
     |            |              |              |              |            |
   setup      workspace        launch         view           jump      scheduler
  (wizard)   (agents' copies)  (tmux, iTerm,  (gitten top)  (cd/open)  (launchd,
     |            |             Muxy, plugin)    |                       systemd,
     |            |   agent, activity            |                       cron)
     |            |   (runs, leases, terminals)  |
     +-----+------+                               |
           |                                      |
       reconcile ------------- discovery          |      inventory: never imports
       (plan & apply)          (providers' APIs,  |      workspace (one-way boundary)
           |                    stale cleanup)    |
     +-----+-------+------------+-----------------+
     |             |            |
   gitops         hook        config ----- state       oplog, notify
   (git: clone,   (post_clone, (layered      (state.yaml,
    fetch, backup, post_create) YAML, accounts, locks, group
    archive, offsite)           overrides)    commit)
     |
   pathutil, procutil, shellutil, repourl, pathtemplate, ctxutil   (leaf helpers)
```

The **inventory** subsystem -- `config`, `discovery`, `reconcile`, `gitops`,
`state` -- tracks repositories: discover, clone, fetch, back up, archive. The
**workspaces** subsystem -- `workspace`, `agent`, `launch`, `activity`, `view`
-- puts agents on repos. Workspaces depend on the inventory, never the
reverse; `cmd` is the only package that sees both, and only to wire a command
that spans them (`apply`, or `remove` refusing a repo agents' copies are made
from). The README's [code map](../README.md#code-map) lists every package.

## Where things live

```
$XDG_CONFIG_HOME/gitten/              (~/.config/gitten)
  config.yaml                          what you want; setup edits it in place, keeping comments
  accounts/<name>-<digest>.gitconfig   accounts gitconfig: each account's identity, key, signing
  accounts.gitconfig                   includeIf sections, included from your global git config

$XDG_DATA_HOME/gitten/                (~/.local/share/gitten)
  state.yaml                           what gitten applied and found; machine-local
  state.yaml.lock                      held for each state write
  state.yaml.sync.lock                 held for a whole sync run: one at a time
  events.jsonl (+ .lock)               operation log: one JSON line per change (report, log)
  runs/<workspace>/<agent>.lease       held while an agent runs
  hooks/                               output of post_clone and post_create hooks
  patches/<copy-id>/<backup-id>/       patch backups: staged.patch, unstaged.patch,
                                       untracked.tar.gz, metadata.json
  archive/                             what was saved before deleting: bundles and their
                                       .json descriptions, whole archived clones, and
                                       unpushed/<copy-id>.bundle (remote_sync.unpushed: bundle)
  mirrors/<host>/<owner>/<name>.git    full mirrors of upstreams (mirror on)
  gitten.log                          the scheduled sync's output (launchd, cron)

$XDG_CACHE_HOME/gitten/               (~/.cache/gitten)
  workspaces/<workspace>/              agents' shared caches (agent environment)
  locks/manifest-<digest>.lock         held while writing the shared manifest

in each working copy (repo clone or agent's copy)
  .git/gitten-copy-id                 this working copy's ID, which namespaces its backups
  .git/gitten.lock                    the repo lock (see below)
  refs/gitten/backups/<copy-id>/<backup-id>            snapshot commits
  refs/gitten/backup-submodules/<copy-id>/<backup-id>  which submodule backups go with it

on a remote, with remote_sync
  refs/gitten/<machine>/<copy-id>/wip/<backup-id>      uncommitted work
  refs/gitten/<machine>/<copy-id>/heads/<branch>       commits no remote has
  refs/heads/gitten/wip/<machine>/<copy-id>            wip: branch only
```

Config is portable (the same file on every machine); state is not, since it
records this machine's paths. A shared manifest (`manifest:`) carries the
repos you add by hand from machine to machine.

## A sync run

`gitten sync` is what the background schedule runs. Everything it does is
safe to repeat, and it holds the state lock only while recording.

```
gitten sync
  |
  |-- take state.yaml.sync.lock; another sync holds it -> exit 0, nothing done
  |
  |-- discover            each discovery rule against its provider's API
  |                       (a failing rule is reported; the others still count)
  |
  |-- reconcile           desired repos (config + discovery) vs state vs disk
  |                       -> apply what reconcile.auto allows: clone, adopt, rename,
  |                          origin URL, managed git config, tags; moves wait for apply
  |
  |-- stale cleanup       repos no complete rule found any more: mark stale, then
  |                       past the grace period apply cleanup.policy
  |
  |-- each working copy, up to `concurrency` at once
  |     repos first, then agents' copies (refreshed from the repo clone just fetched)
  |
  |       backup --> fetch (or refresh) --> fast-forward --> offsite push --> status
  |       (own        (retries,             (sync.update,    (remote_sync)
  |        deadline)   timeout_per_repo)     clean only)
  |
  |-- record              every outcome in one state write
  |-- mirrors             full mirrors due for an update
  `-- notify, oplog       a desktop notification on failures; one log line
```

A copy whose fetch finds git's own lock taken is skipped, not failed, and
retried next run. The exit code is 0, 3 when some operations failed, and 75
when nothing could run because another command held what it needed.

## Config changes: plan and apply

Config is declarative. `reconcile` computes what it takes to bring disk and
state in line with it; `plan` prints that, `apply` runs it, and `sync` runs
the part that moves and deletes nothing.

```
config.yaml --+
discovery ----+--> Compute --> Plan { Actions, Drift, Blocked }
state.yaml ---+       ^
disk ---------+       observe each path (outside any lock): exists? repo? which?

Plan.Apply
  targets at once, up to `concurrency` doing disk work; one target's actions in order,
  stopping at its first failure; workspace actions after, one at a time
    each action:
      Run     disk work (a clone, a rename, a copy), taking the repo lock when needed
      Record  its change to state, under the state lock; changes recorded while
              one write runs share the next (state.GroupCommit)
      After   follow-ups that need the record first (reconnect copies, run hooks)
```

An action is **disruptive** if it moves or deletes a directory a shell or
editor may have open; only `apply` runs those, and only after showing them.
`Blocked` lists what can't be planned (a path held by something else, a
partial clone a copy can't be made from), and `Drift` what differs on disk
from what gitten set (reported, or overwritten with `reconcile.drift`).

## Backups

```
working copy                        temporary index (never the repo's own)
  HEAD ------------------------+
  index ----> read-tree -------+--> commit "index"
  tracked files --> add -u ----+--> commit "worktree" (the snapshot)   parents: HEAD, index,
  untracked (within limits) ---+--> commit "untracked"                 untracked: git stash's
                                                                        layout
snapshot --+--> refs/gitten/backups/<copy-id>/<id>        (backup.strategy stash-ref)
           +--> patches/<copy-id>/<id>/ patches + tarball   (patch; both by default)
```

- The same state always gives the same commit, so an unchanged copy isn't
  saved again.
- Retention prunes only the kinds of backup the current run added to, after
  it succeeded, so a failed run never pushes out the last good backup.
- Each checked-out submodule is backed up as a working copy of its own, and
  its parent's backup records which of its backups goes with it; a
  submodule's backups live as long as a retained parent backup names them.
- `restore` applies a backup with `git stash apply --index` at the commit it
  was taken on (`--apply`), or into a fresh clone (`--into`), never
  overwriting an untracked file.

## Deleting without losing work

Every path that deletes a working copy -- `remove --delete`, stale cleanup,
`ws retire`, `ws prune`, removals in `apply` -- goes through the same steps:

```
take the repo lock, exclusive  --> wait up to lock_timeout, else keep it (exit 75)
re-read state                  --> a copy made from it meanwhile, or it moved? keep it
check git is idle              --> index.lock, a ref lock, gc, rebase/merge under way? keep it
inspect unique work            --> uncommitted, untracked, stash, refs no remote has,
                                   submodules', LFS objects
  nothing unique               --> delete
  something unique             --> bundle it, then verify the bundle; write its .json
                                   description; sync it all to disk; then delete
record the archive             --> catalog in state; `archives ls` also finds any on disk
                                   that a run interrupted before recording
```

A **complete** bundle holds the repo's whole history and restores from
itself alone. A workspace copy's bundle is **thin**: only what the repo it
was made from lacks, checked against that repo, falling back to complete
when the repo lacks something. A repo that thin bundles lean on -- on record
or found on disk -- is bundled whole when it is deleted, so they stay
restorable. `remove --archive` and `cleanup.policy: archive` move the whole
clone into the archive instead.

## Workspaces

```
workspace "app"                     declared in config, or made from flags
 |
 |-- replica agent-1  (its own folder; its own ports, env and lease)
 |     |-- copy of repo A  worktree | clone | reference-clone, on agents/app/agent-1
 |     `-- copy of repo B
 |-- replica agent-2
 |     `-- ...
 `-- shared isolation: every agent in one folder

copies are made from the repo's tracked clone -- nothing is downloaded twice --
holding the repo lock shared until the copy is recorded, so the repo can't be
deleted or moved meanwhile
```

An agent's **lease** (`runs/<workspace>/<agent>.lease`, an flock) is held
while it runs; nothing removes its copies while it is held. **Retiring** an
agent marks it retiring, archives each copy's unique work, removes the copies
and records tombstones; an interrupted retire is left for `ws retire` to
finish. `fleet` opens several workspaces' agents side by side through a
launcher: tmux, iTerm, Muxy, or any `gitten-launch-<name>` on PATH, which
gets the plan as JSON on stdin.

## Offsite copies

With `remote_sync`, each working copy -- and each of its checked-out
submodules, as a copy of its own -- pushes to hidden refs it alone owns
(`refs/gitten/<machine>/<copy-id>/...`): its latest backup as a WIP ref, and
each branch's commits no remote has. One `git push` per copy carries every
change; deletions are checked against the remote first; the record of what
was pushed (in state) means the next run sends only what changed and deletes
only refs it made. No branch is ever pushed, so CI never runs and no one's
branch moves. Restoring needs only the remote (`remote-sync --list` prints
how).

## Concurrency and locks

Several gitten processes run at once routinely: a scheduled sync, commands
you type, agents' hooks. Every lock is an `flock`, released by the kernel if
its holder dies.

| Lock | File | Held | Protects |
| --- | --- | --- | --- |
| State write | `state.yaml.lock` | for one load-modify-save, up to `lock_timeout` to get it | `state.yaml`: every change re-reads it under the lock, so runs never overwrite each other |
| Sync run | `state.yaml.sync.lock` | a whole `sync` | one sync at a time; another just skips |
| Repo | `.git/gitten.lock` | shared while a copy is made from the repo, until recorded; exclusive while it is deleted, archived or moved | a repo is never deleted or moved from under a copy being made of it |
| Agent lease | `runs/<ws>/<agent>.lease` | while the agent runs | its copies aren't removed while it works |
| Manifest | `locks/manifest-<digest>.lock` | one manifest write | the shared manifest, written by several machines' runs |
| Operation log | `events.jsonl.lock` | one append or trim | no line lost to a concurrent trim |
| git's own | `index.lock`, ref locks | never taken by gitten | checked before deleting (CheckIdle); backups don't need them |

Ordering: the repo lock is always taken before the state lock, never while
holding it, so the two can't deadlock. Slow work -- clones, fetches,
backups, archives, prompts -- happens outside the state lock; only the
recording happens inside, and `state.GroupCommit` folds records that arrive
during one write into the next.

## Durability

- Files that matter -- state, config edits, archive descriptions, the
  manifest -- are replaced atomically: written to a temporary file beside
  them, synced, renamed over, and the directory synced
  (`pathutil.WriteFileAtomic`).
- An archive -- bundle, description, LFS objects, or a whole clone copied
  across filesystems -- is synced to disk (`pathutil.SyncTree`) before its
  source is deleted, and directories made for it are synced into their
  parents (`pathutil.MkdirAll`).
- A move that renamed but couldn't sync afterwards still counts as moved
  (`pathutil.MovedError`): the tree is where it was moved to, and that is what
  gets recorded.
- Clones, mirrors and restored archives are built beside their destination
  and renamed into place, so no process sees a half-made repository; they are
  left to git's own syncing, since each can be made again from its source.

## Failure modes

| What fails | What gitten does | What you do |
| --- | --- | --- |
| A provider is down or a token is bad | That rule fails; the others run; stale detection is skipped for everything, so nothing is marked stale on partial data | `gitten doctor`; fix the token (`token_command`, `gh auth login`) |
| The network is flaky | API calls retry on rate limits and 5xx with back-off; fetches retry `sync.retries` times; every call has a deadline | Nothing; the next run catches up |
| git holds a lock in a repo (an agent mid-commit) | The fetch is skipped and retried next run; the backup still runs (temporary index) | Nothing |
| Another command holds a repo or the state | Waits up to `lock_timeout`, then keeps the target and exits 75 | Run it again later |
| A run is killed mid-delete | The archive is already on disk and synced; `archives ls` and `doctor` find one that wasn't recorded | `gitten archives ls` |
| A run is killed mid-retire | The agent stays marked retiring; `ensure` and `apply` leave it alone | `gitten ws retire <ws> <agent>` |
| A clone fails partway | Nothing appears at the destination; the staging directory is swept next time | Nothing; the next run retries |
| The disk is full, or a sync fails | Nothing is deleted before its archive is synced; a failed backup keeps the previous ones | Free space; rerun |
| The schedule has no login session | Tokens must come from a store (`token_command`, gh's stored login), not your shell's environment; setup and `install-agent` warn | `gitten doctor` in the schedule's environment |

## Decisions and trade-offs

The ones that shape the code most:

- **The git CLI, not go-git.** go-git trails git on linked worktrees,
  partial clones, bundles, credential helpers and `includeIf`, and the CLI
  honours your own git and SSH config. The cost is process spawning and
  parsing git's output; every call goes through `gitops` with no prompts and
  a deadline.
- **A YAML state file under an flock, not a database.** Readable, diffable,
  and fast enough at thousands of repos when the lock is held only to record.
  One locked update -- re-read, change, write -- costs about 6 ms at 100
  repos, 60 ms at 1,000 and 390 ms at 5,000, all of it YAML
  (`BenchmarkUpdate` in `internal/state`). What made that matter was the
  number of writes, not the format: the group commit saves the records of
  every change that finishes during one write in the next, so 1,000
  state-only changes take 2 writes and about 50 ms, not 1,000 writes and a
  minute (`BenchmarkApply_Records` in `internal/reconcile`). The cost is
  that every change re-reads the file.
- **Snapshots through a temporary index, not `git stash`.** `git stash
  create` touches the index and fails while `index.lock` is held, which is
  exactly when an agent is working. The cost is building trees by hand.
- **Reconcile, not imperative commands.** Config says what should be; state
  says what gitten did; each run derives the difference. The cost is a
  planner that must observe disk carefully and record each step as it goes.
- **Hidden refs for offsite copies.** Nothing shows in branch lists or
  triggers CI, and each copy owns its namespace, so force-pushes stay inside
  it. The cost is that anyone who can read the remote can fetch them.
- **Worktrees by default for agents.** Cheap and instant from the repo's
  clone; clones and reference clones are there for tools that need a
  separate `.git`.
- **No daemon.** The host's scheduler runs `gitten sync`; there is nothing
  to keep alive, and a run that can't finish just waits for the next one.
