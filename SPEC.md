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

- The discover/sync/backup subsystem is read-only with respect to your working tree and branches -- it never `git pull`s, merges, or commits. (The opt-in **workgroup** subsystem is separate and *does* mutate on request; see [Workgroups](#workgroups).)
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

1. **`git stash create` is not supported** -- this is the core of our backup strategy
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
| `--concurrency` / `-j` | 8 | Max concurrent git operations |

### Commands

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

Run discovery rules and show what repos would be added/removed.

| Flag | Description |
|---|---|
| `--apply` | Actually add discovered repos to state and clone them (default: preview only) |
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

Use --apply to clone new repos and update state.
```

#### `git-mgr sync [flags]`

Fetch all tracked repos and backup uncommitted changes. This is the primary command for cron usage.

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
- Acquires exclusive flock on state file -- if another instance is running, exits immediately with code 0
- Respects `min_fetch_interval` -- skips repos fetched within the window
- All errors are collected, not fatal -- partial success is fine

#### `git-mgr backup [flags]`

Run only the backup step (no fetch).

| Flag | Description |
|---|---|
| `--repos <glob>` | Filter to specific repos |
| `--tag <tag>` | Filter by tag |
| `--list` | List existing backups instead of creating new ones |
| `--restore <ref-or-timestamp>` | Show restore instructions for a specific backup |

```
$ git-mgr backup --list --repos */git-mgr
github.com/ashtonian/git-mgr:
  2026-04-17T10:30:00Z  ref:refs/git-mgr/backups/20260417T103000Z  patch:~/.local/share/git-mgr/patches/...
  2026-04-17T10:15:00Z  ref:refs/git-mgr/backups/20260417T101500Z  patch:~/.local/share/git-mgr/patches/...

$ git-mgr backup --restore 20260417T103000Z --repos */git-mgr
To restore this backup:
  # Apply stash ref (staged + unstaged tracked changes):
  cd ~/projects/github.com/ashtonian/git-mgr
  git stash apply refs/git-mgr/backups/20260417T103000Z

  # Or apply patches manually:
  git apply ~/.local/share/git-mgr/patches/.../staged.patch --cached
  git apply ~/.local/share/git-mgr/patches/.../unstaged.patch
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
2. If path doesn't match the configured `path_template`, keep it at its current location (don't move)
3. Add to state with `manually_added: true`
4. `--scan` walks directory tree looking for `.git` dirs, adopts each

#### `git-mgr clone <url> [flags]`

Manually add and clone a single repo.

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
| `--delete` | Also delete the repo from disk |
| `--yes` | Skip confirmation prompt |

#### `git-mgr remote-sync [flags]`

Push backup refs to remotes.

| Flag | Description |
|---|---|
| `--repos <glob>` | Filter |
| `--tag <tag>` | Filter by tag |
| `--dry-run` | Show what would be pushed |

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
| `bootstrap` | One-command machine setup: init (if missing) → doctor → discover --apply → sync → install-agent. `--profile` (tag scope), `--no-agent`, `--dry-run`. |
| `doctor [--fix]` | Offline preflight: git present, config valid, base_dir writable, provider tokens resolve, configured SSH keys exist, state lock free, tracked repos on disk with matching origin. Non-zero exit on failure. |
| `run [--set/--tag/--repos] -- <cmd>` | Run a command in each matching repo (parallel, bounded by concurrency). `--fail-fast`; non-zero exit if any failed. |
| `manifest export [--out]` / `import <file>` | Portable `{url,tags,path}` manifest of the tracked set, to rehydrate a new machine (captures manual adoptions). import is idempotent and clones over the url's transport. |
| `restore <repo> [id]` | Recover a backup: print steps (default), `--into <dir>` (clone+apply to a scratch dir), or `--apply` onto the tree (refuses dirty without `--force`). id defaults to `latest`. |
| `report [--since]` / `log [-n]` | Summarize / list recent activity from the operation log (clone/sync/backup/remove/wg events). |
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

# Named, reusable repo selections for `--set <name>` on status/sync/backup/run/
# remove/remote-sync. In a set: repos globs OR tags (both empty = all), then
# exclude_archived. --set composes (AND) with inline --repos/--tag.
sets:
  - name: core
    repos: ["github.com/acme/*"]
  - name: active-backend
    tags: ["backend"]
    exclude_archived: true

# Named agent commands for `wg open --agent <name>` (each pane runs the command).
agents:
  claude: "claude"

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

  # Timeout for a single repo's fetch operation.
  timeout_per_repo: "5m"

  # Number of retries on transient network failures (per repo).
  retries: 2

  # Retry backoff base duration. Actual delay: base * 2^attempt.
  retry_backoff: "2s"

# ============================================================================
# Cleanup Policy (Stale Repos)
# ============================================================================

# Controls what happens when a previously-discovered repo is no longer found
# by any discovery rule (deleted upstream, removed from org, etc.).
cleanup:
  # Policy: "mark_stale" (default), "remove", "remove_and_delete", "ignore"
  # - mark_stale: set status to "stale" in state. No disk changes. Visible in `git-mgr status --stale`.
  # - remove: remove from state after grace_period. Keep files on disk.
  # - remove_and_delete: remove from state AND delete from disk after grace_period.
  # - ignore: never mark stale, never remove. Manual cleanup only.
  policy: "mark_stale"

  # Grace period before "remove" or "remove_and_delete" policies take effect.
  # Repo must be continuously stale for this duration. Protects against
  # temporary API failures or org permission changes.
  grace_period: "30d"

  # Never auto-cleanup manually-added repos (via `git-mgr clone` or `git-mgr adopt`).
  protect_manual: true

# ============================================================================
# Backup Settings
# ============================================================================

backup:
  # Enable transparent backup of uncommitted changes during sync.
  enabled: true

  # Strategy: "stash-ref", "patch", or "both" (recommended).
  # - stash-ref: `git stash create` + custom ref. Fast, atomic, git-native. Captures staged + unstaged tracked changes.
  # - patch: Generates patch files + tar of untracked files. Captures everything including untracked.
  # - both: Runs both strategies. Maximum safety.
  strategy: "both"

  # Include untracked files in patch backups. Ignored for stash-ref.
  include_untracked: true

  # Max backups to retain per repo. Oldest are pruned first. 0 = unlimited.
  max_per_repo: 10

  # Directory for patch file storage. Supports ~ expansion.
  # Default: ~/.local/share/git-mgr/patches/
  patch_dir: ""

  # Also force-push a snapshot of uncommitted tracked changes to a recoverable
  # branch (refs/heads/git-mgr/wip/<branch>) on push_remote, in addition to the
  # local backup. A normal, visible branch (not the hidden refs/git-mgr/*
  # namespace), so dirty work is saved offsite. Equivalent to the --push-branch
  # flag on `sync`/`backup`. Untracked-only changes are not captured (no commit
  # to push); the working tree is never modified.
  push_branch: false
  push_remote: "origin"

# ============================================================================
# Remote Sync Settings
# ============================================================================

remote_sync:
  # Enable pushing backup refs to remotes.
  enabled: false

  # Remote name to push refs to. Must exist in each repo's git config.
  remote_name: "origin"

  # Only push refs for repos matching this glob. Empty = all repos.
  repos_glob: ""

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

The state file is the source of truth for all managed repos. It is read/written atomically with flock protection.

```yaml
# State file version for future migrations.
version: 1

# Last time any sync operation completed.
last_sync: "2026-04-17T10:30:00Z"

# Map of repos, keyed by canonical identifier (host/owner/name).
repos:
  github.com/ashtonian/git-mgr:
    # --- Identity ---
    clone_url: "git@github.com:ashtonian/git-mgr.git"
    host: "github.com"
    owner: "your-login"
    name: "git-mgr"
    full_name: "ashtonian/git-mgr"
    provider: "github.com"                               # Provider instance name

    # --- Local State ---
    local_path: "/Users/me/repos/github.com/ashtonian/git-mgr"
    cloned: true
    default_branch: "main"
    head_commit: "abc123def456..."
    current_branch: "feat/backup"

    # --- Metadata ---
    discovered_by: "my-github"
    is_fork: false
    is_archived: false
    language: "Go"
    topics: ["cli", "git"]
    tags: ["personal", "oss"]                             # Combined: rule tags + manual tags
    description: "CLI tool for managing git repos"

    # --- Sync State ---
    last_fetch: "2026-04-17T10:30:00Z"
    last_fetch_error: ""
    last_backup: "2026-04-17T10:30:00Z"
    last_backup_ref: "refs/git-mgr/backups/20260417T103000Z"
    last_backup_has_patch: true
    status: "dirty"                # clean, dirty, conflict, error, uncloned, missing, stale
    error: ""

    # --- Discovery State ---
    first_seen: "2026-04-01T00:00:00Z"
    last_discovered: "2026-04-17T10:00:00Z"
    stale_since: ""                # Set when no longer found by any discovery rule
    manually_added: false          # true if added via `git-mgr clone` or `git-mgr adopt`
```

### State File Locking

```
Lock file: <state_file>.lock (e.g., ~/.local/share/git-mgr/state.yaml.lock)
Mechanism: flock(fd, LOCK_EX | LOCK_NB)
On conflict: exit 0 with message "Another git-mgr instance is running, skipping."
```

**Write protocol:**
1. Acquire flock
2. Read current state
3. Perform operations
4. Write state to `<state_file>.tmp`
5. `os.Rename` tmp to state file (atomic on POSIX)
6. Release flock

---

## Transparent Backup: Detailed Algorithm

### Overview

The backup system captures uncommitted work in two complementary ways:

1. **Stash refs** -- `git stash create` produces a commit hash capturing all staged and unstaged changes to tracked files, without modifying working tree, index, or stash reflog. We store this under `refs/git-mgr/backups/<timestamp>`.

2. **Patch files** -- `git diff` output plus a tar of untracked files, stored outside the repo entirely.

Neither mechanism touches the working tree, index, branches, or stash list. Both are invisible to normal git operations.

### Algorithm

```
func Backup(repo RepoState, config BackupConfig) error:
    path := repo.LocalPath

    // ── Pre-flight ──────────────────────────────────────────────
    if !exists(path + "/.git"):
        return nil

    // Skip if any git lock files present (another operation in progress)
    lockFiles := [".git/index.lock", ".git/HEAD.lock", ".git/config.lock",
                  ".git/shallow.lock", ".git/refs/stash.lock"]
    for _, lf := range lockFiles:
        if exists(path + "/" + lf):
            log.Warn("repo %s is locked (%s), skipping backup", repo.Name, lf)
            return nil

    if isBareRepo(path):
        return nil

    // ── Detect working tree state ───────────────────────────────
    hasStagedChanges   := gitExitCode(path, "diff", "--cached", "--quiet") != 0
    hasUnstagedChanges := gitExitCode(path, "diff", "--quiet") != 0
    untrackedFiles     := gitOutput(path, "ls-files", "--others", "--exclude-standard")
    hasUntracked       := len(untrackedFiles) > 0

    if !hasStagedChanges && !hasUnstagedChanges && !hasUntracked:
        repo.Status = "clean"
        return nil

    // ── Detect special states ───────────────────────────────────
    inRebase := exists(path+"/.git/rebase-merge") || exists(path+"/.git/rebase-apply")
    inMerge  := exists(path + "/.git/MERGE_HEAD")
    inCherryPick := exists(path + "/.git/CHERRY_PICK_HEAD")

    timestamp := time.Now().UTC().Format("20060102T150405Z")

    // ── Strategy: stash-ref ─────────────────────────────────────
    if config.Strategy == "stash-ref" || config.Strategy == "both":
        stashOID := gitOutput(path, "stash", "create")
        if stashOID != "":
            refName := "refs/git-mgr/backups/" + timestamp
            gitExec(path, "update-ref", refName, stashOID)
            repo.LastBackupRef = refName

    // ── Strategy: patch ─────────────────────────────────────────
    if config.Strategy == "patch" || config.Strategy == "both":
        patchDir := filepath.Join(config.PatchDir, repo.Host, repo.Owner, repo.Name, timestamp)
        os.MkdirAll(patchDir, 0700)

        if hasStagedChanges:
            patch := gitOutput(path, "diff", "--cached", "--binary", "--no-color")
            writeFile(patchDir+"/staged.patch", patch)

        if hasUnstagedChanges:
            patch := gitOutput(path, "diff", "--binary", "--no-color")
            writeFile(patchDir+"/unstaged.patch", patch)

        if hasUntracked && config.IncludeUntracked:
            files := strings.Split(untrackedFiles, "\n")
            files = filterBySize(path, files, 50*1024*1024) // skip >50MB
            if len(files) > 0:
                tarExec(path, patchDir+"/untracked.tar.gz", files)
                writeFile(patchDir+"/untracked.list", strings.Join(files, "\n"))

        metadata := BackupMetadata{
            HeadCommit: gitOutput(path, "rev-parse", "HEAD"),
            CurrentBranch: gitOutput(path, "rev-parse", "--abbrev-ref", "HEAD"),
            Timestamp: timestamp,
            InRebase: inRebase, InMerge: inMerge, InCherryPick: inCherryPick,
            HasStaged: hasStagedChanges, HasUnstaged: hasUnstagedChanges,
            UntrackedCount: len(files),
        }
        writeJSON(patchDir+"/metadata.json", metadata)
        repo.LastBackupHasPatch = true

    // ── Prune old backups ───────────────────────────────────────
    if config.MaxPerRepo > 0:
        pruneStashRefs(path, "refs/git-mgr/backups/", config.MaxPerRepo)
        prunePatchDirs(config.PatchDir, repo, config.MaxPerRepo)

    repo.LastBackup = timestamp
    repo.Status = "dirty"
    return nil
```

### Stash Ref Internals

`git stash create` creates a special merge commit:

```
       W (working tree state) <-- returned commit hash
      / \
     I   H (HEAD)
     |
     H
```

- **H** = HEAD commit, **I** = index state, **W** = working tree state
- Stored in object DB but unreferenced until `git update-ref`
- The ref `refs/git-mgr/backups/<timestamp>`:
  - **Not shown by** `git branch -a`, `git tag`, `git stash list`, `git log`, any IDE
  - **Only shown by** `git for-each-ref refs/git-mgr/`
  - **Restoring**: `git stash apply <ref>`

### Patch File Structure

```
~/.local/share/git-mgr/patches/
└── github.com/
    └── your-login/
        └── git-mgr/
            ├── 20260417T103000Z/
            │   ├── metadata.json
            │   ├── staged.patch
            │   ├── unstaged.patch
            │   ├── untracked.tar.gz
            │   └── untracked.list
            └── 20260417T101500Z/
                └── ...
```

### Safety Properties

| Property | Guarantee |
|---|---|
| Working tree modification | NEVER -- all operations are read-only |
| Index modification | NEVER -- `git stash create` reads but doesn't write index |
| Branch modification | NEVER -- no checkout, reset, or commit to any branch |
| Stash list modification | NEVER -- `stash create` != `stash save/push` |
| Concurrent git safety | Skip if any `.git/*.lock` file exists |
| Large file safety | Untracked files > 50MB are excluded from patch tar |
| Atomic ref write | `git update-ref` is atomic |
| Atomic state write | Write to tmp + rename |

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

During discovery, compare newly-discovered repo set against state:

```
func DetectStale(state, discovered):
    for key, repo := range state.Repos:
        if repo.ManuallyAdded: continue              // never stale
        if key in discovered:
            repo.StaleSince = ""                      // clear stale flag
            repo.LastDiscovered = time.Now()
        else:
            if repo.StaleSince == "":
                repo.StaleSince = time.Now()          // newly stale
            repo.Status = "stale"

    // Apply cleanup policy
    if config.Cleanup.Policy == "remove" || config.Cleanup.Policy == "remove_and_delete":
        for key, repo := range state.Repos:
            if repo.Status == "stale" && time.Since(repo.StaleSince) > config.Cleanup.GracePeriod:
                if config.Cleanup.Policy == "remove_and_delete":
                    os.RemoveAll(repo.LocalPath)
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
├── main.go                           # Entry point
├── go.mod                            # Go 1.24+ (latest stable)
├── go.sum
├── Dockerfile                        # Multi-arch build (amd64 + arm64)
├── .github/
│   └── workflows/
│       └── docker.yml                # Build + push multi-arch Docker image
├── SPEC.md                           # This file
│
├── internal/
│   ├── cmd/                          # Kong CLI command definitions
│   │   ├── root.go                   # CLI struct, global flags
│   │   ├── init.go                   # `init` command
│   │   ├── discover.go               # `discover` command
│   │   ├── sync.go                   # `sync` command
│   │   ├── backup.go                 # `backup` command
│   │   ├── status.go                 # `status` command
│   │   ├── clone.go                  # `clone` command
│   │   ├── adopt.go                  # `adopt` command
│   │   ├── remove.go                 # `remove` command
│   │   ├── remote_sync.go            # `remote-sync` command
│   │   ├── config.go                 # `config` command
│   │   └── version.go                # `version` command
│   │
│   ├── config/                       # Configuration (koanf-based)
│   │   ├── config.go                 # Struct definitions, Load(), expandHome()
│   │   ├── defaults.go               # Default values, XDG path resolution
│   │   ├── resolve.go                # ResolveForRepo() -- per-repo override merging
│   │   └── validate.go               # Config validation (go-playground/validator)
│   │
│   ├── state/                        # State persistence
│   │   ├── state.go                  # State + RepoState struct definitions
│   │   ├── store.go                  # Load(), Save() with flock + atomic write
│   │   └── lock.go                   # File locking (flock wrapper)
│   │
│   ├── discovery/                    # Repo discovery
│   │   ├── provider.go               # Provider interface + Repo type
│   │   ├── github.go                 # GitHub/GHE implementation (go-github)
│   │   ├── gitlab.go                 # GitLab implementation (official SDK)
│   │   ├── filter.go                 # Include/exclude filtering
│   │   └── stale.go                  # Stale repo detection + cleanup policy
│   │
│   ├── gitops/                       # Git CLI operations
│   │   ├── exec.go                   # Git command execution wrapper
│   │   ├── clone.go                  # Clone with SSH key support
│   │   ├── fetch.go                  # Fetch with lock checking
│   │   ├── backup.go                 # Transparent backup algorithm
│   │   ├── refs.go                   # Custom ref CRUD + pruning
│   │   ├── lock.go                   # Git lock file detection
│   │   ├── status.go                 # Working tree status detection
│   │   └── adopt.go                  # Parse remote URL, detect host/owner/name
│   │
│   ├── sync/                         # Sync orchestration
│   │   └── sync.go                   # Concurrent sync loop with errgroup
│   │
│   ├── remote/                       # Remote sync backends
│   │   ├── remote.go                 # Interface definition
│   │   └── git_push.go               # Push refs to remote
│   │
│   ├── pathtemplate/                  # Path template resolution
│   │   └── template.go
│   │
│   ├── notify/                        # OS notifications
│   │   └── notify.go                  # osascript (macOS) / notify-send (Linux)
│   │
│   └── ui/                           # Terminal output
│       ├── table.go                  # Status table formatting
│       ├── progress.go               # Progress display for sync/clone
│       └── logger.go                 # slog setup (JSON/text, file output)
```

---

## Dependencies

```
# CLI & Config
github.com/alecthomas/kong          # CLI framework (struct-based, clean subcommands)
github.com/knadh/koanf/v2           # Config loading (YAML + env + CLI flag layering)
github.com/knadh/koanf/parsers/yaml
github.com/knadh/koanf/providers/file
github.com/knadh/koanf/providers/env
github.com/go-playground/validator/v10  # Struct validation

# Provider APIs
github.com/google/go-github/v88     # GitHub API (official, Google-maintained)
code.gitea.io/sdk/gitea             # Gitea/Forgejo API (official)
gitlab.com/gitlab-org/api/client-go  # GitLab API (official, GitLab-maintained)
golang.org/x/oauth2                  # Token-based HTTP auth

# Concurrency
golang.org/x/sync/errgroup          # Bounded concurrency

# State
gopkg.in/yaml.v3                    # State file YAML

# UI
github.com/olekukonez/tablewriter   # Status table rendering

# Logging
log/slog (stdlib)                    # Structured logging
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
4. **Non-zero exit code** only on errors, zero on "already running" skip
5. **--quiet** suppresses normal output
6. **Notifications** (optional) fire OS toast on errors so you don't need to watch logs

---

## Error Handling Strategy

1. **Per-repo errors don't abort the run** -- collect all, continue with remaining
2. **Network errors retry** with exponential backoff (configurable)
3. **Lock conflicts skip gracefully** -- log warning, move to next repo
4. **State file lock conflict exits cleanly** -- code 0, message to stderr
5. **Exit codes**: 0 = success (or skipped), 1 = some repos failed, 2 = fatal (bad config)
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

## Workgroups

The workgroup subsystem manages a parent directory of *N* member working
directories operated on as a unit. Unlike discover/sync/backup (read-only with
respect to the working tree), workgroup commands **deliberately mutate** on
request — creating worktrees/clones and running commit/pull/push. It is a
distinct, opt-in subsystem with its own command family (`workgroup`, alias `wg`).

### Kinds

- **clone** (the default kind) — N independent clones (distinct repos), e.g. a
  microservice bundle, or N isolated agent workspaces on one repo. Members are
  listed explicitly, selected from tracked repos via `from_tag`, or expanded
  from a single repo with `--count`. "In sync" = present, on a branch, not
  behind upstream.
- **worktree** — N `git worktree`s of a single source repo, each on its own
  branch (shared object store; cheap; pass `--source`). For running N agents on
  one project in isolation. "In sync" = present, on the expected branch, not
  behind base.

A *member* is the unifying abstraction: a working directory with a path,
branch, and optional upstream. Group operations (status/check/fetch/commit/
pull/push/exec) treat members identically; only creation and the "in sync"
predicate differ by kind.

### Desired state vs observed state

- **config** declares workgroups (`workgroups:` + `workgroup_base_dir`) — the
  reproducible, machine-portable source of truth. A workgroup may also set
  `sync: true` to register its underlying repo(s) as tracked, so the read-only
  `git-mgr sync` engine keeps them fetched (fetching a worktree source is safe —
  the object store is shared).
- **state** records what has been materialised (the `workgroups:` map) — the
  operational source of truth for group commands.
- `workgroup ensure` reconciles disk to the declared state (creating missing
  members, pruning stale worktree registrations); `workgroup check` asserts
  health and exits non-zero on drift. Together they make a machine restart a
  non-event: re-running `ensure` + `open` deterministically rebuilds everything.

### Creating from the CLI

`workgroup create <name>` builds a workgroup two ways: with no defining flags it
looks `<name>` up in config (the declarative path); with
`--repo`/`--from-tag`/`--source`/`--count`/`--member` it **synthesizes** a
`config.Workgroup` from the CLI as a one-off — no config edit required. **Clone
is the default kind**; `--source` (worktree-specific) selects worktree, or pass
`--kind` explicitly. A clone group takes members from `--repo`/`--member` (repo
URLs or tracked keys) or `--from-tag`, and `--repo R --count N` expands to an
N-clone fleet `agent-1..N` of one repo. A worktree group expands `--count N`
into members `agent-1..N` with branches namespaced `<name>/agent-N` (so distinct
groups off one source never collide); `--member name[:branch]` sets members
explicitly. Both paths converge on the same `Spec` → `ensure` materialisation,
so a flag-built group is indistinguishable from a declared one.

- `--save` appends the synthesized workgroup back to the config file via a
  comment-preserving YAML round-trip (the config library is load-only, so this
  is a `yaml.v3` Node edit, written atomically and re-validated); a name
  conflict errors unless `--force` overwrites the existing entry.
- `--sync` registers the group's underlying repo(s) as tracked at create time
  (tagged `workgroup:<name>`), warning on any whose remote can't be parsed.

### Health / "in sync"

`workgroup check` flags (and exits non-zero on) hard problems that need a human:
missing members, wrong branch (worktree), an in-progress rebase/merge/cherry-pick,
a present git lock, or being behind upstream/base. Uncommitted/untracked work is
reported but not treated as failure — agents are expected to have WIP.

### Terminal integration

`workgroup open` builds a tmux session `wg-<name>` (one pane per member, each
running `wg check` on startup). Under iTerm it attaches via control mode
(`tmux -CC`) so panes are native iTerm splits backed by a restart-surviving tmux
session; `--print` emits the script, `--recreate` rebuilds, `--no-attach` builds
detached (useful from a login agent). `--cmd <command>` runs a command in each
pane before the shell, and `--agent <name>` resolves it from config `agents:` --
so `wg create fleet --repo R --count 4 --open --agent claude` spins up four
isolated workspaces each running an agent.

### Agent lifecycle / WIP safety

The workgroup subsystem treats agent WIP as precious:

- **`wg backup`** snapshots every member's uncommitted work (and the scheduled
  `sync` backs up workgroup members too, so the periodic agent protects them).
  `--push-branch` also force-pushes the snapshot to a recoverable
  `git-mgr/wip/<branch>` on the remote.
- **`wg recover`** restores a member's latest backup (`--into <dir>` for safe
  inspection, `--apply` onto the worktree), completing the backup→recover loop.
- **`wg rm`** backs up dirty members before tearing them down (unless `--force`),
  and **`wg prune`** reaps only members with no unique work (clean and ahead==0
  vs upstream), removing an empty workgroup from state. **`wg pull --backup-first`**
  (default on) snapshots dirty members before a possibly-rebasing pull.
- A workgroup can be built over a **named set** (`wg create --from-set <name>`):
  an agent fleet across a tag/glob-selected set of tracked repos.
