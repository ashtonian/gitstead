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
