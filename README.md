# git-mgr

Discover, clone, sync, and back up git repositories across multiple GitHub and GitLab instances.

## What it does

- **Discovers** repos from GitHub, GitHub Enterprise, and GitLab based on configurable rules
- **Clones** into a structured directory layout (`github.com/owner/repo`)
- **Fetches** all tracked repos periodically (cron-safe, never pulls/merges)
- **Backs up** uncommitted changes transparently without touching your working tree
- **Tracks** state across runs for incremental sync

Designed to run as a cron job every 15 minutes. Also works interactively for bootstrapping a new machine.

## Install

```bash
# From source
go install github.com/ashtonian/git-mgr@latest

# Docker
docker pull ashtonian/git-mgr
```

## Quick Start

```bash
# Create config
git-mgr init

# Edit config to add providers and discovery rules
git-mgr config --edit

# Discover repos (dry-run)
git-mgr discover

# Clone discovered repos
git-mgr discover --apply

# Fetch all + backup uncommitted work
git-mgr sync

# See what's tracked
git-mgr status
```

## Commands

| Command | Description |
|---|---|
| `git-mgr init` | Create config file with defaults |
| `git-mgr discover` | Discover repos from providers (dry-run by default, `--apply` to clone) |
| `git-mgr sync` | Fetch all repos + backup uncommitted changes (the cron command) |
| `git-mgr backup` | Backup only (no fetch). `--list` to see backups, `--restore` for instructions |
| `git-mgr status` | Table of all managed repos. `--json`, `--dirty`, `--stale`, `--tag` filters |
| `git-mgr clone <url>` | Manually clone and track a single repo |
| `git-mgr adopt <path>` | Import existing clone into management. `--scan <dir>` for batch import |
| `git-mgr remove <pattern>` | Stop tracking repos. `--delete` to remove from disk |
| `git-mgr remote-sync` | Push backup refs to remotes |
| `git-mgr config` | Print resolved config. `--validate`, `--edit`, `--for-repo <key>` |
| `git-mgr version` | Print version info |
| `git-mgr workgroup` (`wg`) | Manage [workgroups](#workgroups): parallel worktrees or multi-repo bundles |

## Configuration

Config lives at `~/.config/git-mgr/config.yaml`. See [`config.example.yaml`](config.example.yaml) for a fully annotated example.

### Multiple Providers

Supports any number of GitHub and GitLab instances:

```yaml
providers:
  - name: "github.com"
    type: "github"
    token_command: "gh auth token"

  - name: "ghes.company.com"
    type: "github"
    url: "https://ghes.company.com"
    token_env: "GHES_TOKEN"
    ssh_key: "~/.ssh/id_work"

  - name: "gitlab.company.com"
    type: "gitlab"
    url: "https://gitlab.company.com"
    token_env: "GITLAB_COMPANY_TOKEN"
```

### Discovery Rules

Discover repos by user, org, group, language, topic, or name pattern:

```yaml
discovery:
  - name: "personal"
    provider: "github.com"
    user: "your-login"
    tags: ["personal"]

  - name: "work-services"
    provider: "ghes.company.com"
    org: "platform"
    name_pattern: "^svc-"
    tags: ["work", "services"]
```

### Manual Repos

Track individual repos without discovery rules:

```yaml
repos:
  - url: "git@github.com:someone/useful-fork.git"
    tags: ["libs"]
  - url: "git@gitlab.com:other-org/special-project.git"
    tags: ["work"]
```

### Per-Repo Overrides

Override any setting per repo or glob pattern:

```yaml
overrides:
  - repos: ["ghes.company.com/*"]
    ssh_key: "~/.ssh/id_work"
    backup:
      strategy: "patch"
    remote_sync:
      enabled: false

  - repos: ["github.com/your-login/dotfiles"]
    path_template: "{{.Name}}"
```

### Tags

Tags are assigned by discovery rules or manually. Filter any command with `--tag`:

```bash
git-mgr status --tag work
git-mgr sync --tag personal
git-mgr backup --list --tag services
```

## Transparent Backup

The backup system saves uncommitted work without modifying your working tree, branches, or stash list. Two complementary strategies:

### Stash Refs

`git stash create` produces a commit hash without side effects. We store it under `refs/git-mgr/backups/<timestamp>` -- invisible to `git branch`, `git stash list`, `git log`, and all IDEs.

### Patch Files

`git diff` output plus a tar of untracked files, stored outside the repo at `~/.local/share/git-mgr/patches/`.

### Safety Guarantees

- **Never modifies** working tree, index, branches, or stash list
- **Skips locked repos** (if `.git/index.lock` exists, another operation is in progress)
- **Excludes** untracked files > 50MB
- **Auto-prunes** old backups (configurable `max_per_repo`)

### Restore

```bash
# See backups
git-mgr backup --list --repos */my-repo

# Restore from stash ref
cd ~/repos/github.com/owner/my-repo
git stash apply refs/git-mgr/backups/20260417T103000Z

# Or restore from patches
git apply ~/.local/share/git-mgr/patches/.../staged.patch --cached
git apply ~/.local/share/git-mgr/patches/.../unstaged.patch
```

## Cron Setup

```cron
*/15 * * * * /usr/local/bin/git-mgr sync --quiet 2>> ~/.local/share/git-mgr/git-mgr.log
```

Why this is safe:
- **flock** prevents concurrent runs (overlapping cron exits cleanly)
- **min_fetch_interval** skips recently-fetched repos
- **auto_discover** picks up new repos without a separate command
- **Fetch-only** -- never pulls, merges, or modifies your branches
- **Lock detection** -- skips repos with active git operations

## Stale Repos

When a repo is deleted upstream or you're removed from an org, it's detected during discovery:

```yaml
cleanup:
  policy: "mark_stale"      # mark_stale | remove | remove_and_delete | ignore
  grace_period: "720h"      # 30 days before remove/delete takes effect
  protect_manual: true       # Never auto-cleanup manually-added repos
```

## Adopting Existing Repos

Already have repos cloned? Import them:

```bash
# Single repo
git-mgr adopt ~/projects/cool-lib --tag libs

# Scan a directory
git-mgr adopt --scan ~/projects --tag legacy

# Dry-run first
git-mgr adopt --scan ~/projects --dry-run
```

## Workgroups

A **workgroup** is a parent directory holding *N* member working directories you operate on as a unit — built for running several agents (or yourself) in parallel without them stepping on each other. Two kinds:

- **`worktree`** — N git worktrees of a *single* source repo, each on its own branch (shared `.git`, cheap). Run N agents on one project in isolation.
- **`clone`** — N independent repos checked out side by side (e.g. microservices you work on together). Group commands fan out across them.

Members are operated on identically; only creation and "in sync" semantics differ by kind.

### Declare in config

```yaml
workgroup_base_dir: "~/workgroups"           # parent for all workgroups

workgroups:
  - name: "feature-x"                        # -> ~/workgroups/feature-x/{agent-1,agent-2,agent-3}
    kind: "worktree"
    source: "github.com/your-login/myapp"    # a tracked repo key, or a local path
    base_ref: "origin/main"                  # fork point (optional)
    members:
      - { name: "agent-1" }                  # branch defaults to the member name
      - { name: "agent-2" }
      - { name: "agent-3" }

  - name: "platform"                         # a multi-repo bundle
    kind: "clone"
    from_tag: "platform"                     # members = all tracked repos with this tag
```

### Commands

| Command | Description |
|---|---|
| `wg create <name>` | Materialise a configured workgroup (create its members) |
| `wg ensure [name]` | Reconcile workgroup(s) to config — recreate missing members; idempotent |
| `wg adopt <name> <dir>` | Register an existing directory of repos as a workgroup |
| `wg ls` | List workgroups |
| `wg status <name>` | Per-member table: branch, state, upstream, ahead/behind, notes |
| `wg check <name>` | Assert set up + in sync; **non-zero exit** and flags on drift |
| `wg fetch/commit/pull/push <name>` | Group git operations across all members (concurrent) |
| `wg exec <name> -- <cmd>` | Run a command in each member directory |
| `wg open <name>` (`wg tmux`) | Open/attach a tmux session, one pane per member |
| `wg rm <name>` | Tear down (worktrees detached); `--delete` also removes directories |

`commit` skips clean members and stages tracked changes by default (`--add-all` for untracked); `pull` is `--ff-only` by default (`--rebase` to rebase); `push` takes `--set-upstream`.

### Escape hatch

When agents get stuck, drive every member at once:

```bash
git-mgr wg status feature-x                    # who's dirty / behind / mid-rebase?
git-mgr wg commit feature-x -m wip --add-all   # commit everyone's WIP
git-mgr wg exec feature-x -- git rebase --abort
```

### Terminal layout (tmux + iTerm)

`wg open` builds a tmux session `wg-<name>` with one pane per member, each `cd`'d into its directory and running `wg check` on startup so drift is flagged the moment you open it:

```bash
git-mgr wg open feature-x          # create-or-attach; --recreate to rebuild
git-mgr wg open feature-x --print  # print the tmux script instead of running it
```

Under **iTerm** it uses tmux control mode (`tmux -CC`), so panes render as native iTerm splits while the tmux session survives disconnects. After a Mac restart the tmux server is gone — just run the same command again and the identical layout is rebuilt deterministically. Make it one keystroke with an alias, or auto-build at login:

```bash
alias wgx='git-mgr wg open feature-x'
# or, in a login script / launchd agent, build it detached:
#   git-mgr wg open feature-x --no-attach
```

## Docker

```bash
# Run sync in Docker
docker run -v ~/.config/git-mgr:/root/.config/git-mgr \
           -v ~/repos:/root/repos \
           -v ~/.ssh:/root/.ssh:ro \
           ashtonian/git-mgr sync

# Multi-arch: linux/amd64, linux/arm64
```

## Config Debugging

```bash
# Validate config
git-mgr config --validate

# See resolved config for a specific repo
git-mgr config --for-repo github.com/company/api

# Print full resolved config
git-mgr config
```

## Environment Variables

All config fields can be overridden via `GIT_MGR_*` env vars:

| Variable | Overrides |
|---|---|
| `GIT_MGR_CONFIG` | `--config` flag |
| `GIT_MGR_STATE` | `--state` flag |
| `GIT_MGR_BASE_DIR` | `base_dir` |
| `GITHUB_TOKEN` | Default GitHub token |
| `GITLAB_TOKEN` | Default GitLab token |

## Development

```bash
# Build
go build -o git-mgr .

# Unit tests
go test ./...

# E2E tests (requires Docker)
go test -tags=e2e -v ./e2e/

# Build with version info
go build -ldflags="-X main.version=1.0.0 -X main.commit=$(git rev-parse HEAD)" .
```

## License

MIT
