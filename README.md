# git-mgr

Discover, clone, sync, and back up git repositories across multiple GitHub,
GitLab, and Gitea/Forgejo instances — into one structured local layout.

git-mgr is built to run two ways: **interactively** when bootstrapping a new
machine, and as a **cron job** that fetches everything and transparently backs
up uncommitted work without ever touching your branches or working tree. It also
manages **workgroups** — fleets of isolated worktrees/clones for running parallel
agents — keeping their WIP safe.

## Highlights

- **Multi-provider discovery** — GitHub, GitHub Enterprise, GitLab (incl.
  self-hosted), and Gitea/Forgejo, by user, org, group, or raw search query.
  SSH or HTTPS/PAT transport (`clone_protocol`).
- **One-command bootstrap** — `git-mgr bootstrap` (init → doctor → discover →
  sync → agent) plus a portable `manifest export/import` to rehydrate a new
  machine; `doctor` diagnoses tooling/auth/state before things break.
- **Structured layout** — repos land at `{{.Host}}/{{.Owner}}/{{.Name}}` (fully
  templatable). Operate them in bulk with named **sets** (`--set`) and
  `run -- <cmd>`.
- **Transparent backups** of uncommitted work via stash-refs and patch files —
  never modifies your working tree, index, branches, or stash list; `restore`
  recovers them (into a scratch dir or in place).
- **Workgroups / agent fleets** — spin up N isolated worktrees or clones and run
  an agent in each pane (`wg create … --open --agent claude`); per-member backup,
  safe teardown, recover, and prune keep agent WIP from ever being lost.
- **Cron-safe sync** — flock-guarded, fetch-only, with per-repo fetch intervals
  and retry/back-off; an **operation log** (`report`/`log`) records what ran.
- **Resilient** — one failing provider doesn't abort the run; GitHub/Gitea calls
  retry on rate-limit and 5xx; Ctrl-C cancels in-flight work cleanly.

## Install

```bash
# From source (requires Go; see go.mod for the version)
go install github.com/ashtonian/git-mgr@latest

# Docker (multi-arch: linux/amd64, linux/arm64)
docker pull ashtonian/git-mgr

# Homebrew
brew install ashtonian/tap/git-mgr
```

git-mgr shells out to `git`, so git must be on `PATH`.

## Quick start

**No config needed** — if git and your auth (SSH agent/keys, or a git credential
helper like `gh auth setup-git`) already work on your machine, git-mgr does too.
It uses your existing transport and a default layout (`~/repos`):

```bash
git-mgr adopt ~/code/myrepo            # track a clone you already have
git-mgr clone git@github.com:me/app    # clone + track by URL (your SSH/HTTPS auth)
git-mgr sync                           # fetch all tracked repos + back up WIP
git-mgr status                         # table of what you're managing
git-mgr wg create scratch --source ~/code/app --count 4 --open --agent claude
```

To **auto-discover** repos from a provider (the one thing that needs to know
*what* to fetch): if you're logged into the `gh` CLI, `init` detects it and
writes a ready GitHub provider + rule — so this is zero-edit:

```bash
git-mgr init                 # logged into gh? auto-configures GitHub. else: uncomment a stub
git-mgr discover --apply     # clone everything the rules match
git-mgr install-agent        # optional: periodic background sync
```

`git-mgr bootstrap` runs all of that in one command (init → doctor → discover →
sync → agent), so on a gh-authed machine it's a true one-liner.

## Commands

| Command | Description |
|---|---|
| `git-mgr init` | Create a config file with sensible defaults. |
| `git-mgr bootstrap` | Set up a fresh machine in one command: init → doctor → discover --apply → sync → install agent. `--profile`, `--no-agent`, `--dry-run`. |
| `git-mgr discover` | Discover repos from providers (dry-run; `--apply` to clone). `--rule`, `--tag` to scope. |
| `git-mgr sync` | Fetch all repos + back up uncommitted changes (the cron command). `--no-backup`, `--no-fetch`, `--no-discover`, `--force`, `--push-branch`. |
| `git-mgr backup` | Back up only (no fetch). `--list` to see backups, `--restore <id>` for instructions, `--push-branch` to also save WIP to a remote branch. |
| `git-mgr restore <repo> [id]` | Recover a backup: print steps (default), `--into <dir>` to clone+apply into a scratch dir, or `--apply` onto the tree (dirty-guarded). |
| `git-mgr status` | Table of managed repos. `--live` inspects working trees now (dirty + ahead/behind, no network); `--json`, `--dirty`, `--stale`, `--errors`, `--tag`, `--repos`. |
| `git-mgr doctor` | Diagnose tooling, auth (tokens/SSH keys), config, and repo-on-disk health. `--fix` for safe repairs. |
| `git-mgr clone <url>` | Clone and track a single repo by SSH URL. `--tag`, `--path`. |
| `git-mgr adopt <path>` | Import an existing clone. `--scan` for batch, `--dry-run`, `--tag`. |
| `git-mgr remove <pattern>` | Stop tracking repos. `--tag`/`--set`, `--delete` to also remove from disk, `--yes`. |
| `git-mgr run -- <cmd>` | Run a command in each matching repo (parallel). `--repos`/`--tag`/`--set`, `--fail-fast`. |
| `git-mgr manifest export\|import` | Portable `{url,tags,path}` manifest to rehydrate the repo set on a new machine. |
| `git-mgr remote-sync` | Push backup refs to remotes. `--repos`, `--tag`, `--dry-run`. |
| `git-mgr config` | Print resolved config. `--validate`, `--edit`, `--path`, `--for-repo <key>`, `--redact`. |
| `git-mgr report` / `log` | Summarize / list recent activity (clones, backups, syncs…) from the operation log. `--since`, `-n`. |
| `git-mgr workgroup` (`wg`) | Manage workgroups: parallel worktrees or multi-repo bundles. `wg create` builds one from flags or config; `--save`, `--sync`, `open`, `ensure`, `check`. |
| `git-mgr install-agent` | Install/remove a periodic sync agent (launchd/systemd/cron). |
| `git-mgr install-completions` | Print a shell completion script (bash/zsh/fish). See [Shell completions](#shell-completions). |
| `git-mgr version` | Print version, commit, build date. |

Global flags: `--config`, `--state`, `--verbose`/`-v`, `--quiet`/`-q`,
`--concurrency`/`-j`.

## Shell completions

Tab-completion for commands, flags, and dynamic values — repo keys, tags, named
sets, and workgroup names are pulled live from your state/config.

```bash
# zsh / bash — add to your rc file
eval "$(git-mgr install-completions)"

# fish
git-mgr install-completions | source
```

The dynamic value predictors honor `GIT_MGR_CONFIG` / `GIT_MGR_STATE`, so
completion reflects the same state your commands use. Note: to complete workgroup
subcommands, spell out `git-mgr workgroup …` — the `wg` alias does not expand its
subcommands during completion.

## Configuration

Config lives at `~/.config/git-mgr/config.yaml`. See
[`config.example.yaml`](config.example.yaml) for a fully annotated reference.

If that default file doesn't exist, git-mgr runs on defaults and `GIT_MGR_*`
environment variables. A path you choose with `--config` or `GIT_MGR_CONFIG`
must exist and parse: a missing, unreadable or malformed file is an error rather
than a silent fall back to defaults, which would otherwise run with no discovery
rules and mark every discovered repo stale.

### Providers

Any number of GitHub, GitLab, and Gitea/Forgejo instances:

```yaml
providers:
  - name: "github.com"
    type: "github"                    # github | gitlab | gitea | forgejo
    token_command: "gh auth token"

  - name: "ghes.company.com"
    type: "github"
    url: "https://ghes.company.com"   # GitHub Enterprise
    token_env: "GHES_TOKEN"
    ssh_key: "~/.ssh/id_work"

  - name: "gitlab.company.com"
    type: "gitlab"
    url: "https://gitlab.company.com"
    token_env: "GITLAB_COMPANY_TOKEN"

  - name: "gitea.company.com"
    type: "gitea"                     # "forgejo" is an alias
    url: "https://gitea.company.com"  # /api/v1 is handled by the SDK
    token_env: "GITEA_TOKEN"
```

Token resolution order: `token` → `token_env` → provider default env var
(`GITHUB_TOKEN` / `GITLAB_TOKEN` / `GITEA_TOKEN` / `FORGEJO_TOKEN`) →
`token_command`.

### Discovery rules

Each rule selects repos from one provider. Use `org`/`user` for GitHub and
Gitea, `group`/`user` for GitLab. Repos matching **any** rule are included.

```yaml
discovery:
  - name: "personal"
    provider: "github.com"
    user: "your-login"
    tags: ["personal"]

  - name: "work-services"
    provider: "ghes.company.com"
    org: "platform"
    visibility: "all"                 # all | public | private | internal
    languages: ["Go", "Rust"]         # any of these, case-insensitive
    topics: ["service"]               # repo must have ALL listed topics
    name_pattern: "^svc-"             # regex on repo name
    include_forks: false
    include_archived: false
    tags: ["work", "services"]
    ssh_key: "~/.ssh/id_work"
```

Filters are pushed to the server where the provider's list API supports it, and
applied locally otherwise (per-repo language/topic lookups happen only for repos
that pass the cheaper filters). When a provider genuinely can't honor a filter,
`discover` prints a warning rather than silently returning nothing.

### Raw search queries

For power users, a rule can pass a raw `query:` straight to the provider's native
search API instead of the structured selectors/filters. It is mutually exclusive
with `user`/`org`/`group` and the structured filters:

```yaml
discovery:
  - name: "go-stars"
    provider: "github.com"
    query: "org:acme language:go stars:>50"
    tags: ["oss"]
```

GitHub's Search API caps results at 1000 per query; when a query matches more,
`discover` warns that results were truncated (list-based `org:`/`user:` rules
have no such cap). GitLab and Gitea route `query:` to their own project/repo
search.

### Authentication

git-mgr separates two auth surfaces and **does not store credentials** itself:

- **Provider APIs** (discovery) use a PAT, resolved per provider from `token` /
  `token_env` / `token_command` (e.g. `token_command: "gh auth token"`).
- **Git transport** (clone/fetch) uses `clone_protocol` — `ssh` (default; your
  SSH agent/keys, or a provider/override `ssh_key`) or `https`. Over HTTPS, auth
  flows through git's own credential helper (`gh auth setup-git`, osxkeychain, a
  Git Credential Manager, or a PAT stored there). Set it globally or per repo set:

```yaml
clone_protocol: https            # default ssh
overrides:
  - repos: ["github.com/work-org/*"]
    clone_protocol: ssh
    ssh_key: "~/.ssh/id_work"
```

`git-mgr clone <url>` honors the scheme you pass (an `https://` URL clones over
HTTPS, `git@…`/`ssh://` over SSH). `git-mgr doctor` reports whether each
provider's token resolves and whether configured SSH keys exist.

### Manual repos, excludes, overrides, tags

```yaml
# Track individual repos without a discovery rule.
repos:
  - url: "git@github.com:someone/useful-fork.git"
    tags: ["libs"]

# Skip repos even if a rule matched them.
exclude:
  - name_pattern: "^deprecated-"
  - repos: ["my-org/legacy-monolith"]

# Override any setting per repo or glob (later entries win).
overrides:
  - repos: ["ghes.company.com/*"]
    base_dir: "~/work"                 # clone this set under a different root
    ssh_key: "~/.ssh/id_work"
    clone_protocol: "https"            # transport for this set (else infer/ssh)
    backup: { strategy: "patch" }
    remote_sync: { enabled: false }
  - repos: ["github.com/your-login/dotfiles"]
    path_template: "{{.Name}}"
  - repos: ["github.com/acme/archived-*"]
    sync: { enabled: false }           # track + back up, but never auto-fetch
```

Everything has a per-repo escape hatch: `base_dir`/`path_template` (destination),
`ssh_key`/`clone_protocol` (auth/transport), and `sync`/`backup`/`remote_sync`
behavior. The operation log can be turned off with `log: { events: false }`.

Tags come from discovery rules or manual entries and filter any command. For
recurring selections, define a **named set** and use `--set` (it composes with
`--repos`/`--tag`):

```yaml
sets:
  - name: core
    repos: ["github.com/acme/*"]
  - name: active-backend
    tags: ["backend"]
    exclude_archived: true
```

```bash
git-mgr status --tag work
git-mgr sync   --set core
git-mgr run    --set active-backend -- go test ./...
```

## Transparent backups

The backup system saves uncommitted work without modifying your working tree,
branches, or stash list. Two complementary strategies:

- **Stash refs** — `git stash create` produces a commit hash with no side
  effects, stored under `refs/git-mgr/backups/<timestamp>`. Invisible to
  `git branch`, `git stash list`, `git log`, and IDEs.
- **Patch files** — `git diff` output plus a tar of untracked files, stored
  outside the repo at `~/.local/share/git-mgr/patches/`.

Guarantees: never modifies the working tree/index/branches/stash; skips locked
repos (`.git/index.lock`); excludes oversized untracked files; auto-prunes old
backups (`max_per_repo`). Patches are saved byte for byte, so `git apply`
reproduces the index and working tree exactly. A patch backup is written to a
temporary folder and published only when every part succeeded, and old backups
are pruned only after a new one succeeds. A dirty repo whose backup fails is
reported as an error, never silently recorded as backed up.

```bash
git-mgr backup --list --repos "*/my-repo"
git-mgr backup --restore 20260417T103000Z --repos "*/my-repo"   # prints how to restore
```

**Save WIP offsite** — `--push-branch` (or `backup.push_branch: true`) also
force-pushes a snapshot of your uncommitted tracked changes to a recoverable
branch `git-mgr/wip/<branch>` on the remote. Unlike the hidden stash-ref, it's a
normal branch visible in the host's UI, so dirty work survives a lost laptop and
is one `git fetch` away. The branch is force-updated each run; the working tree
is never touched.

```bash
git-mgr backup --push-branch --repos "*/my-repo"   # local backup + push WIP to git-mgr/wip/<branch>
git-mgr sync --push-branch                          # same, as part of the periodic sync
```

## Workgroups

A *workgroup* is a parent directory of N working directories operated on as a
unit. Two kinds:

- **clone** (the default) — N independent clones, e.g. a microservice bundle, or
  N isolated agent workspaces on one repo. Fully separate object stores.
- **worktree** — N `git worktree`s of one source repo, each on its own branch,
  sharing one object store (cheap; pass `--source`).

Unlike sync/backup, workgroup commands deliberately mutate on request. Launch one
as a one-off from the CLI — no config edit needed:

```bash
# A clone fleet of every repo tagged "backend" (clone is the default kind):
git-mgr wg create review --from-tag backend
git-mgr wg open review                       # tmux session, one pane per member

# 3 independent clones of one repo — 3 agents, isolated working copies:
git-mgr wg create fleet --repo git@github.com:me/app.git --count 3

# Worktrees instead (shared object store), forked from origin/main:
git-mgr wg create scratch --source ~/code/app --count 4 --base-ref origin/main

# Persist any of these to config (--force overwrites), and track the underlying
# repo(s) so `git-mgr sync` keeps them fetched and backed up:
git-mgr wg create fleet --repo git@github.com:me/app.git --count 3 --save --sync
```

**Agent shells, one command.** `wg open --cmd <command>` runs a command in each
pane before handing it a shell; combined with `--open` on create you get N
isolated workspaces each running your agent. Under iTerm the panes are native
splits (tmux `-CC` control mode), elsewhere a normal tmux session:

```bash
# 4 clones of a repo, each pane running `claude` (a shell alias works too):
git-mgr wg create fleet --repo git@github.com:me/app.git --count 4 --open --cmd claude

# or open an existing workgroup the same way:
git-mgr wg open fleet --cmd claude          # add --no-check to skip the startup status line
```

`--member` (repeatable) sets members explicitly — `name[:branch]` for worktree,
a repo URL/key for clone — overriding `--count`. Workgroups declared in config
(`workgroups:`) are reconciled with `wg ensure` and rebuilt deterministically
after a reboot — a machine restart is a non-event. `wg check` exits non-zero on
drift (missing members, wrong branch, in-progress rebase/merge, behind upstream),
and `wg backup` (plus the scheduled `sync`) keeps every member's WIP safe.

| `wg` subcommand | Description |
|---|---|
| `create <name>` | Build from flags (one-off) or config. `--kind`, `--repo`, `--from-tag`, `--from-set`, `--source`, `--count`, `--member`, `--save`, `--sync`, `--open`, `--cmd`/`--agent`. |
| `ensure <name>` | Reconcile disk to the declared/created state (idempotent). |
| `open <name>` | Build/attach a tmux session, one pane per member. `--cmd <command>` (or `--agent <name>` from config) runs it per pane; `--no-check`, `--print`, `--recreate`, `--no-attach`. |
| `check <name>` | Assert health; non-zero exit on drift. |
| `backup <name>` / `recover <name>` | Back up / restore each member's WIP (`--push-branch`; `recover --into`/`--apply`). |
| `prune <name>` | Reap members with no unique work (clean + merged); never touches dirty WIP. `--dry-run`, `--all`. |
| `ls` / `status <name>` | List workgroups / show member status (`--json`). |
| `overview` | Member health across all workgroups — the fleet supervisor view (`--json`). |
| `rm <name>` | Tear down (backs up dirty members first unless `--force`). |
| `adopt <name>` | Import an existing directory of working trees. |

## Running on a schedule

`git-mgr sync` is cron-safe: flock prevents overlapping runs, `min_fetch_interval`
skips recently-fetched repos, `auto_discover` picks up new repos, and it is
strictly fetch-only.

```bash
git-mgr install-agent          # launchd (macOS) / systemd timer (Linux) / cron
```

Or wire it up by hand:

```cron
*/15 * * * * /usr/local/bin/git-mgr sync --quiet 2>> ~/.local/share/git-mgr/git-mgr.log
```

## Resilience

GitHub and Gitea requests retry automatically on rate-limit (429 / secondary
limit) and 5xx responses, honoring `Retry-After` with capped back-off (the GitLab
client has equivalent retry built in). If one discovery rule fails, the others
still run, and stale detection is skipped for that run so a transient outage
can't mark healthy repos stale. A run filtered with `--tag` or `--rule` only
judges repos first found by the rules it ran, so `sync --tag personal` never
marks work repos stale. SIGINT/SIGTERM cancels in-flight git and API operations
cleanly.

## Environment variables

All config fields can be overridden via `GIT_MGR_*` env vars (e.g.
`GIT_MGR_BASE_DIR`, `GIT_MGR_CONCURRENCY`). Notable ones:

| Variable | Effect |
|---|---|
| `GIT_MGR_CONFIG` | Config file path (same as `--config`). |
| `GIT_MGR_STATE` | State file path (same as `--state`). |
| `GIT_MGR_BASE_DIR` | Override `base_dir`. |
| `GITHUB_TOKEN` / `GITLAB_TOKEN` / `GITEA_TOKEN` / `FORGEJO_TOKEN` | Default provider tokens. |

## Docker

```bash
docker run -v ~/.config/git-mgr:/root/.config/git-mgr \
           -v ~/repos:/root/repos \
           -v ~/.ssh:/root/.ssh:ro \
           ashtonian/git-mgr sync
```

## Development

```bash
go build ./...
go vet ./...
go test ./...                 # unit tests
go test -race ./...           # with the race detector
```

End-to-end tests live in a **separate module** under [`e2e/`](e2e/) so their
heavy dependencies (testcontainers, Docker client) stay out of the root module.
They build the `git-mgr` binary and drive it against a real Forgejo container:

```bash
cd e2e && go test ./...       # requires Docker
```

Build with version info:

```bash
go build -ldflags="-X main.version=1.0.0 -X main.commit=$(git rev-parse HEAD)" .
```

See [CONTRIBUTING.md](CONTRIBUTING.md) for the full workflow and
[SECURITY.md](SECURITY.md) for the security model and how to report issues.

## License

MIT — see [LICENSE](LICENSE).
