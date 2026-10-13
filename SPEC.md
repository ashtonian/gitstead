# git-mgr: Technical Specification

## Overview

`git-mgr` is a CLI tool that discovers, clones, syncs, and backs up git repositories into a structured local directory layout. It is designed to run both interactively (bootstrapping a new machine) and as a background cron job (periodic fetch + transparent backup of uncommitted work).

**Targets**: macOS, Linux, Docker (multi-arch: amd64 + arm64).

## Goals

1. **Discover** repos from multiple GitHub, GitLab, and Gitea/Forgejo instances based on configurable rules (user, org, group, language, topics, tags, etc.)
2. **Clone** repos into a templated directory structure (e.g., `github.com/owner/repo`)
3. **Sync** (fetch-only) all tracked repos periodically -- safe to run every 15 minutes via cron. Optionally auto-discovers new repos before fetching.
4. **Backup** uncommitted changes transparently without interfering with any git operations in progress
5. **Track** all managed repos in a local state file. The state file is optional -- `git-mgr` can operate statelessly (discover + clone each run) or use a state file for incremental sync. The state file is also portable: copy it to a new machine and run `git-mgr sync` to re-clone everything. When remote_sync is enabled, the state file is included so it can bootstrap a fresh machine from the remote.
6. **Adopt** existing locally-cloned repos into management without re-cloning
7. **Cleanup** stale repos (deleted upstream, removed from org) with configurable policy
8. **Notify** on errors/completion via native OS notifications (macOS/Linux) or log file
9. **Optionally push** backup refs to remotes for offsite safety

## Non-Goals

- The discover/sync/backup subsystem is read-only with respect to your working tree and branches -- it never `git pull`s, merges, or commits. (The opt-in **workspace** subsystem is separate and *does* mutate on request; see [Workspaces](#workspaces).)
- Not a replacement for git workflows (no branch management, no PR creation)
- Not a monorepo/subrepo tool (no manifest-based dependency linking)

---

## Architecture Decisions

### git CLI vs go-git

**Decision: Shell out to `git` CLI.**

| Operation | go-git Support | Notes |
|---|---|---|
| Clone + custom SSH key | YES | `ssh.NewPublicKeysFromFile()` |
| Fetch all + prune | PARTIAL | Prune yes; must loop remotes manually |
| `git stash create` | **NO** | Not implemented (issue #606, open since 2022) |
| Update-ref custom namespace | YES | `Storer.SetReference()` |
| `git diff --cached` | PARTIAL | Not directly exposed (issue #700) |
| List untracked files | YES | Via `Status()`, but 70x slower than git on large repos |
| Rev-parse HEAD | YES | `repo.Head()` |
| For-each-ref | YES | `repo.References()` + filter |
| Push custom refspecs | YES | `PushOptions.RefSpecs` |
| Status (clean/dirty) | YES | `IsClean()` |

**Why git CLI wins:**

1. **Temporary-index plumbing** -- backups build trees with `GIT_INDEX_FILE`, `update-index --stdin`, `write-tree` and `commit-tree`, and restore with `git stash apply --index`; go-git has no equivalent
2. **Performance** -- go-git uses ~8x more memory for clone/fetch. Status is 70x slower on large repos. With 100+ repos this matters.
3. **Completeness** -- every operation works perfectly via CLI with zero caveats
4. **git is a hard dependency anyway** -- no portability win from go-git
5. **Simplicity** -- ~20 lines of wrapper code vs managing go-git's API surface

### CLI & Config Framework

**Decision: Kong (CLI) + Koanf (config)**.

- **Kong** (`github.com/alecthomas/kong`) -- struct-based CLI definition, cleaner than cobra for this scale. Subcommands as struct fields, flags as struct tags.
- **Koanf** (`github.com/knadh/koanf/v2`) -- config loading with layered precedence: defaults -> config file -> env vars -> CLI flags. Supports `koanf:"field"`, `default:"value"`, `validate:"required"` struct tags.
- **slog** (stdlib `log/slog`) -- structured JSON/text logging, no third-party dependency.
- **go-playground/validator** -- struct validation for config.

### Notifications

**Decision: Shell out to OS-native notification commands.** Zero dependencies, same thing libraries like beeep do internally.

```go
func notify(title, msg string) error {
    switch runtime.GOOS {
    case "darwin":
        return exec.Command("osascript", "-e",
            fmt.Sprintf(`display notification %q with title %q`, msg, title)).Run()
    case "linux":
        return exec.Command("notify-send", title, msg).Run()
    }
    return nil // silent on unsupported platforms / Docker
}
```

Notifications are opt-in (`notify.enabled: true`). Failures to notify are logged but never block operations.

---

## CLI Interface

```
git-mgr [global flags] <command> [command flags] [args]
```

### Global Flags

| Flag | Default | Description |
|---|---|---|
| `--config` | `~/.config/git-mgr/config.yaml` | Config file path |
| `--state` | `~/.local/share/git-mgr/state.yaml` | State file path |
| `--verbose` / `-v` | false | Verbose output |
| `--quiet` / `-q` | false | Suppress non-error output |
| `--concurrency` / `-j` | 8 | Max concurrent git operations: fetches, backups, and the clones and other changes of different repos that `sync` and `apply` make |

### Commands

#### `git-mgr setup [flags]`

Guided, re-runnable setup. Detection (package `internal/setup`) reads `gh auth
status --json hosts`, `~/.ssh/config` (with `Include`), the keys in `~/.ssh`,
the global git identity and `includeIf` files, and clones under `~/src`,
`~/code`, `~/projects`, `~/repos`, `~/work`, `~/dev`, `~/git`, `~/go/src` and the
ghq root; with network access it asks each host which account each key signs in
as (`ssh -T`, `BatchMode`, strict known_hosts, never adding to it). Defaults come
from the existing config when there is one, otherwise from detection: the base
dir and layout that place your own repos where they are (deeper layouts weigh
more), one account per gh login with its token pinned
(`gh auth token --hostname H --user U`), its key, and the recommended features.

Answers render into the config with `config.Document`: a YAML tree edit that
keeps comments and order, sets `base_dir`, `layout` (dropping `path_template`)
and the feature keys, and upserts by name each account, its provider (same
name, `account:` set) and one rule per tracked owner (`<account>` for its own
repos, `<account>/<org>` per organization), removing that provider's rules for
owners no longer tracked. Before writing it shows the diff and runs the checks
(`setup.Checker`: tokens, keys, `ssh -T`, a test signature), which `doctor` also
uses offline. After writing it adopts the chosen clones and installs the agent.

| Flag | Description |
|---|---|
| `--yes` | Take the detected and recommended answers without asking |
| `--answers <file>` | Take the answers from a YAML file (`setup.Answers`) |
| `--save-answers <file>` | Also write the answers used |
| `--dry-run` | Show the diff and checks; change nothing |
| `--offline` | No `ssh -T` or provider API calls |

#### `git-mgr init`

Create a config file with sensible defaults. Interactive prompts for base_dir, provider instances, first discovery rule.

```
$ git-mgr init
Config file: ~/.config/git-mgr/config.yaml
Base directory for repos [~/repos]: ~/projects
Add a provider? (y/n): y
  Provider type (github/gitlab/gitea/forgejo): github
  Instance name [github.com]: github.com
  API URL (leave empty for github.com): 
  Token command [gh auth token]: 
Add a discovery rule? (y/n): y
  Provider instance: github.com
  User or org? (user/org): user
  Username: your-login
  Include forks? (y/n): n
  Include archived? (y/n): n
  Tags (comma-separated, optional): personal
  Rule name [ashtonian-repos]:
Config written to ~/.config/git-mgr/config.yaml
```

#### `git-mgr discover [flags]`

Run discovery rules and show the plan for the repos they find (see
[Reconciliation](#reconciliation)).

| Flag | Description |
|---|---|
| `--apply` | Make the plan's changes that move and delete nothing: clone new repos, update origin URLs, managed git config and tags (default: preview only). Moves wait for `git-mgr apply`. |
| `--rule <name>` | Run only a specific discovery rule |
| `--tag <tag>` | Run only rules with this tag |

```
$ git-mgr discover
Discovering repos...
  [github.com] your-login: found 47 repos (3 new, 44 existing, 2 stale)
  [ghes.company.com] my-org: found 23 repos (0 new, 23 existing, 0 stale)
  [gitlab.com] my-group: found 12 repos (1 new, 12 existing, 0 stale)

New repos:
  + github.com/your-login/new-project
  + github.com/your-login/another-thing
  + github.com/your-login/experiment
  + gitlab.com/my-group/new-service

Stale repos (not found in latest discovery):
  ~ github.com/your-login/old-thing (stale since 2026-04-10)
  ~ github.com/your-login/archived-lib (stale since 2026-04-15)

Use --apply to make the changes that move and delete nothing, or `git-mgr apply` for all of them.
```

#### `git-mgr sync [flags]`

Discover, apply the plan's changes as far as `reconcile.auto` allows (default:
everything that moves and deletes nothing), fetch all tracked repos and back up
uncommitted changes. This is the primary command for cron usage. It never
creates, moves or removes workspace copies; it backs them up.

| Flag | Description |
|---|---|
| `--repos <glob>` | Filter to repos matching glob (e.g., `*/your-login/*`) |
| `--tag <tag>` | Filter to repos with this tag |
| `--no-backup` | Skip the backup step |
| `--no-fetch` | Skip fetching, only backup |
| `--no-discover` | Skip auto-discovery (even if enabled in config) |
| `--force` | Ignore min_fetch_interval, fetch everything |

```
$ git-mgr sync
Discovering repos (auto-discover enabled)...
  [github.com] your-login: 1 new repo
Syncing 83 repos (concurrency: 8)...
  Fetched: 80  Skipped (recent): 0  Skipped (locked): 1  Failed: 1  Stale: 1
  Backed up: 12 dirty repos
  Errors:
    github.com/your-login/private-thing: fetch: ssh: Connection timed out
```

**Cron safety:**
- Takes the sync run lock (`<state_file>.sync.lock`) -- if another sync is running, exits immediately with code 0
- Holds the state lock only to record each change and, at the end, every repo's outcome in one write; clones, fetches and backups run without it
- Backs up each working copy first on `sync.backup_timeout`, then fetches on `sync.timeout_per_repo`; tracked repos run first, then workspace copies, each phase on the `concurrency` pool, and each working copy is visited once
- Workspace clones aren't fetched: once their repo's clone has fetched, their `refs/remotes/origin/*` are refreshed from it (`git fetch <clone> +refs/remotes/origin/*:refs/remotes/origin/*`), without the network; worktrees share their repo's refs
- Respects `min_fetch_interval` -- skips repos fetched within the window
- All errors are collected, not fatal -- partial success is fine

#### `git-mgr backup [flags]`

Run only the backup step (no fetch).

| Flag | Description |
|---|---|
| `--repos <glob>` | Filter to specific repos |
| `--tag <tag>` | Filter by tag |
| `--list` | List existing backups instead of creating new ones |
| `--restore <id>` | Show restore instructions for a specific backup (`latest` for the newest) |

Exits non-zero if any selected repo failed to back up; the failure is also
stored as `last_backup_error` and shown as `failed` in `status`.

```
$ git-mgr backup --list --repos '*/git-mgr'
github.com/ashtonian/git-mgr:
  20261012T150000.000Z  snapshot, patch
  20261012T155117.905Z  snapshot, patch, 1 untracked path(s) left out

$ git-mgr backup --restore 20261012T155117.905Z --repos '*/git-mgr'
To restore backup 20261012T155117.905Z of github.com/ashtonian/git-mgr (onto a clean tree):
  cd ~/projects/github.com/ashtonian/git-mgr
  git stash apply --index refs/git-mgr/backups/8ed3b5a3e9e769c3/20261012T155117.905Z
  (not in this backup: huge.bin (3.0 MiB is over the 1.0 MiB file limit))
  tar -xzf ~/.local/share/git-mgr/patches/.../untracked.tar.gz
```

#### `git-mgr status [flags]`

Show state of all managed repos.

| Flag | Description |
|---|---|
| `--json` | JSON output |
| `--repos <glob>` | Filter |
| `--tag <tag>` | Filter by tag |
| `--dirty` | Only show repos with uncommitted changes |
| `--stale` | Only show stale repos |
| `--errors` | Only show repos in error state |

```
$ git-mgr status
REPO                                  STATUS   BRANCH   LAST FETCH   BACKUP   TAGS
github.com/ashtonian/git-mgr         dirty    main     2m ago       2m ago   personal
github.com/your-login/dotfiles       clean    main     2m ago       -        personal
ghes.company.com/org/api             clean    main     15m ago      -        work,backend
ghes.company.com/org/infra           error    main     failed       -        work,infra
gitlab.com/my-group/service          clean    main     2m ago       -        work
github.com/your-login/old-thing      stale    main     3d ago       -        personal
...
83 repos (2 dirty, 1 error, 1 stale, 79 clean)
```

#### `git-mgr adopt <path> [flags]`

Import an existing locally-cloned repo into management without re-cloning.

| Flag | Description |
|---|---|
| `--tag <tag>` | Assign tags (comma-separated) |
| `--scan <dir>` | Recursively scan a directory for git repos and adopt all found |
| `--dry-run` | Show what would be adopted without changing state |

```
$ git-mgr adopt ~/old-projects/cool-lib
Detected: github.com/someone/cool-lib (from remote origin)
Adopted: github.com/someone/cool-lib -> ~/old-projects/cool-lib

$ git-mgr adopt --scan ~/legacy-repos --tag legacy --dry-run
Would adopt 14 repos:
  ~/legacy-repos/api -> github.com/company/api
  ~/legacy-repos/frontend -> github.com/company/frontend
  ...
```

**Adopt logic:**
1. Read the repo's `origin` remote URL to determine host/owner/name
2. Plan it as a repo added by hand at its current path: it is never moved, and its origin URL is kept
3. Write managed git config that isn't already set (a value you set yourself is kept and reported as drift), record its copy ID, add it to state with `manually_added: true`
4. `--scan` walks directory tree looking for `.git` dirs, adopts each

#### `git-mgr clone <url> [flags]`

Add a single repo by hand and clone it, at its templated path or `--path`. It
is planned like any other repo, so a clone already at that path is adopted
instead, and it keeps that path and URL from then on.

| Flag | Description |
|---|---|
| `--tag <tag>` | Assign tags (comma-separated) |
| `--path <override>` | Override the templated path |

```
$ git-mgr clone git@github.com:someone/cool-lib.git --tag libs
Cloned to ~/projects/github.com/someone/cool-lib
Added to state with tags: [libs]
```

#### `git-mgr remove <repo-pattern> [flags]`

Stop tracking repos.

| Flag | Description |
|---|---|
| `--delete` | Also delete the clone from disk. Unique work is archived to `backup.archive_dir` first; a clone that can't be inspected or archived is kept and stays tracked, and the command exits non-zero. |
| `--archive` | Also move the clone, whole, to `backup.archive_dir/<id>/` and catalog it; `archives restore` puts it back. A clone with linked worktrees is kept and stays tracked, and the command exits non-zero. Exclusive with `--delete`. |
| `--yes` | Skip confirmation prompt |

#### `git-mgr hooks post-clone|post-create`

Runs hooks again, printing their output as well as logging it.

| Subcommand | Description |
|---|---|
| `post-clone [<pattern>] [--tag t] [--set s]` | Each selected repo's `post_clone` hooks, in its clone. A repo not on disk is skipped and said so. |
| `post-create <workspace> [<agent>...]` | The `post_create` hooks of the workspace's copies (only these agents'): the repo's, then the workspace's. |

Exits non-zero (3) when any hook fails.

#### `git-mgr archives ls|show|restore|rm`

The catalog of archives git-mgr made before taking copies and repos away:
verified bundles of unique work, and whole clones moved by `remove --archive`
or `cleanup.policy: archive`. Each archive is `<archive_dir>/<id>.bundle`
or the directory `<archive_dir>/<id>/`, with `<id>.json` describing it.

| Subcommand | Description |
|---|---|
| `ls [--workspace w] [--repo glob]` | ID, when, where it came from, the repo, what it saved. |
| `show <id>` | The description and the restore steps: the bundle's fetch-and-apply commands, or `archives restore` for a clone. |
| `restore <id> [--to path]` | Move an archived clone back to where it was (or `--to`), drop it from the catalog and track it by hand (`adopt`), so it is never judged stale again. Refuses a bundle (its steps are in `show`), an occupied destination, and -- without `--to` -- a repo tracked again meanwhile; with `--to` such a repo's archive is restored untracked. |
| `rm <id>... [--yes]` | Delete archives for good -- bundle or clone, description, LFS objects, submodules' archives -- and drop them from the catalog. Only files named for the archive, beside it, are deleted. Asks first; without a terminal, needs `--yes`. |

#### `git-mgr mirror [--repos g] [--tag t] [--set s] [--force]`

Make or update the mirrors that are due -- of each selected repo mirroring
is on for, not stale, last updated more than `mirror.interval` ago or failing
(all, with `--force`) -- `concurrency` at once. Per repo: a mirror recorded
elsewhere (`mirror_path`, the repo renamed since) is moved to
`<dir>/<key>.git` first; missing, it is made with `git clone --mirror <url>`
in a staging directory, renamed into place; present, it must have
`remote.origin.mirror=true` (else it is left alone and reported), gets the
recorded URL as origin, and fetches `origin` (`+refs/*:refs/*`, no
`--prune`); with `lfs`, `git lfs fetch --all origin`. Each within
`sync.clone_timeout`, with the repo's SSH key over SSH. Records `mirror_path`,
`last_mirror`, or `last_mirror_error`. `sync` runs the same after its
fetches unless `--no-fetch`. Mirrors are never deleted.

#### `git-mgr remote-sync [flags]`

Back up each selected working copy and take it offsite now, under the
`remote_sync` policy it resolves to (enabled or not); with no selection,
workspace copies too.

| Flag | Description |
|---|---|
| `--repos <glob>` | Filter |
| `--tag <tag>` | Filter by tag |
| `--set <name>` | Filter by named set |
| `--dry-run` | Show what would be pushed and deleted, planned from the latest backups; writes nothing |
| `--list` | List the offsite refs every machine and copy left for the selected repos, with restore commands |

Algorithm, per copy (`gitops.PlanOffsite`/`PushOffsite`):

```
namespace = render(remote_sync.namespace, Machine, Copy)   # refs/git-mgr/<machine>/<copy>
last      = state's offsite record (reset if remote or namespace changed)
latest    = this run's backup (snapshot commit, backup ID); unknown if none ran
wip: ref     push +<snapshot>:<ns>/wip/<id> unless already pushed;
             delete <ns>/wip/<older id> past keep (never the current one)
wip: branch  push +<commit of W's tree on HEAD>:refs/heads/git-mgr/wip/<machine>/<copy>;
             delete it when the copy is clean
unpushed     branches with commits no refs/remotes/* reach (a linked worktree:
             only its own branch; a detached HEAD as "HEAD")
  push       push +<tip>:<ns>/heads/<branch> when changed; delete when gone
  bundle     git bundle create <archive_dir>/unpushed/<copy>.bundle <branches> --not --remotes
one `git push` per copy with every refspec; deletions are checked with
`ls-remote` first; a failed part keeps its old record and is retried next run
```

#### `git-mgr config [flags]`

Print resolved configuration.

| Flag | Description |
|---|---|
| `--edit` | Open config in `$EDITOR` |
| `--validate` | Validate config and exit |
| `--path` | Print config file path |

#### `git-mgr version`

Print version, commit, build date.

```
$ git-mgr version
git-mgr v1.42 (abc1234, built 2026-04-17)
```

### Operate layer

Beyond the discover/sync/backup core, these commands make a fleet of repos and
agent workspaces easy to set up and operate. All repo-selecting commands share
one selector: `--repos <glob>` ∩ `--tag <tag>` ∩ `--set <name>` (named sets from
config).

| Command | Description |
|---|---|
| `bootstrap` | One-command machine setup: init (if missing) → doctor → discover --apply → sync → install-agent. `--profile` (tag scope; also names the schedule and limits it to the tag), `--no-agent`, `--dry-run`. |
| `plan [--json]` | Discover and show every change config asks for -- repos and workspaces -- with drift and blocked changes. Changes nothing. |
| `apply [--yes]` | Discover and make every change, including moves and removals; asks before those unless `--yes` (refuses without a terminal). Saves state after each change; non-zero exit if any change failed or was blocked. |
| `doctor` | Offline, read-only preflight: git present, config valid, base_dir writable, provider tokens resolve, configured SSH keys exist, whether a sync is running, state lock free within 5s (held longer, its holder is reported as stuck), each recorded schedule installed with its binary present and lingering on (systemd), doctor itself passing in each schedule's recorded environment, tracked repos on disk with matching origin. Non-zero exit on failure. |
| `install-agent` | Install a named background sync (`--name`, default `default`) running `sync --quiet` plus its selection (`--repos`/`--tag`/`--set`) every `--interval` (must divide an hour or a day). Records a stable binary path (refuses `/nix/store`, Cellar and temporary paths), the current `PATH` and a long-lived `SSH_AUTH_SOCK`; runs at nice 10 with idle I/O. launchd: LaunchAgent, or `--daemon` LaunchDaemon with `UserName` via sudo. systemd: `OnCalendar=` + `Persistent=true`; `--linger` enables lingering. cron: refuses to rewrite an unreadable crontab, backs it up first, touches only lines with its marker. Recorded in state under `schedules`; `--list`, `--uninstall`, `--print`. |
| `run [--set/--tag/--repos] -- <cmd>` | Run a command in each matching repo (parallel, bounded by concurrency). `--fail-fast`; non-zero exit if any failed. |
| `manifest export [--out]` / `import <file>` | Portable `{url,tags,path}` manifest of the tracked set, to rehydrate a new machine (captures manual adoptions). import is idempotent and clones over the url's transport. |
| `manifest save [--dry-run]` | Add each repo tracked by hand that neither `repos:` nor the shared manifest lists to the manifest; with no `manifest:`, print them as `repos:` entries. |

The shared manifest (`manifest: <path>`) has the export format (`repos:` of
`url`, `tags`, `path`, and `ssh_key`/`account` as in `repos:`). Config loading
reads it after normalizing and appends its entries to `repos:` -- a missing
file is empty, a malformed one or an entry with a credential is a load error
-- so discovery's `_manual` rule finds them on every machine. An entry's
`path` (also allowed in `repos:`) is absolute, `~`-relative, or relative to
the repo's `base_dir`, and replaces `path_template` for it. After a hand
addition is applied (`addByHand`: clone, adopt, manifest import), the repo's
entry is upserted -- URL, user tags, `config.PortablePath` of its path
(`""` when templated) -- and `remove` drops the entries of what it untracked.
Writes hold an flock (30s wait) on a lock file in the user's cache directory (`git-mgr/locks/manifest-<hash of its path>.lock`), never beside the manifest, and replace the file
through a synced temp file and rename. Entries match by repo key, so a URL's
transport may differ. Config itself is never written.
| `restore <repo> [id]` | Recover a backup: print steps (default), `--into <dir>` (clone+apply to a scratch dir), or `--apply` onto the tree (refuses dirty without `--force`). id defaults to `latest`. |
| `report [--since]` / `log [-n]` | Summarize / list recent activity from the operation log (clone/sync/backup/remove/workspace events and applied changes). |
| `config --redact` | Print the resolved config with inline provider tokens blanked (token_env/token_command kept), safe to share. |

The **operation log** is an append-only JSONL file next to the state file
(`events.jsonl`), written best-effort on every mutation (O_APPEND keeps
concurrent line writes atomic) and bounded to a few MiB by trimming the oldest
entries.

---

## Configuration

**File**: `~/.config/git-mgr/config.yaml` (XDG-compliant, overridable with `--config` or `GIT_MGR_CONFIG` env var)

**Loading precedence** (koanf, highest wins): CLI flags -> env vars (`GIT_MGR_*`) -> config file -> defaults.

```yaml
# ============================================================================
# git-mgr configuration
# ============================================================================

# Accounts: one identity on one or more hosts. Its ssh_key and git identity are
# written into every repo it covers (core.sshCommand, user.name, user.email).
# A repo's account is named by an override, its repos: entry, its rule or the
# rule's provider (`account:`), else matched by hosts and owners.
accounts:
  - name: work
    hosts: [github.com]
    owners: [acme]                 # empty = every owner on these hosts no other account claims
    ssh_key: "~/.ssh/id_work"
    git: {name: "Ashton Kinslow", email: "you@acme.com"}
    signing:                       # inherit (default) | required | off
      mode: required               # required writes commit/tag.gpgsign, gpg.format,
      key: "~/.ssh/id_work.pub"    # user.signingkey and the signer program; off sets
                                   # commit/tag.gpgsign false; doctor test-signs headless

# Base directory for all cloned repos. Supports ~ expansion.
base_dir: "~/repos"

# Go template for directory structure under base_dir.
# Available fields: {{.Host}}, {{.Owner}}, {{.Name}}, {{.FullName}} (owner/name)
path_template: "{{.Host}}/{{.Owner}}/{{.Name}}"

# Git transport for clone/fetch: "ssh" (default, uses SSH agent/keys) or "https"
# (auth via git's own credential helper; git-mgr stores no credentials).
# Overridable per repo set under `overrides:`.
clone_protocol: "ssh"

# Max concurrent git operations (clone, fetch, backup).
concurrency: 8

# How long a command waits for another run's state write before exiting 75.
lock_timeout: "2m"

# Named, reusable repo selections for `--set <name>` on status/sync/backup/run/
# remove/remote-sync. In a set: repos globs OR tags (both empty = all), then
# exclude_archived. --set composes (AND) with inline --repos/--tag.
sets:
  - name: core
    repos: ["github.com/acme/*"]
  - name: active-backend
    tags: ["backend"]
    exclude_archived: true

# Agent profiles: commands typed into an interactive shell in an agent's replica.
agents:
  - name: claude
    command: "claude"
    resume: "claude --continue"

# How new clones are made; also accounts[].clone and overrides[].clone, each
# overriding the one before field by field.
clone:
  filter: ""                 # blob:none | blob:limit=<size> | tree:<depth>
  depth: 0                   # commits per branch (with --no-single-branch); 0 = all
  sparse: []                 # cone-mode directories; [] = everything (an override's [] undoes a default)
  submodules: false          # check out submodules within sparse, recursively, with filter and depth
  lfs: auto                  # auto | skip (managed lfs.fetchexclude=*, so existing clones get it too)

# Settings every workspace starts from; see Workspaces.
workspace_defaults:
  root: "~/workspaces"
  isolation: worktree        # worktree | clone | reference-clone | shared
  agents: 1
  agent_name: "agent-{{.N}}"
  branch: "agents/{{.Workspace}}/{{.Agent}}"
  hooks:
    post_create: []          # after the repo's post_create; a workspace's own replaces it
  ports: {base: 41000, count: 10}   # each agent's block; base 0 = none; workspaces[].ports overrides
  env: {}                    # templates of AgentVars; a workspace's env adds to it, name by name
  cache_root: ""             # "" = $XDG_CACHE_HOME/git-mgr/workspaces; <cache_root>/<workspace> is shared

workspaces:
  - name: app
    repos: ["github.com/acme/api"]   # keys, globs, local paths or URLs; or set:, or path:
    agents: 4

# ============================================================================
# Provider Instances
# ============================================================================

# Named provider instances. Supports multiple GitHub, GitLab, and Gitea/Forgejo instances
# (e.g., github.com + GitHub Enterprise + self-hosted GitLab + Gitea).
# Each discovery rule references a provider by name.
providers:
  - name: "github.com"            # Unique name, referenced by discovery rules
    type: "github"                # "github", "gitlab", "gitea", or "forgejo"
    # API URL. Leave empty for github.com / gitlab.com defaults.
    url: ""                       # e.g., "https://ghes.company.com" for GitHub Enterprise
    # Auth token. Resolved in order: token field > env var > token_command output.
    token: ""
    # Env var name to check for token. Default: GITHUB_TOKEN / GITLAB_TOKEN.
    token_env: ""                 # e.g., "GHES_TOKEN" for a second GitHub instance
    # Shell command to retrieve token. Useful for credential managers.
    token_command: "gh auth token"
    # Default SSH key for all repos from this provider (overridable per-rule).
    ssh_key: ""                   # e.g., "~/.ssh/id_personal"

  # Example: GitHub Enterprise
  # - name: "ghes.company.com"
  #   type: "github"
  #   url: "https://ghes.company.com"
  #   token_env: "GHES_TOKEN"
  #   token_command: "gh auth token -h ghes.company.com"
  #   ssh_key: "~/.ssh/id_work"

  # Example: Self-hosted GitLab
  # - name: "gitlab.company.com"
  #   type: "gitlab"
  #   url: "https://gitlab.company.com"
  #   token_env: "GITLAB_COMPANY_TOKEN"
  #   token_command: ""
  #   ssh_key: "~/.ssh/id_work"

  # Example: gitlab.com
  # - name: "gitlab.com"
  #   type: "gitlab"
  #   url: ""                     # defaults to https://gitlab.com
  #   token_command: "glab auth token"

# ============================================================================
# Discovery Rules
# ============================================================================

# Each rule discovers repos from a named provider instance.
# Repos matching ANY rule are included. All rules run during `git-mgr discover`.
discovery:
  - name: "my-github"
    provider: "github.com"        # References a provider by name from above
    # Exactly one of: user, org
    user: ""                      # GitHub username (uses authenticated user if empty)
    org: ""                       # GitHub organization

    # Raw provider search query. Mutually exclusive with user/org/group and the
    # structured filters below; routed to the provider's native search API.
    # GitHub caps search results at 1000 (discover warns when exceeded).
    query: ""                     # e.g., "org:acme language:go stars:>50"

    # Filters (all optional, empty = no filter)
    include_forks: false
    include_archived: false
    visibility: "all"             # "all", "public", "private", "internal"
    languages: []                 # e.g., ["Go", "Rust"] -- any of the repo's languages, case-insensitive
    topics: []                    # e.g., ["backend"] -- repo must have ALL listed topics
    name_pattern: ""              # Regex filter on repo name (e.g., "^api-")

    # Tags assigned to repos discovered by this rule. Used for filtering in commands.
    tags: []                      # e.g., ["personal", "oss"]

    # SSH key override for this rule's repos (overrides provider default).
    ssh_key: ""                   # e.g., "~/.ssh/id_work"

  # --- GitHub Enterprise example ---
  # - name: "work-backend"
  #   provider: "ghes.company.com"
  #   org: "backend-team"
  #   topics: ["service"]
  #   tags: ["work", "backend"]
  #   ssh_key: "~/.ssh/id_work"

  # --- GitLab example ---
  # - name: "work-gitlab"
  #   provider: "gitlab.com"
  #   group: ""                   # GitLab group/subgroup path (e.g., "my-company/backend")
  #   user: ""                    # GitLab username
  #   include_forks: false
  #   include_archived: false
  #   visibility: "all"           # "all", "public", "internal", "private"
  #   topics: []
  #   name_pattern: ""
  #   tags: ["work"]
  #   ssh_key: ""

# ============================================================================
# Exclude Rules
# ============================================================================

# Repos matching ANY exclude rule are skipped, even if a discovery rule matched them.
exclude:
  # Regex on full repo name (owner/name)
  - name_pattern: ""              # e.g., "^deprecated-"
  # Explicit repo list (owner/name)
  - repos: []                    # e.g., ["my-org/legacy-monolith"]

# ============================================================================
# Per-Repo Overrides
# ============================================================================

# Override any setting for specific repos. Matched by full name (host/owner/name)
# or glob pattern. Later entries take precedence over earlier ones.
overrides:
  # Override by exact repo
  - repos: ["github.com/company/huge-monorepo"]
    sync:
      timeout_per_repo: "15m"
    backup:
      include_untracked: false

  # Override by glob pattern -- all company repos
  - repos: ["ghes.company.com/*"]
    ssh_key: "~/.ssh/id_work"
    backup:
      strategy: "patch"           # Company policy: no custom refs
    remote_sync:
      enabled: false

  # Override for a personal repo
  - repos: ["github.com/your-login/dotfiles"]
    backup:
      enabled: false
    path_template: "{{.Name}}"

# ============================================================================
# Sync Settings
# ============================================================================

sync:
  # Run discovery before sync (auto-clone new repos). When true, `git-mgr sync`
  # also runs the equivalent of `git-mgr discover --apply` before fetching.
  auto_discover: true

  # Minimum time between fetches for the same repo. Prevents redundant work on
  # frequent cron runs. Duration string (e.g., "10m", "1h").
  min_fetch_interval: "10m"

  # Deadlines; 0 means none. When one passes, git and everything it started
  # is killed, and the error names the setting.
  timeout_per_repo: "5m"    # one repo's fetch, retries included
  backup_timeout: "2m"      # one working copy's backup and status read
  clone_timeout: "30m"      # one clone
  api_timeout: "60s"        # one provider API request attempt
  discovery_timeout: "5m"   # one discovery rule: lookups, retries and rate-limit waits included

  # Number of retries on transient network failures (per repo).
  retries: 2

  # Retry backoff base duration. Actual delay: base * 2^attempt.
  retry_backoff: "2s"

# ============================================================================
# Config Changes
# ============================================================================

reconcile:
  # What sync and discover --apply change on their own: "safe" (everything that
  # moves and deletes nothing), "all" (also move repos), "none" (only report).
  auto: "safe"
  # Values git-mgr set that were changed by hand: "report" or "overwrite".
  drift: "report"

# ============================================================================
# Cleanup Policy (Stale Repos)
# ============================================================================

# Controls what happens when a previously-discovered repo is no longer found
# by any discovery rule (deleted upstream, removed from org, etc.).
cleanup:
  # Policy: "mark_stale" (default), "remove", "archive", "remove_and_delete", "ignore"
  # - mark_stale: set status to "stale" in state. No disk changes. Visible in `git-mgr status --stale`.
  # - remove: remove from state after grace_period. Keep files on disk.
  # - archive: remove from state after grace_period and move the clone, whole,
  #   to backup.archive_dir/<id>/, catalogued; `archives restore` puts it back.
  #   A clone with linked worktrees or workspace copies is kept and stays tracked.
  # - remove_and_delete: remove from state AND delete from disk after grace_period.
  #   Unique work is archived to backup.archive_dir first; a repo that can't be
  #   archived is kept, stays tracked and records the reason in its error.
  # - ignore: never mark stale, never remove; such repos keep syncing. Manual cleanup only.
  # A stale repo is no longer fetched or pushed offsite, but is still backed up.
  policy: "mark_stale"

  # Grace period before "remove", "archive" or "remove_and_delete" take effect.
  # Repo must be continuously stale for this duration. Protects against
  # temporary API failures or org permission changes.
  grace_period: "30d"

  # Never judge repos added by hand: `git-mgr clone`, `git-mgr adopt` and
  # `repos:` entries (rule "_manual"). They stay tracked until `git-mgr remove`.
  protect_manual: true

# ============================================================================
# Backup Settings
# ============================================================================

backup:
  # Enable transparent backup of uncommitted changes during sync.
  enabled: true

  # Strategy: where each snapshot (index, working tree, untracked files) is kept.
  # - stash-ref: a stash-format commit at refs/git-mgr/backups/<copy>/<id>.
  # - patch: the same snapshot as patches + a tarball under patch_dir; outlives the repo.
  # - both: both (recommended).
  strategy: "both"

  # Back up untracked files (ignored files never are).
  include_untracked: true
  # Untracked files over these sizes are left out and reported.
  max_untracked_file_mb: 50
  max_untracked_total_mb: 500

  # Retention per working copy: every backup younger than keep_all, then the
  # newest of each day younger than keep_daily, then at most max_per_copy
  # (0 = no cap). The newest backup is always kept.
  keep_all: "24h"
  keep_daily: "336h"
  max_per_copy: 100

  # Directory for patch file storage. Supports ~ expansion.
  # Default: ~/.local/share/git-mgr/patches/
  patch_dir: ""

  # Where git-mgr archives unique work (a verified git bundle plus a .json
  # restore note) before deleting a clone or worktree.
  # Default: ~/.local/share/git-mgr/archive/
  archive_dir: ""

# ============================================================================
# Mirrors
# ============================================================================

# Full mirrors of upstreams; also accounts[].mirror and overrides[].mirror
# ({enabled, lfs}), the most specific winning.
mirror:
  enabled: false
  dir: ""                         # "" = ~/.local/share/git-mgr/mirrors; <dir>/<key>.git
  interval: "24h"                 # sync updates a mirror at most this often (a failing one every sync)
  lfs: false                      # git lfs fetch --all into the mirror

# ============================================================================
# Remote Sync Settings
# ============================================================================

remote_sync:
  # Take each working copy offsite during sync: uncommitted work and commits
  # no remote has, pushed under a hidden namespace the copy owns. `git-mgr
  # remote-sync` does it on demand either way. Any key below can be set per
  # account (accounts[].remote_sync) or per repo (overrides[].remote_sync).
  enabled: false

  # A remote name, or a URL template ({{.Host}}, {{.Owner}}, {{.Name}},
  # {{.FullName}}) for a separate backup remote, e.g.
  # "git@backup.example:{{.Host}}/{{.FullName}}.git". Hidden refs don't show
  # in branch lists or trigger CI, but anyone who can read the remote can
  # fetch them.
  remote: "origin"

  # Uncommitted work: "ref" pushes each new backup to <namespace>/wip/<id>
  # (untracked files and the staged/unstaged split included); "branch" pushes
  # a visible branch git-mgr/wip/<machine>/<copy> (tracked changes only, on
  # top of HEAD); "off" pushes none.
  wip: "ref"

  # Commits no remote has: "push" saves each such branch as
  # <namespace>/heads/<branch> (your branches are never pushed); "bundle"
  # writes them to backup.archive_dir/unpushed/<copy>.bundle (protects against
  # a deleted branch, not a lost disk); "off" does neither.
  unpushed: "push"

  # The hidden ref prefix each copy owns on the remote. Must stay under refs/,
  # outside branches and tags, and include {{.Copy}}.
  namespace: "refs/git-mgr/{{.Machine}}/{{.Copy}}"

  # How long a pushed WIP ref stays; the current one always stays.
  keep: "720h"

# ============================================================================
# Hooks
# ============================================================================

# Commands run with /bin/sh -c in a checkout git-mgr just made; also
# accounts[].hooks and overrides[].hooks (most specific list wins, [] = none),
# and workspace_defaults.hooks / workspaces[].hooks for post_create.
hooks:
  post_clone: []                  # in a repo's new clone; never in one adopted
  post_create: []                 # in each new workspace copy, then the workspace's own
  timeout: "15m"                  # each command; 0 = none

# ============================================================================
# Notifications
# ============================================================================

notify:
  # Enable native OS notifications (macOS Notification Center / Linux notify-send).
  # Notifications fire on: sync errors, sync completion summary, stale repo warnings.
  # Silently ignored in Docker/headless (no error if notification command missing).
  enabled: false

  # When to notify:
  on_error: true                  # Notify when repos fail to fetch/backup
  on_completion: false            # Notify on every sync completion (noisy for cron)
  on_stale: true                  # Notify when new stale repos detected
  on_waiting: true                # Notify when an agent is waiting for you
  waiting_after: "30s"            # a tmux pane quiet this long after working is waiting; 0 = off

# ============================================================================
# Logging
# ============================================================================

log:
  # Log level: "debug", "info", "warn", "error"
  level: "info"

  # Log format: "json" or "text"
  format: "json"

  # Log file path. Empty = stderr only. Useful for cron.
  # Logs always go to stderr; this adds a file output in addition.
  file: ""                        # e.g., "~/.local/share/git-mgr/git-mgr.log"
```

---

## State File

**File**: `~/.local/share/git-mgr/state.yaml` (XDG-compliant, overridable with `--state` or `GIT_MGR_STATE` env var)

The state file records what git-mgr applied to each repo and workspace -- what
the reconciler compares with config to turn a config edit into moves, URL and
git config changes -- and what the last run found, which `status` shows without
touching any repo. It is one YAML file, read and written atomically under flock.
Every write rewrites the whole file, so its cost grows with the number of
tracked repos (about 60 ms at 1,000 and 400 ms at 5,000 on an M2 Pro; nearly all
of it YAML encoding); apply shares writes between actions to keep their number
small (see [Reconciliation](#reconciliation)).

```yaml
# State schema version. Older files are migrated on load; a file newer than the
# running git-mgr is refused rather than rewritten.
version: 3

# Last time any sync operation completed.
last_sync: "2026-04-17T10:30:00Z"

# Names this machine in offsite refs; set on first use.
machine: mac-1a2b3c

# Map of repos, keyed by canonical identifier (host/owner/name). The host
# never carries a port, whether it came from a provider's URL or a clone's
# origin, so a repo discovered and the same repo adopted share one key.
repos:
  github.com/ashtonian/git-mgr:
    # --- What git-mgr applied ---
    clone_url: "git@github.com:ashtonian/git-mgr.git"
    host: "github.com"
    owner: "your-login"
    name: "git-mgr"
    full_name: "ashtonian/git-mgr"
    provider_id: "4242"            # the host's repo ID; survives renames and transfers
    copy_id: "8ed3b5a3e9e769c3"    # the working copy's ID (also in .git/git-mgr-copy-id)
    local_path: "/Users/me/repos/github.com/ashtonian/git-mgr"
    cloned: true
    discovered_by: "my-github"     # the rule that finds it; absent for one added by hand
    tags: ["personal", "oss"]      # rule tags + user_tags
    user_tags: ["oss"]             # given by hand (clone/adopt --tag, manifest)
    manually_added: false          # added by hand (clone, adopt, manifest import): path and URL are pinned
    managed_config:                # repo-local keys git-mgr set, and the values it set
      core.sshcommand: "ssh -i '/Users/me/.ssh/id_work' -o IdentitiesOnly=yes"
      user.email: "you@acme.com"
    is_archived: false             # for repo sets with exclude_archived
    stale_since: ""                # when discovery first missed it; cleanup's grace period runs from here

    # --- What the last run found ---
    status: "dirty"                # clean, dirty, conflict, error, missing, stale
    current_branch: "feat/backup"
    last_fetch: "2026-04-17T10:30:00Z"
    last_fetch_error: ""
    mirror_path: "/Volumes/backup/git-mirrors/github.com/ashtonian/git-mgr.git"   # where its mirror is
    last_mirror: "2026-10-13T06:00:00Z"
    last_mirror_error: ""
    last_backup: "2026-04-17T10:30:00Z"   # last time the current state was known to be saved
    last_backup_error: ""
    offsite_remote: origin                # what remote_sync last left offsite, so the next push
    offsite_namespace: refs/git-mgr/mac-1a2b3c/8ed3b5a3e9e769c3   # sends only what changed and
    offsite_wip: ["20260417T103000.000Z"]                         # deletes only refs it made
    offsite_heads: {feat/backup: 0123abcd...}
    last_offsite: "2026-04-17T10:30:00Z"
    last_offsite_error: ""

# Workspaces. declared: true means config declares it, so removing the
# declaration removes the workspace; workspaces created from flags or adopted
# are kept as recorded. Version 2's workgroups are migrated to workspaces: each
# member becomes an agent's replica with one copy.
workspaces:
  app:
    name: app
    dir: "/Users/me/workspaces/app"
    isolation: worktree          # worktree | clone | reference-clone | shared
    base_ref: ""                 # "" = the upstream's default branch
    declared: true
    created_at: 2026-10-12T14:00:00Z
    replicas:                    # one per agent
      - agent: agent-1
        path: "/Users/me/workspaces/app/agent-1"
        copies:                  # one per repo; the replica itself when there is one repo
          - repo: github.com/ashtonian/git-mgr   # the tracked repo it is made from; "" if untracked
            path: "/Users/me/workspaces/app/agent-1"
            branch: agents/app/agent-1
            copy_id: "1f2e3d4c5b6a7980"
            url: ""              # a clone's origin, as git-mgr set it
            managed_config: {}   # a clone's managed git config
            published: 2026-10-12T17:00:00Z    # when ws publish last pushed the branch
            pr: https://github.com/ashtonian/git-mgr/pull/42
            last_backup: 2026-10-12T14:00:00Z   # backup and offsite status, as for repos
        retiring: ""             # when a retire began; set until it finishes
        port: 41000              # the agent's block of ports, given on first need, kept for the replica's life
        ports: 10
        run:                     # the agent's latest run; whether it runs now is its lease's to say
          profile: claude
          command: "claude --continue"
          pid: 61060
          launcher: tmux
          session: "%3"
          started: 2026-10-12T15:00:00Z
          stop_requested: ""     # when `ws stop` asked it to end
          waiting: ""            # when it was last seen waiting for you; zero once it works again
          waiting_for: ""        # what it said it waits for
          ended: 2026-10-12T17:30:00Z
          exit_code: 0

# What each fleet's last open left: the launcher, and the Muxy projects made.
fleets:
  daily: {name: daily, launcher: muxy, opened: 2026-10-12T15:00:00Z, muxy_projects: {app: "8f2c..."}}

# Archives made before copies and repos were taken away: bundles of unique
# work, and whole clones (clone: true; path is the clone's directory).
archives:
  - id: github.com_acme_old-lib-20261013T060000.000000000Z
    path: /Users/me/.local/share/git-mgr/archive/github.com_acme_old-lib-20261013T060000.000000000Z
    clone: true
    repo: github.com/acme/old-lib
    source: /Users/me/repos/github.com/acme/old-lib
    branch: main
    work: the whole clone, with 1 unpushed commit
    created: 2026-10-13T06:00:00Z
  - id: app-agent-3-git-mgr-20261012T170000.000000000Z
    path: /Users/me/.local/share/git-mgr/archive/app-agent-3-git-mgr-20261012T170000.000000000Z.bundle
    repo: github.com/ashtonian/git-mgr
    workspace: app
    agent: agent-3
    source: /Users/me/workspaces/app/agent-3
    branch: agents/app/agent-3
    work: 2 changed files, 1 untracked file
    created: 2026-10-12T17:00:00Z

# Copies retired, newest last (the newest 500 are kept).
tombstones:
  - {workspace: app, agent: agent-3, repo: github.com/ashtonian/git-mgr, path: /Users/me/workspaces/app/agent-3,
     branch: agents/app/agent-3, pr: "", retired: 2026-10-12T17:00:00Z, archive: app-agent-3-git-mgr-20261012T170000.000000000Z}

# Background syncs `install-agent` installed, so doctor can check them and
# --uninstall can remove them by name.
schedules:
  work:
    name: work
    kind: launchd
    interval: 5m0s
    bin: /opt/homebrew/bin/git-mgr
    global: [--config, /Users/me/.config/git-mgr/config.yaml, --state, /Users/me/.local/share/git-mgr/state.yaml]
    sync: [--tag, work]
    env: {PATH: "/opt/homebrew/bin:/usr/bin:/bin"}
    log_path: /Users/me/.local/share/git-mgr/git-mgr.log
    installed: 2026-10-12T14:00:00Z
```

## Reconciliation

Package `internal/reconcile` brings tracked repos on disk and in state in line
with config, and is the plan engine the workspace subsystem
(`internal/workspace`, see [Workspaces](#workspaces)) builds its actions on. Each
run compares, for every repo and workspace:

- **desired** -- `reconcile.Repos` derives it from config and this run's
  discovery through one resolver: path (`base_dir`/`path_template` after
  overrides), origin URL (`clone_protocol`), SSH key (override, `repos:` entry,
  rule, provider), tags (rule tags ∪ `user_tags`), rule, provider ID, managed
  git config; `workspace.Desired` derives each workspace's replicas and copies;
- **recorded** -- what git-mgr set, from state;
- **on disk** -- `reconcile.Observe`: whether the path exists and is a repo,
  and with one `git config --local --list` its origin and local config, plus its
  copy ID and common git dir.

Desired against recorded gives the plan's actions; on disk against recorded
gives drift (a value git-mgr set was changed by hand), which is reported and
left unless `reconcile.drift: overwrite`.

| Action | Disruptive | Effect |
|---|---|---|
| `clone` | no | Clone a new repo, or one gone from disk; write managed config; record. |
| `adopt` | no | Record a clone already at the desired path (an interrupted clone, or `adopt`). |
| `rename` | no | A tracked repo with the same provider ID on the same host that discovery no longer finds under its key: rekey it and the workspace copies made from it. |
| `url` | no | `git remote set-url origin`. |
| `config`, `copy-config` | no | Set or unset a managed key (`core.sshcommand`, `user.name`, `user.email`, the signing keys) in a repo or workspace clone, or start/stop managing one already set to the wanted value. |
| `tags` | no | Update tags and discovering rule in state. |
| `move` | yes | Rename the directory (same filesystem only), remove now-empty parents below `base_dir`, then reconnect what was made from it: `git worktree repair`, and each workspace clone's `git-mgr` remote and alternates. A move already done by hand or before an interruption is just recorded. |
| `create-workspace` | no | Record a declared workspace (and, shared, its agents). |
| `create-copy` | no | Make an agent's copy from the repo's clone (worktree, local clone or `--shared` clone), write a clone's managed config, record it with its copy ID; or record one already there. |
| `copy` | no | Record which tracked repo a copy is of (a copy migrated or adopted without one). |
| `move-copy` | yes | `git worktree move`, or a rename for a clone. |
| `remove-copy`, `remove-workspace` | yes | Archive unique work to `backup.archive_dir`, then remove; a copy that can't be archived is kept. |
| `forget-copy`, `copy-url`, `copy-source`, `workspace` | no | Drop a copy gone from disk; a clone's origin URL; a clone's `git-mgr` remote after its repo moved; a workspace's own settings. |

Repos added by hand are pinned: their path and URL are the recorded ones. A
repo whose rule failed, or that a filtered run didn't cover, is not judged.
Paths wanted by two repos, occupied destinations, an isolation change of a
workspace, and a copy that would move into or out of its own folder are
reported as blocked. A workspace naming an untracked local repo or URL adds an
`adopt` or `clone` of it to the repo part of the plan.

`workspace.Apply` runs repo actions first, then plans workspaces again against
the updated state, since copies are made from repos (a copy of a repo the same
run clones). The first plan is made against the state as it will be once the
run's clones are done; a disruptive workspace action in the second plan runs
only if the first showed it.

`Plan.Apply` first records observations (copy IDs, provider IDs, the archived
flag), then runs the selected actions. A repo's actions run in plan order --
rename first, move last -- and an action on a target whose earlier action
failed is skipped. Different repos' actions run at once, up to `concurrency`
doing disk work (a clone, a `git config` write). Each action does its disk work
first and then records the result; an action waiting for its record to be saved
gives up its slot, and the records that arrive while one state write runs are
saved together by the next (`state.GroupCommit`), so 1,000 changes cost a
couple of writes rather than 1,000. A move renames its directory, and tidies
the parents it emptied, as it is recorded, under the state lock. Serial
actions -- every workspace action -- run after the others, one at a time, since
a workspace's copies share its record and their repos must be in place. Every action looks at the disk before acting,
so repeating an interrupted run finishes it instead of failing on what the first
run did.

`sync` and `discover --apply` apply repo actions selected by `reconcile.auto`
(`safe`: non-disruptive; `all`; `none`). `ws ensure` applies non-disruptive
workspace actions. `apply` applies everything.

### State File Locking

```
State lock:    <state_file>.lock       (e.g. ~/.local/share/git-mgr/state.yaml.lock)
Sync run lock: <state_file>.sync.lock
Mechanism:     flock(fd, LOCK_EX | LOCK_NB), retried every 100ms
```

The state lock guards one write. Every command that changes state does its slow
work -- clones, fetches, backups, archives, prompts -- without it, then records
the outcome with an update:

1. Acquire the state lock, waiting up to `lock_timeout` (default 2m); past
   that, fail with exit code 75
2. Re-read the state file
3. Apply the change to what was read, skipping anything that changed meanwhile
   (a repo moved or untracked by another run keeps its new record)
4. Write state to `<state_file>.tmp` and `os.Rename` it over the state file
   (atomic on POSIX)
5. Release the lock

Commands that apply a plan (`sync`, `apply`, `clone`, `adopt`, `discover
--apply`, `manifest import`, `ws create`, `ws ensure`, `setup`) read state
without the lock and record each change with an update, sharing the update with
the changes recorded meanwhile, so two runs' changes never overwrite each
other. The sync run lock keeps syncs from overlapping; it never
waits, and a sync that finds it held exits 0. Locks are per open file
description, so a process never holds the state lock while asking for it again.

Stale cleanup under `remove_and_delete` follows the same protocol: repos are
marked stale under the lock, their files archived and deleted without it, and
untracked under the lock only if still stale at the same path. A repo whose
files cleanup kept stays tracked, is retried next run, and is listed with the
reason; `sync` counts it as a failure.

---

## Transparent Backup: Detailed Algorithm

### Overview

A backup is a **snapshot** of one working copy: its index, the tracked files in
its working tree, and its untracked files (ignored files never). It is built in
temporary index files, so the repository's own index and `index.lock` are never
read for writing or locked: a backup taken while an agent's `git commit` holds
the lock succeeds and leaves the lock alone. Nothing in the working tree, index,
branches or stash list changes.

The snapshot is stored in up to two forms, chosen by `backup.strategy`:

1. **Snapshot ref** (`stash-ref`, `both`) -- a commit in the layout `git stash`
   uses, kept at `refs/git-mgr/backups/<copy-id>/<backup-id>`.
2. **Patch backup** (`patch`, `both`) -- the same snapshot exported to files in
   `<patch_dir>/<copy-id>/<backup-id>/`, which outlive the repository.

### Working copies

Every clone and every linked worktree is a **working copy** with its own copy
ID: 16 hex characters stored in the copy's private git directory
(`.git/git-mgr-copy-id`, or `.git/worktrees/<name>/git-mgr-copy-id`). Linked
worktrees share one ref namespace, so the copy ID keeps their backups, and their
retention, apart. The ID moves with the copy (`git worktree move`, `mv` of a
clone).

### Snapshot layout

```
       W  tracked working tree      <- the snapshot commit; refs point here
     / | \
    B  I  U
       |
       B
```

- **B** = HEAD when the snapshot was taken (absent on an unborn branch, where
  I becomes W's first parent)
- **I** = the index (on unresolved conflicts: B's tree, and `index_unmerged`
  is recorded)
- **U** = untracked files that fit the limits (absent when there are none)

`git stash apply --index W` restores the staged and unstaged changes and the
untracked files exactly. Snapshot commits carry a fixed identity
(`git-mgr <git-mgr@localhost>`), are never signed, and are dated with B's commit
date, so an unchanged state always produces the same W. A backup whose W equals
the copy's latest backup is not saved again (outcome `unchanged`).

### Algorithm

```
func Backup(copy, cfg) (result, error):
    if `git --no-optional-locks status --porcelain -z` is empty:
        return clean

    I.tree  = write-tree   with GIT_INDEX_FILE=<copy of .git/index>
    W.tree  = add -u; write-tree   with GIT_INDEX_FILE=<another copy>
    U.tree  = update-index --add --stdin <untracked files>; write-tree
              with GIT_INDEX_FILE=<new empty index>
        untracked = ls-files -z --others --exclude-standard
        skipped, with a reason: nested git repositories, files over
        max_untracked_file_mb, files past max_untracked_total_mb, unreadable
        files, sockets and other non-regular files
    I, U, W = commit-tree ...   (fixed identity and date, --no-gpg-sign)

    copyID = CopyID(copy)
    if W == latest backup's W (and it has every form the strategy wants):
        return unchanged(latest.ID)

    id = UTC time "20060102T150405.000Z", later than the latest ID
    stash-ref: update-ref refs/git-mgr/backups/<copyID>/<id> W "" (create only)
    patch:     build <patch_dir>/<copyID>/<id>.partial/, rename to <id>/
    prune each form that just succeeded, by retention
    any requested form that failed -> error (the result still names what was saved)
```

### Patch backup layout

```
~/.local/share/git-mgr/patches/
└── 8ed3b5a3e9e769c3/                  # copy ID
    ├── copy.json                      # copy ID, path, remotes
    ├── 20261012T155117.905Z/
    │   ├── metadata.json              # id, snapshot W, base B, branch, skipped paths
    │   ├── staged.patch               # git diff --binary B..I (trees)
    │   ├── unstaged.patch             # git diff --binary I..W (trees)
    │   └── untracked.tar.gz           # U's files, written from the object store
    └── 20261012T163000.001Z/
```

Patches are tree-to-tree diffs with fixed options (`--binary --no-renames
--no-ext-diff --no-textconv`, `a/` and `b/` prefixes), so user diff settings
never leak in. The tarball is written in Go from U's blobs: no path quoting, no
argument lists, symlinks and executable bits kept. `copy.json` lets a fresh
clone at the same path find the patch backups of the copy it replaced.

### Retention

Per copy and per form, after a successful backup: keep every backup younger
than `keep_all` (24h), then the newest backup of each UTC day younger than
`keep_daily` (14 days), then at most `max_per_copy` (100). The newest backup is
always kept. A failed backup prunes nothing.

### Restore

| Form | In place (`restore --apply`, `ws recover --apply`) | Scratch (`--into <dir>`) |
|---|---|---|
| Snapshot ref | `git stash apply --index W`; on an unborn branch, `read-tree` + `checkout-index` of W and U, then `read-tree` of I | clone, check out B detached (or an orphan branch), then the same |
| Patch only | `git apply --index staged.patch`, `git apply unstaged.patch`, untar refusing to overwrite or leave the tree | clone, check out the recorded base, then the same |

`--apply` refuses a dirty tree without `--force`; `git stash apply` itself
refuses to overwrite an untracked file and reports merge conflicts when HEAD
has moved since the backup.

### Safety Properties

| Property | Guarantee |
|---|---|
| Working tree, index, branches, stash | Never modified: all trees are built in temporary index files |
| `index.lock` | Never taken; backups run while another git command holds it |
| Optional locks | Every git call runs with `--no-optional-locks` |
| Worktrees | Separate copy IDs, refs and retention |
| Unchanged state | Not saved again; identical snapshots have identical commit IDs |
| Large or odd untracked files | Left out and reported with a reason, never silently dropped |
| Partial failure | Reported as an error and in `last_backup_error`; prunes nothing |
| Atomic writes | Refs via `update-ref`; patch backups via `.partial` + rename; JSON via temp + rename |

---

## Discovery: Provider Interface

```go
// internal/discovery/provider.go

// Repo represents a discovered repository from any provider.
type Repo struct {
    Host          string   // "github.com", "ghes.company.com", "gitlab.com"
    Owner         string   // User or org/group name
    Name          string   // Repository name
    FullName      string   // "owner/name"
    SSHURL        string   // "git@github.com:owner/name.git"
    Description   string
    Language      string
    Topics        []string
    IsFork        bool
    IsArchived    bool
    Visibility    string   // "public", "private", "internal"
    DefaultBranch string
}

// Provider discovers repos from a hosting service instance.
type Provider interface {
    // Name returns the provider instance name (e.g., "github.com", "ghes.company.com").
    Name() string

    // Discover returns all repos matching the given rule.
    // Handles pagination internally.
    Discover(ctx context.Context, rule config.DiscoveryRule) ([]Repo, error)
}
```

### GitHub Provider

```go
// internal/discovery/github.go

// Uses google/go-github/v88 (official Google-maintained SDK).
// Supports github.com and GitHub Enterprise (custom URL).
// Token resolution: provider.Token > provider.TokenEnv env var > provider.TokenCommand output.

func NewGitHubProvider(providerCfg config.Provider) (*GitHubProvider, error):
    // If providerCfg.URL is empty, defaults to https://api.github.com
    // If providerCfg.URL is set, uses github.NewEnterpriseClient(url, url, httpClient)
    // Resolves token via providerCfg.Token / TokenEnv / TokenCommand

func (g *GitHubProvider) Discover(ctx context.Context, rule config.DiscoveryRule) ([]Repo, error):
    // If rule.User is set:
    //   client.Repositories.ListByUser(ctx, user, &ListOptions{PerPage: 100})
    // If rule.User is empty (authenticated user):
    //   client.Repositories.ListByAuthenticatedUser(ctx, &RepositoryListOptions{PerPage: 100})
    // If rule.Org is set:
    //   client.Repositories.ListByOrg(ctx, org, &RepositoryListByOrgOptions{PerPage: 100})
    //
    // Paginate until all pages consumed.
    // Apply client-side filters: fork, archived, visibility, language, topics, name_pattern.
    // Set Host from providerCfg.URL (parsed) or "github.com".
    // Convert to []Repo.
```

### GitLab Provider

```go
// internal/discovery/gitlab.go

// Uses gitlab.com/gitlab-org/api/client-go (official GitLab Go SDK, maintained by GitLab Inc.).
// Supports gitlab.com and self-hosted GitLab (custom URL).
// Token resolution: provider.Token > provider.TokenEnv env var > provider.TokenCommand output.

func NewGitLabProvider(providerCfg config.Provider) (*GitLabProvider, error):
    // If providerCfg.URL is empty, defaults to https://gitlab.com
    // Otherwise uses custom URL

func (g *GitLabProvider) Discover(ctx context.Context, rule config.DiscoveryRule) ([]Repo, error):
    // If rule.User is set:
    //   client.Projects.ListUserProjects(user, &ListProjectsOptions{PerPage: 100})
    // If rule.Group is set:
    //   client.Groups.ListGroupProjects(group, &ListGroupProjectsOptions{PerPage: 100, IncludeSubGroups: true})
    //
    // Paginate, filter, convert to []Repo.
    // Set Host from providerCfg.URL (parsed) or "gitlab.com".
```

### Filter Pipeline

```go
// internal/discovery/filter.go

func FilterRepos(repos []Repo, rule config.DiscoveryRule, excludes []config.ExcludeRule) []Repo:
    var result []Repo
    for _, repo := range repos:
        if !rule.IncludeForks && repo.IsFork: continue
        if !rule.IncludeArchived && repo.IsArchived: continue
        if rule.Visibility != "all" && repo.Visibility != rule.Visibility: continue
        if len(rule.Languages) > 0 && !contains(rule.Languages, repo.Language): continue
        if len(rule.Topics) > 0 && !hasAll(repo.Topics, rule.Topics): continue
        if rule.NamePattern != "" && !regexp.MustCompile(rule.NamePattern).MatchString(repo.Name): continue

        excluded := false
        for _, excl := range excludes:
            if excl.NamePattern != "" && regexp.MustCompile(excl.NamePattern).MatchString(repo.Name): excluded = true
            if contains(excl.Repos, repo.FullName): excluded = true
        if excluded: continue

        result = append(result, repo)
    return result
```

### Stale Repo Detection

During discovery, compare newly-discovered repo set against state. Stale
detection runs only when every rule in the pass succeeded, and only judges repos
the pass could have found: an unfiltered pass covers every tracked repo, a pass
filtered by `--tag` or `--rule` covers only repos whose `discovered_by` rule ran.

```
func DetectStale(state, discovered, scope):
    for key, repo := range state.Repos:
        if repo.ManuallyAdded: continue              // never stale
        if key in discovered:
            repo.StaleSince = ""                      // clear stale flag
            repo.LastDiscovered = time.Now()
        else if !scope.Covers(repo):
            continue                                  // another rule's repo
        else:
            if repo.StaleSince == "":
                repo.StaleSince = time.Now()          // newly stale
            repo.Status = "stale"

    // Apply cleanup policy
    if config.Cleanup.Untracks():                     // remove, archive, remove_and_delete
        for key, repo := range state.Repos:
            if repo.Status == "stale" && time.Since(repo.StaleSince) > config.Cleanup.GracePeriod:
                switch config.Cleanup.Policy:
                case "remove_and_delete":
                    // archive unique work, then delete; keep the repo on any failure
                    if err := gitops.RemoveClone(repo.LocalPath, config.Backup.ArchiveDir); err != nil:
                        repo.Error = err
                        continue
                case "archive":
                    // move the whole clone into the archive; keep the repo on any failure
                    if err := gitops.ArchiveClone(repo.LocalPath, config.Backup.ArchiveDir); err != nil:
                        repo.Error = err
                        continue
                delete(state.Repos, key)
```

---

## Git Operations Layer

```go
// internal/gitops/exec.go

// Git executes a git command in the given repo directory.
func Git(ctx context.Context, repoPath string, args ...string) (string, error) {
    cmd := exec.CommandContext(ctx, "git", append([]string{"-C", repoPath}, args...)...)
    var stdout, stderr bytes.Buffer
    cmd.Stdout = &stdout
    cmd.Stderr = &stderr
    err := cmd.Run()
    if err != nil {
        return "", fmt.Errorf("git %s: %w\n%s", strings.Join(args, " "), err, stderr.String())
    }
    return strings.TrimSpace(stdout.String()), nil
}

// GitWithSSHKey executes a git command with a custom SSH key.
func GitWithSSHKey(ctx context.Context, repoPath, sshKeyPath string, args ...string) (string, error) {
    cmd := exec.CommandContext(ctx, "git", append([]string{"-C", repoPath}, args...)...)
    if sshKeyPath != "" {
        cmd.Env = append(os.Environ(),
            fmt.Sprintf("GIT_SSH_COMMAND=ssh -i %s -o IdentitiesOnly=yes -o StrictHostKeyChecking=accept-new", sshKeyPath),
        )
    }
    // ... same stdout/stderr capture as Git()
}

// GitExitCode runs a git command and returns just the exit code.
func GitExitCode(ctx context.Context, repoPath string, args ...string) int {
    cmd := exec.CommandContext(ctx, "git", append([]string{"-C", repoPath}, args...)...)
    cmd.Run()
    return cmd.ProcessState.ExitCode()
}
```

### Lock Detection

```go
// internal/gitops/lock.go

var gitLockFiles = []string{
    ".git/index.lock", ".git/HEAD.lock", ".git/config.lock",
    ".git/shallow.lock", ".git/refs/stash.lock", ".git/packed-refs.lock",
}

func IsLocked(repoPath string) (bool, string) {
    for _, lf := range gitLockFiles {
        if _, err := os.Stat(filepath.Join(repoPath, lf)); err == nil {
            return true, lf
        }
    }
    return false, ""
}
```

---

## Sync Orchestrator

```go
// internal/sync/sync.go

func Sync(ctx context.Context, cfg *config.Config, state *state.State, opts SyncOptions) (*SyncResult, error) {
    lock, err := acquireLock(cfg.StateFile + ".lock")
    if err != nil {
        return nil, fmt.Errorf("another git-mgr instance is running")
    }
    defer lock.Release()

    // Auto-discover new repos if enabled
    if cfg.Sync.AutoDiscover && !opts.NoDiscover {
        discoverAndApply(ctx, cfg, state)
    }

    result := &SyncResult{}
    sem := make(chan struct{}, cfg.Concurrency)
    var mu sync.Mutex
    g, ctx := errgroup.WithContext(ctx)

    for key, repo := range state.Repos {
        key, repo := key, repo

        // Resolve per-repo overrides
        repoCfg := cfg.ResolveForRepo(key)

        // Filter by glob or tag
        if opts.ReposGlob != "" && !matchGlob(opts.ReposGlob, key): continue
        if opts.Tag != "" && !containsTag(repo.Tags, opts.Tag): continue

        g.Go(func() error {
            sem <- struct{}{}
            defer func() { <-sem }()

            repoCtx, cancel := context.WithTimeout(ctx, repoCfg.Sync.TimeoutPerRepo)
            defer cancel()

            // Fetch
            if !opts.NoFetch {
                if time.Since(repo.LastFetch) < repoCfg.Sync.MinFetchInterval && !opts.Force {
                    mu.Lock(); result.SkippedRecent++; mu.Unlock()
                } else {
                    err := fetchWithRetry(repoCtx, repo, repoCfg)
                    mu.Lock()
                    if err != nil {
                        result.Failed++
                        result.Errors = append(result.Errors, SyncError{Repo: key, Err: err})
                        repo.LastFetchError = err.Error()
                        repo.Status = "error"
                    } else {
                        result.Fetched++
                        repo.LastFetch = time.Now()
                        repo.LastFetchError = ""
                    }
                    mu.Unlock()
                }
            }

            // Backup
            if !opts.NoBackup && repoCfg.Backup.Enabled {
                err := gitops.Backup(repoCtx, repo, repoCfg.Backup)
                mu.Lock()
                if err != nil {
                    result.BackupErrors = append(result.BackupErrors, SyncError{Repo: key, Err: err})
                } else if repo.Status == "dirty" {
                    result.BackedUp++
                }
                mu.Unlock()
            }

            updateRepoStatus(repoCtx, repo)
            return nil
        })
    }

    g.Wait()

    // Notifications
    if cfg.Notify.Enabled {
        sendSyncNotifications(cfg, result)
    }

    if err := state.Save(); err != nil {
        return result, fmt.Errorf("saving state: %w", err)
    }
    return result, nil
}
```

---

## Remote Sync: Push Refs

```go
// internal/remote/git_push.go

func PushBackupRefs(ctx context.Context, repoPath, remoteName, sshKeyPath string) error {
    _, err := gitops.Git(ctx, repoPath, "remote", "get-url", remoteName)
    if err != nil {
        return fmt.Errorf("remote %q not found", remoteName)
    }
    refspec := "refs/git-mgr/*:refs/git-mgr/*"
    _, err = gitops.GitWithSSHKey(ctx, repoPath, sshKeyPath, "push", remoteName, refspec)
    return err
}
```

---

## Path Template

```go
// internal/pathtemplate/template.go

type RepoData struct {
    Host     string // "github.com", "ghes.company.com"
    Owner    string // "your-login"
    Name     string // "git-mgr"
    FullName string // "ashtonian/git-mgr"
}

func Resolve(tmpl string, data RepoData) (string, error) {
    t, err := template.New("path").Parse(tmpl)
    if err != nil {
        return "", fmt.Errorf("invalid path template: %w", err)
    }
    var buf bytes.Buffer
    if err := t.Execute(&buf, data); err != nil {
        return "", fmt.Errorf("executing path template: %w", err)
    }
    result := filepath.Clean(buf.String())
    if filepath.IsAbs(result) || strings.Contains(result, "..") {
        return "", fmt.Errorf("path template produced unsafe path: %s", result)
    }
    return result, nil
}
```

---

## Project Structure

```
git-mgr/
├── main.go                 # entry point: signal handling, kong wiring
├── config.example.yaml     # annotated reference config (loaded by a test)
├── e2e/                    # end-to-end tests: Forgejo in Docker, plus local ones
└── internal/
    ├── cmd/                # the CLI: one *_impl.go per command family
    ├── config/             # loading (pkg config), defaults, validation, resolution, edits
    ├── discovery/          # provider clients, filters, stale detection and cleanup
    ├── reconcile/          # desired vs recorded vs on-disk for repos; the plan engine
    ├── workspace/          # workspaces: specs, planning, copies, health (depends on the above)
    ├── state/              # the state file: schema, migrations, locks, GroupCommit
    ├── gitops/             # everything that runs git
    ├── scheduler/          # launchd, systemd, cron
    ├── setup/              # the setup wizard's detection, answers and checks
    ├── repourl/            # remote URL parsing and redaction
    ├── oplog/, notify/     # operation log, desktop notifications
    └── pathtemplate/, pathutil/, shellutil/, ctxutil/
```

The README's code map says what each package owns.

---

## Dependencies

The direct dependencies, from `go.mod`:

```
# CLI and config
github.com/alecthomas/kong                 # CLI framework
github.com/willabides/kongplete            # shell completion for kong
github.com/posener/complete
github.com/ashtonian/pkg/config   # config loading (koanf underneath); to be its own module
github.com/go-playground/validator/v10     # struct validation
gopkg.in/yaml.v3                           # state file, config edits

# Provider APIs
github.com/google/go-github/v88            # GitHub
code.gitea.io/sdk/gitea                    # Gitea / Forgejo
gitlab.com/gitlab-org/api/client-go        # GitLab

# Terminal
charm.land/huh/v2                          # setup wizard and pickers
charm.land/bubbletea/v2                    # git-mgr top
charm.land/lipgloss/v2                     # git-mgr top's styling
golang.org/x/term                          # is this a terminal, and how wide

# Concurrency and the OS
golang.org/x/sync                          # errgroup
golang.org/x/sys                           # the terminal's foreground job, for ws stop

# Logging
log/slog (stdlib)
```

All dependencies pinned to latest stable versions at build time. No go-git.

---

## Build, Versioning & Docker

### Go Version

Use latest stable Go (currently 1.24+). Set in `go.mod` and Dockerfile.

### Version Embedding

Version is injected at build time via ldflags. No hardcoded version strings.

```go
// main.go
var (
    version = "dev"
    commit  = "none"
    date    = "unknown"
)
```

Build: `go build -ldflags="-s -w -X main.version=$VERSION -X main.commit=$COMMIT -X main.date=$DATE"`

### Dockerfile (Multi-Arch)

```dockerfile
# syntax=docker/dockerfile:1.4
FROM --platform=$BUILDPLATFORM golang:1.24-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .

ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=none
RUN CGO_ENABLED=0 GOOS=linux GOARCH=$TARGETARCH go build \
    -ldflags="-s -w -X main.version=$VERSION -X main.commit=$COMMIT -X main.date=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    -o git-mgr .

FROM alpine:3.21
RUN apk add --no-cache git openssh-client
COPY --from=builder /app/git-mgr /usr/local/bin/git-mgr
ENTRYPOINT ["git-mgr"]
```

Key: Alpine final image includes `git` and `openssh-client` (required for SSH clone/fetch).

### GitHub Actions CI/CD

```yaml
name: Build & Push

on:
  push:
    branches: [main]
    tags: ['v*']
  pull_requests:
    branches: [main]
  workflow_dispatch:
    inputs:
      version:
        description: 'Version override'
        required: false

jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0          # Full history for version calculation

      - name: Set up Docker Buildx
        uses: docker/setup-buildx-action@v3

      - name: Login to DockerHub
        if: github.event_name != 'pull_request'
        uses: docker/login-action@v3
        with:
          username: ${{ secrets.DOCKERHUB_USERNAME }}
          password: ${{ secrets.DOCKERHUB_TOKEN }}

      - name: Calculate version
        id: version
        run: |
          if [ -n "${{ github.event.inputs.version }}" ]; then
            echo "version=${{ github.event.inputs.version }}" >> $GITHUB_OUTPUT
          elif [[ "$GITHUB_REF" == refs/tags/v* ]]; then
            echo "version=${GITHUB_REF#refs/tags/v}" >> $GITHUB_OUTPUT
          else
            LATEST_TAG=$(git describe --tags --abbrev=0 2>/dev/null || echo "v0.0.0")
            MAJOR=$(echo $LATEST_TAG | sed 's/v//' | cut -d. -f1)
            COMMITS=$(git rev-list --count HEAD)
            echo "version=${MAJOR}.${COMMITS}" >> $GITHUB_OUTPUT
          fi

      - name: Build and push
        uses: docker/build-push-action@v6
        with:
          context: .
          platforms: linux/amd64,linux/arm64
          push: ${{ github.event_name != 'pull_request' }}
          tags: |
            ashtonian/git-mgr:latest
            ashtonian/git-mgr:${{ steps.version.outputs.version }}
          build-args: |
            VERSION=${{ steps.version.outputs.version }}
            COMMIT=${{ github.sha }}
          cache-from: type=gha
          cache-to: type=gha,mode=max
```

**Version strategy**:
- Tagged push (`v1.2.3`): uses tag as version
- Main branch push: `MAJOR.COMMIT_COUNT` (auto-incrementing)
- Manual dispatch: explicit version input
- PR: build only, no push

---

## Cron Integration

```cron
*/15 * * * * /usr/local/bin/git-mgr sync --quiet 2>> ~/.local/share/git-mgr/git-mgr.log
```

The tool is cron-safe because:
1. **flock** prevents concurrent execution -- overlapping runs exit cleanly
2. **min_fetch_interval** makes frequent runs cheap
3. **auto_discover** picks up new repos without a separate command
4. **Non-zero exit code** only on errors (3 for a partial failure), zero on "already running" skip
5. **--quiet** suppresses normal output
6. **Notifications** (optional) fire OS toast on errors so you don't need to watch logs

---

## Error Handling Strategy

1. **Per-repo errors don't abort the run** -- collect all, continue with remaining
2. **Network errors retry** with exponential backoff (configurable)
3. **Lock conflicts skip gracefully** -- log warning, move to next repo
4. **State file lock conflict**: `sync` exits 0 with a message to stderr; every other command exits 75
5. **Exit codes**:

| Code | Meaning |
| --- | --- |
| 0 | Everything succeeded. `sync` also exits 0 when another run holds the lock, since overlapping scheduled runs are routine. |
| 1 | The command failed: bad config, unreadable state, every target failed, or it has no `--json` output or needs `--yes`. |
| 2 | A check ran and found problems: `doctor`, `ws check`. |
| 3 | Partial failure: some repos, copies or changes failed and the rest succeeded. Each failure is printed to stderr. |
| 75 | Another git-mgr run holds the lock; try again (EX_TEMPFAIL). |
| 80 | Usage error: unknown command or flag. |

   `--json` (global) prints one document on stdout: `main` keeps the real
   stdout for `Context.Emit` and points `os.Stdout` at stderr (and sets
   `--quiet`), so nothing else reaches it. Commands end with
   `ctx.finish(doc, err)`, printing doc and returning err for the exit code;
   plan-applying commands share the change log `noteChanges` fills
   (`{changes: [action + ok/skipped/info/error], drift, blocked, errors}`);
   composite commands run steps through `captured`, which returns each step's
   document for one outer document. Commands without a result refuse
   `--json`; ones that ask first need `--yes`. `doctor` in a schedule's
   environment is run with `--json` and its checks decoded.
6. **All errors include repo context** -- `fmt.Errorf("fetching %s: %w", repo.FullName, err)`

---

## Security Considerations

1. **SSH keys** -- per-provider + per-rule key paths support multi-org setups
2. **Token resolution** -- supports `token_command` for credential managers (1Password, Vault, etc.), never requires tokens in config file. Per-provider `token_env` for isolated env vars.
3. **No HTTPS** -- SSH only, avoids credential storage issues with 2FA
4. **State file permissions** -- created with 0600 (contains repo metadata but no secrets)
5. **Config file permissions** -- created with 0600 (may reference key paths)
6. **Patch dir permissions** -- created with 0700 (contains code diffs)
7. **No secrets in state** -- tokens resolved at runtime, never persisted

---

## Workspaces

The workspace subsystem (`internal/workspace`, commands `workspace`, aliases
`ws` and `wg`) manages repos worked on by several agents at once. Unlike
discover/sync/backup (read-only with respect to the working tree), workspace
commands **deliberately mutate** on request — making worktrees and clones and
running commit/pull/push. It depends on the inventory (tracked repos, the
reconcile engine) and never the reverse: inventory code reads workspace records
from state only to keep them consistent (a rename rekeys copies, a move
reconnects them, a repo with copies is never untracked).

### Model

| Concept | What it is |
| --- | --- |
| Workspace | One repo or several related ones (`repos:` keys, globs, local paths, URLs; or `set:`), or a shared folder (`path:`), × `agents`. Its directory is `<root>/<name>`. |
| Replica | One agent's folder: `<dir>/<agent>`; with shared isolation, the shared folder. |
| Copy | One checkout of a repo in a replica: the replica itself when the workspace has one repo, `<replica>/<name>` when it has several (by name, or as much of the key as tells repos apart). Its own copy ID, branch, account config and backup history. |
| Source | The repo's tracked clone, which every copy is made from. |

Agent names come from `agent_name` (`agent-{{.N}}`); branches from `branch`
(`agents/{{.Workspace}}/{{.Agent}}`) and must pass git's ref rules. Workspace
names are letters, digits, `-` and `_`, so they are safe in paths, branches,
refs and terminal session names.

### Isolation

- **worktree** — `git worktree add` from the source: on the existing local
  branch, else tracking origin's branch of that name, else a new branch at
  `base_ref` (default `origin/HEAD`, else `HEAD`).
- **clone** — staged like any clone: `git clone --local --no-checkout --origin
  git-mgr <source>` (objects hardlinked; git copies them across filesystems),
  `origin` added as the upstream URL, origin's refs fetched from the source with
  the refspec on the command line (`+refs/remotes/origin/*:refs/remotes/origin/*`;
  as a remote's refspec it would make git refuse to track origin's branches),
  `origin/HEAD` copied, the branch checked out as for a worktree, and the
  source's checked-out branch dropped. The `git-mgr` remote keeps git's default
  refspec, showing the source's branches as `git-mgr/*`.
- **reference-clone** — the same with `--shared` (alternates); the source gets
  `gc.pruneExpire=never` first.
- **shared** — no copies; agents work in `path`, or the one repo's clone.

A new copy's `post_create` hooks -- the repo's (`hooks`, `accounts[].hooks`,
`overrides[].hooks`, most specific wins), then the workspace's
(`workspaces[].hooks`, else `workspace_defaults.hooks`) -- run after it is
made and its managed config written, as do a new clone's `post_clone` hooks
in the clone action (never on adopt). Each command: `/bin/sh -c` in the
checkout, stdin empty, its own session (no terminal), git's batch
environment plus `GIT_MGR_EVENT`, `GIT_MGR_PATH`, `GIT_MGR_REPO`,
`GIT_MGR_SOURCE`, `GIT_MGR_WORKSPACE`, `GIT_MGR_AGENT`, `GIT_MGR_BRANCH`,
killed with its process group at `hooks.timeout`; output replaces
`<state dir>/hooks/<name>.<event>.log`. The first failure stops the rest and
returns `hook.Error` (command, log path, last 10 lines); the action still
records the checkout and reports the failure with the command that re-runs it.

Every new checkout -- the repo's clone and each copy -- takes the shape the
repo's `clone` config gives, not its source's:

1. With `sparse`, it is made empty (`clone --no-checkout`, `worktree add
   --no-checkout`), `git sparse-checkout set --cone -- <dirs>` is run, and
   only then is it checked out (`checkout`; a copy clone's branch checkout),
   so nothing outside the cone is checked out or, in a partial clone, fetched.
2. With `lfs: skip`, the clone is made with `-c lfs.fetchexclude=*` and every
   checkout step runs with `GIT_LFS_SKIP_SMUDGE=1`, which also covers
   submodules and worktrees of repos cloned before. Otherwise a copy clone
   hardlinks the source's `lfs/objects` before checking out.
3. With `submodules`, the gitlinks the checkout holds (`git ls-files -z -t
   --stage`: mode 160000, not tagged `S`) are checked out with `git submodule
   update --init --recursive [--depth N] [--filter F] -- <paths>`, in place
   after the clone is renamed into its path. `git submodule update` alone
   would clone gitlinks outside the cone. Under `lfs: skip` each submodule
   gets `lfs.fetchexclude=*` too. A failure returns `SubmoduleError`: the
   checkout stays and is recorded, and the action reports the failure, so a
   repo with an unreachable submodule isn't cloned again on every run.

A partial source (a promisor remote, or `clone.filter` set for the repo) only
gets worktrees, and `plan` blocks clones of it: a clone's fetches through it
fail, since upload-pack refuses lazy fetches. A shallow source is
cloned through git's local transport (git ignores `--local`). When a source
moves, the `move` action repairs its worktrees and repoints its clones'
`git-mgr` remotes and alternates; `ws fetch` and a refresh read the source's
path from state, never from the remote.

### Desired state vs recorded

`workspace.Compute` plans tracked repos (with `Scope.Repos`) and workspaces:
declared ones, ones given on the command line (`Scope.Extra`, recorded as
undeclared), and recorded undeclared ones, kept as they are. It adds an
`adopt`/`clone` for each untracked path or URL a workspace names, plans repos,
then plans workspaces against the state as it will be. Recorded copies match
wanted ones by agent and repo, or by path for a copy with no repo, judged on
disk by copy ID, else (worktree) a common git dir equal to the source's, else
(clone) a `git-mgr` remote at the source or an origin of the repo.

### Health

`ws check` flags (and exits non-zero on) missing copies, a wrong branch, an
in-progress rebase/merge/cherry-pick, a git lock, or being behind the upstream
or, for a branch without one, what it started from. Uncommitted and untracked
work is reported but isn't a failure: agents are expected to have WIP.

### Runs and leases

`ws run <workspace> <agent>` is one agent's run, in the terminal it is called
from:

1. Check the agent's copies and print any issue (`--no-check` skips it).
2. Take the lease `<state dir>/runs/<workspace>/<agent>.lease`: an exclusive
   non-blocking flock, holding a JSON record of the run (pid, profile, command,
   launcher, terminal session). Held by another run, refuse. The kernel drops
   the lock when the holder exits, so a held lease always means a live run.
3. Record the run in the replica's `run:` (profile, command, pid, start time,
   and the terminal: launcher from `GIT_MGR_LAUNCHER`, else tmux, Muxy or iTerm
   as their variables show; session from `GIT_MGR_SESSION`, else `TMUX_PANE`,
   `MUXY_SESSION_ID`, `ITERM_SESSION_ID` or `TERM_SESSION_ID`; for tmux the
   server's socket, from `$TMUX`), so `git-mgr top` can read its activity.
4. Run `$SHELL -i -c <command>` in the replica, with the profile's `env` and
   then the agent's environment (below). The command is the
   profile's `resume` when the replica has a run on record (unless `--fresh`).
   The interactive shell takes the terminal's foreground process group, so
   Ctrl-C and Ctrl-\ reach the agent; the run catches (never ignores, which
   the agent would inherit) SIGINT and SIGQUIT so they don't end it first, and
   on SIGTERM or SIGHUP hangs up: SIGHUP to the terminal's foreground group
   (the agent's job) and to the shell, which doesn't pass it on itself.
5. Record the end and exit status (128+n for signal n), release the lease, and
   exec an interactive shell in the replica, with the same environment
   (`--no-shell` exits instead).

While the agent runs in a tmux pane, a watcher looks at the pane every 2s
(`activity.Watch` over the tracker `top` uses, with its own state handle): 5s
or more of output, then none for `notify.waiting_after`, records `waiting`
("its pane has been quiet for 30s") unless a wait is already recorded; output
again clears it. `ws waiting` -- for agents' own hooks -- takes the workspace
and agent from `--workspace`/`--agent` or `GIT_MGR_WORKSPACE`/`GIT_MGR_AGENT`
(none: exit 0, silently), the message from `--message`, a JSON argument
(Codex: `type: agent-turn-complete`, `last-assistant-message`) or JSON on
stdin, read for at most 2s (Claude Code: `message`, or `hook_event_name:
Stop` with `last_assistant_message`), records it on the running run under a lock wait of at most 5s, and
notifies under `notify.enabled` and `notify.on_waiting` unless the same run
reported a wait within 10s. `--clear` zeroes it. It always exits 0 and
prints nothing on stdout: a hook must never fail its agent, and Claude Code
reads a hook's stdout (a UserPromptSubmit hook's into the prompt). Waiting is an attribute of a running agent, not a
state of its own, so launchers and plugins that look for `running` see it.

### Agent environment

Every command run as an agent -- its run and the shell left after it, `ws
exec`, `post_create` hooks, `ws env` -- gets `workspace.AgentEnv`:
`GIT_MGR_WORKSPACE`, `GIT_MGR_AGENT`, `GIT_MGR_AGENT_N` (its place among the
names `agent_name` gives the workspace, else among its agents by name),
`GIT_MGR_REPLICA`, `GIT_MGR_PORT` and `GIT_MGR_PORTS` when it has ports,
`GIT_MGR_CACHE`; then the workspace's `env` (workspace_defaults' with the
workspace's own added name by name) rendered as Go templates of the same
(`{{add .Port 1}}`), sorted by name. Config validation renders every template
for a sample agent and rejects names that aren't variable names or start with
`GIT_MGR_`.

Ports: an agent without a block gets one under the state lock -- the lowest
`count` ports from `base` that overlap no recorded agent's block and that
nothing is listening on (a bind to each) -- recorded on its replica and kept
for the replica's life; a replica removed frees its block. `ws exec` gives its
agents their environment before running anything, since giving ports writes
the state. The shared cache is `<cache_root>/<workspace>`, made on first use
and deleted by `ws rm --delete` and by `remove-workspace` on apply.

`ws stop` reads the holder's pid from the lease, sends SIGTERM, and waits up to
`--timeout` for the lease to be released; with `--kill`, it then SIGKILLs the
holder and every descendant (from `ps`). It records `stop_requested`, so the
agent shows as `stopping` meanwhile. A copy is never removed while its agent
holds the lease, and a removed replica's lease file is deleted.

Agent states, from the lease and the record: `running`, `stopping`, `missing`
(a copy gone from disk), `stopped` (a run on record), `ready`. Logs are the
terminal's: an agent's TUI output isn't a useful log, and tmux, iTerm and Muxy
keep scrollback.

### Fleets and launchers

A fleet (`fleets:`) is workspaces opened side by side with one launcher;
`ws open` is a fleet of one. `fleet open`:

1. Without `host:`, makes what is missing of the declared workspaces, as
   `ws ensure` does (`--no-ensure` skips it).
2. Makes a test signature (`gitops.TestSign`, no terminal, 20 s) for each
   account with `signing.mode: required` covering a repo the workspaces have
   copies of, and refuses to open if one fails.
3. Builds the launch plan (`launch.Build`): per workspace its directory, and
   per agent its replica, title (`launchers.title`), profile, state, and
   command: `<git-mgr> --config <c> --state <s> ws run <ws> <agent>
   [--profile p | --command c] [--fresh] [--no-check] [--no-shell]`, quoted
   only where a shell needs it. With `host:`, the plan comes from git-mgr on
   the host instead: `ssh -o BatchMode=yes <host> <remote_command> fleet plan
   <name> --json [...]`, over a shared ssh connection (ControlMaster) that the
   launcher's later commands reuse.
4. Opens it with the launcher (`launch.New`): `auto` picks tmux inside tmux
   (`$TMUX`), Muxy inside Muxy (`$MUXY_SESSION_ID`), iTerm inside iTerm, else
   tmux on PATH.
5. Records the fleet's last open (launcher, time, Muxy projects) under
   `fleets:` in state.

The plan is the plugin protocol: `git-mgr-launch-<name>` on PATH gets it as
JSON on stdin, with `options` from `launchers.plugins.<name>`.

| Launcher | Opens | How |
| --- | --- | --- |
| tmux | A session per fleet, a window per workspace (or agent), a pane per agent, spilling past `max_panes` into `<ws>-2`… | `new-session`/`new-window`/`split-window -e GIT_MGR_LAUNCHER=tmux -c <replica> <command>`; each pane gets the option `@git-mgr-agent=<ws>/<agent>`, its title (`select-pane -T`, `allow-set-title off`) and `remain-on-exit`. An existing session is attached, filled with the agents it lacks (found by that option), or rebuilt (`on_exists`). Attach replaces git-mgr (`tmux attach`, `-CC` in iTerm under `control_mode: auto`), or switches the client inside tmux; with `host:`, both run through `ssh -t`. |
| iTerm | A window, a tab per workspace (or agent), a split per agent | One AppleScript through `osascript`: create the window (or a tab in the current one), split each tab in √n columns read left to right (or `columns`/`rows`), name each session, wait `type_delay`, write the badge escape (`OSC 1337 SetBadgeFormat`) and the command, through `ssh -t <host>` for a remote fleet. |
| Muxy | A project per workspace, a tab per agent | `muxy [--host h] project add <dir> --name <n> --reuse --json`, `project set-color`, then by `open`: `layout` writes `.muxy/layouts/<name>.json` (`{"tabs": [{"name", "command"}]}`) through `muxy exec`; `sessions` runs `session create <project> --directory <replica> -- <command>` for each agent not running; `state` waits for the app to list the projects, quits it (asking under `restart: ask`), adds tabs to projects with none in `~/Library/Application Support/Muxy 2/desktop-state.json` (format 3 only; each pane's command in `startup_commands`), saves the old file beside it, relaunches the app and waits for the sessions. `prune_projects` deletes the projects recorded for workspaces no longer in the fleet. |
| plugin | Whatever it does | The plan on stdin; its exit status is the launch's. |

### Publish

`ws publish` pushes each selected copy's branch (`git push -u origin
<branch>`, with the repo's SSH key) and records `published`. With `--pr` it
opens a request per copy: `gh pr create --repo [host/]owner/name --head
<branch> --base <base> --title --body [--draft]` for a GitHub host,
`glab mr create --repo owner/name --source-branch --target-branch --title
--description --yes [--draft]` for a GitLab host, the host's kind and token
coming from the provider whose rule discovered the repo (else the provider on
its host): `GH_TOKEN`/`GH_HOST` or `GITLAB_TOKEN`/`GITLAB_HOST`. An open
request is found (the URL in gh's error, or `gh pr view`/`glab mr view`)
rather than duplicated. The base defaults to origin's default branch, the
title to the branch's last commit subject, the body to `git log --format='- %s'
origin/<base>..<branch>`. Other hosts get the compare URL. The URL is
recorded as the copy's `pr`.

### Retire

`workspace.Retire` is the one way a copy is deleted -- `ws retire`, `ws rm
--delete`, `ws prune`, `remove-copy` and `remove-workspace` on apply:

1. Take the agent's lease (`git-mgr retire` as its holder), so no `ws run`
   starts. Held by a run: with `--stop`, `agent.Stop` it and take the lease;
   otherwise keep every copy.
2. Per copy, `RemoveCopy`: `gitops.InspectUniqueWork` (working tree, index,
   untracked files, stashes, commits no remote has, detached commits, an
   operation in progress, and each checked-out submodule from `git submodule
   status --recursive`); if anything would be lost, archive it (a verified
   bundle, one per submodule with work, the clone's `lfs/objects` hardlinked
   or copied to `<id>.lfs`, and `<id>.json` describing it all); then `git
   worktree remove` or delete the clone. Any failure keeps the copy.
3. Return a tombstone per copy removed and a catalog entry per archive; the
   caller drops the copies and records both (`State.Bury`, keeping the newest
   500 tombstones), then removes empty directories and the lease files of
   replicas that are gone.

`ws retire` first records `retiring` on each agent; it is cleared with the
replica once every copy is gone, and an agent left with copies shows as
`retiring`.

### WIP safety

- **`ws backup`** snapshots every copy's uncommitted work, and the scheduled
  `sync` backs copies up too; `--offsite` also takes them offsite under the
  repo's, then the workspace's, `remote_sync`.
- **`ws recover`** restores a copy's latest backup (`--into`, `--apply`).
- **`ws rm`** only forgets the workspace unless `--delete` is given. With
  `--delete` each copy's unique work (changed and untracked files, detached
  commits, and for clones also stashes and unpushed commits) is archived as a
  verified git bundle in `backup.archive_dir` before the copy is removed;
  `--force` discards it instead. A copy that can't be inspected, archived or
  removed stays on disk and in state. **`ws prune`** removes only copies a full
  inspection finds free of unique work. **`ws pull --backup-first`** (default
  on) snapshots dirty copies first.
- A tracked repo with workspace copies, or with linked worktrees, is never
  untracked or deleted (`remove`, stale cleanup) until they are gone.
- Every archive -- of a retired copy, of a repo `remove --delete` or stale
  cleanup deleted, or of a clone `remove --archive` or `cleanup.policy:
  archive` moved whole -- is catalogued under `archives:` in state; `git-mgr
  archives ls|show` lists them and prints their restore steps, `archives
  restore` moves an archived clone back, `archives rm` deletes archives.
