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
- **Transparent backups** of uncommitted work, per working copy and restorable
  exactly — never touches your working tree, index, `index.lock`, branches or
  stash list; `restore`
  recovers them (into a scratch dir or in place).
- **Workgroups / agent fleets** — spin up N isolated worktrees or clones and run
  an agent in each pane (`wg create … --open --cmd claude`); per-member backup,
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

```bash
git-mgr setup     # a few questions, a review of the config it will write, then it writes it
git-mgr plan      # what will be cloned and changed
git-mgr apply     # do it
```

`setup` starts every answer at what it finds on your machine, so on a typical
machine you mostly press Enter:

1. **Layout** — where repos live and how they're arranged (`owner/name`,
   `host/owner/name` or `name`), guessed from where your own repos already sit.
2. **Accounts** — every account `gh` is logged in to, plus any you add by hand
   (GitLab, Gitea/Forgejo, another GitHub). For each: whose repos to track
   (your own, including private ones, and your organizations, listed from the
   provider), how git reaches the host (an SSH key — setup asks each host which
   account your keys sign in as — or HTTPS), the commit identity (from your
   global git config or `includeIf` files), signing, and optionally a separate
   base dir.
3. **Features** — backups (on), background sync every 15 minutes (on), offsite
   backup refs (off), what `sync` may change by itself, what happens when a repo
   disappears upstream.
4. **Existing clones** — those discovery won't find where they are can be
   tracked in place.
5. **Review** — the diff of your config file and checks: each token works, each
   key signs in as the right account, signing works without a terminal. Nothing
   is written until you confirm.

Re-run `setup` any time to change your answers; it edits the config in place,
keeps your comments and touches only what it manages. Scripted:

| Flag | Effect |
| --- | --- |
| `--yes` | Take the detected and recommended answers without asking. |
| `--answers <file>` | Take the answers from a YAML file. |
| `--save-answers <file>` | Also write the answers used, to repeat this setup on another machine. |
| `--dry-run` | Show the diff and checks; change nothing. |
| `--offline` | Skip `ssh -T` probes and provider API calls. |

No config is needed for the basics either — if git and your auth already work,
git-mgr does too, with `~/repos` as the base dir:

```bash
git-mgr adopt ~/code/myrepo            # track a clone you already have
git-mgr clone git@github.com:me/app    # clone + track by URL (your SSH/HTTPS auth)
git-mgr sync                           # fetch all tracked repos + back up WIP
git-mgr status                         # table of what you're managing
git-mgr wg create scratch --source ~/code/app --count 4 --open --cmd claude
```

`git-mgr bootstrap` runs `setup` (if there's no config yet), `doctor`,
`discover --apply`, `sync` and installs the agent, in one command. `git-mgr init`
writes a config without asking anything.

## Commands

| Command | Description |
|---|---|
| `git-mgr setup` | Set up git-mgr, or change your setup: layout, accounts, what to track, features. `--yes`, `--answers`, `--save-answers`, `--dry-run`, `--offline`. |
| `git-mgr init` | Write a config with sensible defaults, without asking. |
| `git-mgr bootstrap` | Set up a fresh machine in one command: setup → doctor → discover --apply → sync → install agent. `--profile`, `--no-agent`, `--dry-run`. |
| `git-mgr discover` | Discover repos from providers and show the plan; `--apply` makes the changes that move and delete nothing (clone new repos, update URLs, managed git config, tags). `--rule`, `--tag` to scope. |
| `git-mgr sync` | Discover, apply config changes as far as `reconcile.auto` allows, fetch all repos and back up uncommitted changes (the cron command). `--no-backup`, `--no-fetch`, `--no-discover`, `--force`, `--push-branch`. |
| `git-mgr plan` | Show what config would change on disk and in state, without changing anything. `--json`. See [Config changes](#config-changes-plan-and-apply). |
| `git-mgr apply` | Make repos and workgroups match config, including the moves and removals sync leaves for you. Asks first; `--yes`. |
| `git-mgr backup` | Back up only (no fetch). `--list` to see backups, `--restore <id>` for instructions, `--push-branch` to also save WIP to a remote branch. |
| `git-mgr restore <repo> [id]` | Recover a backup: print steps (default), `--into <dir>` to clone+apply into a scratch dir, or `--apply` onto the tree (dirty-guarded). |
| `git-mgr status` | Table of managed repos. `--live` inspects working trees now (dirty + ahead/behind, no network); `--json`, `--dirty`, `--stale`, `--errors`, `--tag`, `--repos`. |
| `git-mgr doctor` | Diagnose tooling, auth (tokens/SSH keys), config, and repo-on-disk health. Read-only; `plan`/`apply` fix what config can. |
| `git-mgr clone <url>` | Clone and track one repo by hand, at its templated path or `--path`. `--tag`. A repo added by hand keeps the path and URL it was given. |
| `git-mgr adopt <path>` | Track an existing clone where it is. `--scan` for batch, `--dry-run`, `--tag`. |
| `git-mgr remove <pattern>` | Stop tracking repos. `--tag`/`--set`, `--yes`. `--delete` also removes the clone from disk after archiving its unique work (see [Deleting without losing work](#deleting-without-losing-work)). |
| `git-mgr run -- <cmd>` | Run a command in each matching repo (parallel). `--repos`/`--tag`/`--set`, `--fail-fast`. |
| `git-mgr manifest export\|import` | Portable `{url,tags,path}` manifest to rehydrate the repo set on a new machine. |
| `git-mgr remote-sync` | Push backup refs to remotes. `--repos`, `--tag`, `--dry-run`. |
| `git-mgr config` | Print resolved config. `--validate`, `--edit`, `--path`, `--for-repo <key>`, `--redact`. |
| `git-mgr report` / `log` | Summarize / list recent activity (clones, backups, syncs…) from the operation log. `--since`, `-n`. |
| `git-mgr workgroup` (`wg`) | Manage workgroups: parallel worktrees or multi-repo bundles. `wg create` builds one from flags or config; `--save`, `--sync`, `open`, `ensure`, `check`. |
| `git-mgr install-agent` | Install, list (`--list`) or remove (`--uninstall`) background syncs (launchd/systemd/cron); `--name` and `--repos`/`--tag`/`--set` for several. |
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
environment variables. Only what's switched on runs: discovery needs providers
and rules, workgroups exist once declared, background sync once the agent is
installed, offsite backup refs once `remote_sync` is enabled. Backups of
uncommitted work are on by default. A flat tree with one account and nothing
else is a complete setup:

```yaml
base_dir: ~/projects
layout: owner/name            # owner/name | host/owner/name (default) | name
accounts:
  - name: personal
    hosts: [github.com]
```

`layout` names a common `path_template`; set one or the other. With `name`,
two repos with the same name can't both be placed and are reported as blocked. A path you choose with `--config` or `GIT_MGR_CONFIG`
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

Each provider takes its token from the one source it sets: `token`,
`token_env` or `token_command`. If that source yields nothing — an unset
variable, a failing command — discovery for that provider fails rather than
trying another source, which could send one account's token to another
account's API. A provider that sets none uses `GITHUB_TOKEN` / `GITLAB_TOKEN` /
`GITEA_TOKEN` / `FORGEJO_TOKEN`, and without those sees only public repos.

### Discovery rules

Each rule selects repos from one provider. Use `org`/`user` for GitHub and
Gitea, `group`/`user` for GitLab. A rule with none of them lists the token's own
repos, private ones included; a named `user` lists only that user's public
repos. Repos matching **any** rule are included.

```yaml
discovery:
  - name: "personal"
    provider: "github.com"            # no user/org: the token's own repos
    affiliation: [owner]              # GitHub: owner | collaborator | organization_member
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
`discover` reports the rule as incomplete and none of its repos are judged
stale that run (list-based `org:`/`user:` rules have no such cap). GitLab and Gitea route `query:` to their own project/repo
search.

### Authentication

git-mgr separates two auth surfaces and **does not store credentials** itself:

- **Provider APIs** (discovery) use a PAT from the provider's one token source
  (e.g. `token_command: "gh auth token"`).
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
HTTPS, `git@…`/`ssh://` over SSH). A URL embedding a credential
(`https://user:token@…`) is refused everywhere — clone, `repos:`, manifests,
workgroups, adopt — so none lands in state or logs; use a credential helper. `git-mgr doctor` reports whether each
provider's token resolves and whether configured SSH keys exist.

### Accounts

An account is one identity on one or more hosts: the SSH key and the commit
identity for the repos it covers. With work and personal repos on the same
host, each repo pushes with the right key and commits with the right email —
yours and your agents'.

```yaml
accounts:
  - name: personal
    hosts: [github.com]                # every github.com owner no other account claims
    ssh_key: ~/.ssh/id_personal
    git: {name: Ashton Kinslow, email: me@example.com}
  - name: work
    hosts: [github.com]
    owners: [acme, acme-labs]          # these owners, and their GitLab subgroups
    ssh_key: ~/.ssh/id_work
    git: {name: Ashton Kinslow, email: you@acme.com}
    signing:
      mode: required                   # inherit (default) | required | off
      key: ~/.ssh/id_work.pub          # .pub or key:: means SSH signing; a key ID means GPG
      program: /Applications/1Password.app/Contents/MacOS/op-ssh-sign   # optional
  - name: ghe
    ssh_key: ~/.ssh/id_ghe             # covers only what names it below
providers:
  - {name: ghes, type: github, url: https://ghes.acme.com, account: ghe}
```

A repo's account is the one named by the most specific of: a matching
override's `account:`, its `repos:` entry, its discovery rule, that rule's
provider; otherwise the account whose `hosts` and `owners` match the repo (an
account listing the owner beats one covering the whole host). Its SSH key
follows the same order, where an `ssh_key:` set directly at any of those places
counts too. `git-mgr config --for-repo <key>` shows what a repo resolves to.

git-mgr uses the key for its own clones and fetches, and writes the account
into the repo's own git config, so your own git commands there behave the same:

| Key | Written when |
| --- | --- |
| `core.sshCommand = ssh -i <key> -o IdentitiesOnly=yes` | the repo has an SSH remote and a key |
| `user.name`, `user.email` | the account sets them; otherwise your global identity applies |
| `commit.gpgsign`, `tag.gpgsign`, `gpg.format`, `user.signingkey`, `gpg.ssh.program`/`gpg.program`/`gpg.x509.program`, `gpg.ssh.allowedSignersFile` | `signing.mode: required` |
| `commit.gpgsign = false`, `tag.gpgsign = false` | `signing.mode: off`, so an agent never stalls on a passphrase prompt |

With `signing.mode: required`, `git-mgr doctor` makes a test signature the way
an agent would — no terminal, 20-second limit — and fails if it can't: SSH
signing through an agent or a passphrase-less key works unattended; GPG needs
a gpg-agent holding the passphrase.

Workgroup clone members get the same keys as the repo they come from, so
agents commit as the right account; worktree members share their source repo's
config. If you set one of these keys in a repo yourself, git-mgr leaves it and
reports the difference (see [Config changes](#config-changes-plan-and-apply)).
Changing an account updates every repo it covers on the next `sync` or `apply`.

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

## Config changes: plan and apply

git-mgr keeps a state file (`~/.local/share/git-mgr/state.yaml`) recording what
it last set for every repo and workgroup: path, origin URL, tags, the
discovering rule, the provider's repo ID, the copy ID, the repo-local git config
it wrote, and each workgroup's members. On every run it compares three things:

- **desired** — what config and this run's discovery ask for;
- **recorded** — what git-mgr set, from state;
- **on disk** — the directory, its origin URL and its git config.

Desired against recorded gives the changes. On disk against recorded gives
*drift*: something git-mgr set was changed by hand. Drift is reported and left
alone unless `reconcile.drift: overwrite`.

```console
$ git-mgr plan
Changes:
  ~ url              github.com/acme/api: git@github.com:acme/api.git -> https://github.com/acme/api.git
  ~ config           github.com/acme/api core.sshcommand: (unset) -> ssh -i '~/.ssh/id_work' -o IdentitiesOnly=yes
  ~ rename           github.com/acme/api-v2: github.com/acme/api -> github.com/acme/api-v2 (same repository (provider ID 4242))
  > move             github.com/acme/web: ~/repos/github.com/acme/web -> ~/repos/acme/web   [needs git-mgr apply]
  - remove-member    fleet/agent-3: ~/workgroups/fleet/agent-3 (no longer declared; unique work archived first)   [needs git-mgr apply]
  + create-member    fleet/agent-4: ~/workgroups/fleet/agent-4   [git-mgr apply or wg ensure]
Changed by hand, left alone (reconcile.drift: overwrite restores them):
  ! github.com/acme/tools origin URL: git-mgr set git@github.com:acme/tools.git, now git@work:acme/tools.git; config wants git@github.com:acme/tools.git

sync applies 3 of 6 on its own (reconcile.auto: safe); `git-mgr apply` makes them all.
```

| Config change | What happens |
| --- | --- |
| A new repo matches a rule or `repos:` entry | Cloned (`+ clone`). |
| `base_dir`, `path_template` or an override's path | The repo moves (`> move`), with its linked worktrees reconnected and workgroups that use it repointed. |
| `clone_protocol` | The origin URL changes (`~ url`). |
| An `ssh_key` or account added, changed or removed | `core.sshCommand`, `user.name` and `user.email` are written, changed or unset in repos (`~ config`) and clone members (`~ member-config`). |
| A rule's tags, or the rule that finds a repo | Tags and rule are updated in state (`~ tags`). |
| A repo renamed or transferred upstream | Matched by the provider's repo ID: rekeyed, re-pointed, moved; its clone, work and backups stay (`~ rename`). |
| A tracked clone deleted by hand | Cloned again. |
| A workgroup declared, or a member added | Created (`+ create-workgroup`, `+ create-member`). |
| A member or a whole workgroup removed from config | Its unique work is archived, then it is removed (`- remove-member`, `- remove-workgroup`). |
| A workgroup's `base_dir` | Its members move (`> move-member`). |

Changes marked `>` and `-` move or delete directories that a shell, editor or
agent may be using, so they wait for `git-mgr apply`, which lists them and asks
first. Everything else is applied by `sync` and `discover --apply` as it comes
up. `sync` and `discover` handle repos only; workgroups change through `apply`
and `wg ensure`.

Repos added by hand (`clone`, `adopt`, `manifest import`) keep the path and URL
they were given; only their tags and managed git config follow config. A
workgroup created from flags or adopted is kept as recorded: `wg ensure <name>`
recreates its missing members, and nothing removes it but `wg rm`.

An interrupted run is safe to repeat. Every change looks at the disk first: a
clone that finished before the interruption is adopted, a move that finished is
recorded, a member that was created is recorded.

| Key | Default | Meaning |
| --- | --- | --- |
| `reconcile.auto` | `safe` | What `sync` and `discover --apply` change on their own: `safe` (everything except moves and removals), `all`, or `none` (only report). |
| `reconcile.drift` | `report` | `report` leaves values changed by hand; `overwrite` puts git-mgr's back. |

## Transparent backups

Each backup is a snapshot of one working copy: the index, the tracked files in
the working tree, and the untracked files (ignored files never). It is built in
temporary index files, so it never touches your working tree, index, branches or
stash, and never takes `index.lock`: a backup taken while an agent's
`git commit` is running neither fails nor gets in its way.

`backup.strategy` picks where snapshots are kept:

- **Snapshot refs** (`stash-ref`) — a commit in `git stash`'s own layout at
  `refs/git-mgr/backups/<copy-id>/<backup-id>`, invisible to `git branch`,
  `git stash list`, `git log` and IDEs. Restores with
  `git stash apply --index <ref>`.
- **Patch backups** (`patch`) — the same snapshot as `staged.patch`,
  `unstaged.patch` and `untracked.tar.gz` under
  `~/.local/share/git-mgr/patches/<copy-id>/<backup-id>/`. They outlive the repo:
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
- **Nothing silently skipped.** Untracked files over `max_untracked_file_mb`
  (50) or past `max_untracked_total_mb` (500), unreadable files and nested git
  repositories are left out *and reported* with the reason, in the command's
  output and in the backup's metadata.
- **Failures are loud.** A backup that fails, in whole or in part, makes
  `backup`, `sync` and `wg backup` exit non-zero, is stored as
  `last_backup_error`, and shows as `failed` in `status`. A patch backup is
  written to a temporary folder and published only when complete, and pruning
  runs only after a backup succeeds.
- **Retention by age.** Per copy: every backup from the last 24 hours
  (`keep_all`), then the newest of each day for 14 days (`keep_daily`), capped at
  100 (`max_per_copy`). The newest backup is always kept.

```bash
git-mgr backup --list --repos "*/my-repo"
git-mgr restore github.com/me/my-repo                    # prints the restore commands
git-mgr restore github.com/me/my-repo --into /tmp/look   # clone + restore into a scratch dir
git-mgr restore github.com/me/my-repo --apply            # onto the clean working tree
```

**Save WIP offsite** — `--push-branch` (or `backup.push_branch: true`) also
force-pushes your uncommitted tracked changes, as one commit on top of HEAD, to
a branch `git-mgr/wip/<branch>` on the remote. It's a normal branch visible in
the host's UI, so dirty work survives a lost laptop. Untracked files and the
staged/unstaged split are not pushed. Recover with
`git fetch origin git-mgr/wip/<branch> && git cherry-pick --no-commit FETCH_HEAD`.

```bash
git-mgr backup --push-branch --repos "*/my-repo"   # local backup + push WIP to git-mgr/wip/<branch>
git-mgr sync --push-branch                          # same, as part of the periodic sync
```

### Deleting without losing work

Every command that deletes a clone or worktree — `remove --delete`,
`wg rm --delete`, `wg prune` and the `remove_and_delete` stale-cleanup policy —
first inspects it for work that exists nowhere else: changed or untracked files,
stashes, commits no remote has, commits on a detached HEAD and an unfinished
rebase or merge. If there is any, it is written to one git bundle in
`backup.archive_dir` (default `~/.local/share/git-mgr/archive/`) and verified with
`git bundle verify` before anything is deleted. For a worktree, its branches stay
in the source repo, so only the working tree and detached commits are archived.
If git-mgr can't inspect the repo or write the bundle, nothing is deleted and the
repo stays tracked.

Each bundle has a `.json` file beside it recording the source path, remotes,
branch, what was saved and the restore command:

```bash
git clone <remote> restored && cd restored
git fetch ~/.local/share/git-mgr/archive/<name>-<time>.bundle '+refs/*:refs/restored/*'
git for-each-ref refs/restored   # heads/*, git-mgr/archive/<id>/{head,worktree,stash-N}
```

The `worktree` ref is a commit of the working tree including untracked files;
`git checkout <ref> -- .` brings them back. Archives are never pruned
automatically.

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
a repo URL/key for clone — overriding `--count`. `wg ensure` creates whatever is
missing — workgroups declared in config (`workgroups:`), members added to a
declaration, members gone from disk, including those of workgroups created from
flags — so a machine restart is a non-event. Members removed from a declaration,
and new member paths, wait for `git-mgr apply` (see
[Config changes](#config-changes-plan-and-apply)). `wg check` exits non-zero on
drift (missing members, wrong branch, in-progress rebase/merge, behind upstream),
and `wg backup` (plus the scheduled `sync`) keeps every member's WIP safe.

| `wg` subcommand | Description |
|---|---|
| `create <name>` | Build from flags (one-off) or config. `--kind`, `--repo`, `--from-tag`, `--from-set`, `--source`, `--count`, `--member`, `--save`, `--sync`, `--open`, `--cmd`/`--agent`. |
| `ensure [name]` | Create what is missing of one workgroup or all of them; list removals and moves for `git-mgr apply`. Idempotent. |
| `open <name>` | Build/attach a tmux session, one pane per member. `--cmd <command>` (or `--agent <name>` from config) runs it per pane; `--no-check`, `--print`, `--recreate`, `--no-attach`. |
| `check <name>` | Assert health; non-zero exit on drift. |
| `backup <name>` / `recover <name>` | Back up / restore each member's WIP (`--push-branch`; `recover --into`/`--apply`). |
| `prune <name>` | Delete members with no unique work: clean, no stash, nothing unpushed, verified by a full inspection; anything it can't inspect is kept. `--dry-run`, `--all`. |
| `ls` / `status <name>` | List workgroups / show member status (`--json`). |
| `overview` | Member health across all workgroups — the fleet supervisor view (`--json`). |
| `rm <name>` | Forget the workgroup and leave its files. `--delete` also removes each member after archiving its unique work; `--force` discards that work instead; `--push-branch` pushes WIP first. Members that can't be removed stay tracked and the command exits non-zero. |
| `adopt <name>` | Import an existing directory of working trees. |

## Running on a schedule

`git-mgr sync` is built to run unattended. Only one sync runs at a time; a
second one exits 0 at once. Clones, fetches and backups run without the state
lock, which a run takes only for the milliseconds it needs to record a result,
so `git-mgr wg create` or `git-mgr clone` work normally while a long sync is
running. `min_fetch_interval` skips recently-fetched repos, and `auto_discover`
picks up new repos. It never pulls or merges, and it moves or deletes a repo
only if you configure it to (`reconcile.auto: all`,
`cleanup.policy: remove_and_delete`).

Nothing waits on a person or hangs forever:

- git runs with no terminal: `GIT_TERMINAL_PROMPT=0`, `GCM_INTERACTIVE=never`,
  `SSH_ASKPASS_REQUIRE=never`, and in a session of its own, so ssh can't ask
  for a passphrase or to trust a host key. A credential that needs typing
  fails with git's own message instead. Load keys into an agent, or use a
  credential helper.
- Every external call has a deadline, and the error names the setting that
  ran out. When one passes, git is killed along with everything it started.

| Setting | Default | Bounds |
| --- | --- | --- |
| `sync.backup_timeout` | `2m` | One working copy's backup (taken before its fetch, on its own budget) and status read. |
| `sync.timeout_per_repo` | `5m` | One repo's fetch, retries included. |
| `sync.clone_timeout` | `30m` | One clone. |
| `sync.api_timeout` | `60s` | One provider API request, retries included. |
| `sync.discovery_timeout` | `5m` | One discovery rule: listing plus language/topic lookups. |
| `lock_timeout` | `2m` | How long a command waits for another run's state write before exiting 75. |
| `token_command` | `30s` (fixed) | Running a provider's `token_command`. |

A clone is made in a hidden `.git-mgr-clone-*` directory beside its
destination and renamed into place when complete. A failed or killed clone
never leaves half a repository where the repo belongs, and the next clone of
the same repo removes what a killed run left behind.

### Installing a schedule

```bash
git-mgr install-agent                                   # every 15m: launchd (macOS) / systemd timer (Linux)
git-mgr install-agent --name work --tag work --interval 5m
git-mgr install-agent --list
git-mgr install-agent --uninstall --name work
git-mgr install-agent --print                           # show the unit files and commands; change nothing
```

Each schedule has a name (`default` unless you give one) and its own
selection of repos, so a work schedule and a personal one live side by side;
installing a name again replaces it. `bootstrap --profile work` installs a
`work` schedule that syncs `--tag work`. Schedules are recorded in the state
file, and `git-mgr doctor` checks each one.

| Flag | Default | Meaning |
| --- | --- | --- |
| `--name` | `default` | Schedule name: lowercase letters, digits, dashes. |
| `--interval` | `15m` | Must divide an hour or a day (5m, 10m, 15m, 20m, 30m, 1h, 2h, 3h, 4h, 6h, 8h, 12h, 24h), so every backend runs it exactly. |
| `--repos`, `--tag`, `--set` | all repos | What the schedule syncs. |
| `--kind` | per OS | `launchd`, `systemd` or `cron`. |
| `--bin` | the `git-mgr` on PATH | Binary the schedule runs. |
| `--daemon` | off | macOS: a LaunchDaemon that runs as you while nobody is logged in. Installed with `sudo`. |
| `--linger` | off | Linux: `loginctl enable-linger`, so the timer runs while you are logged out. |
| `--log` | `~/.local/share/git-mgr/git-mgr.log` | Output of launchd and cron runs; systemd runs log to the journal (`journalctl --user -u git-mgr`). |

What install does, so a background run behaves like one from your shell:

- **Binary:** the `git-mgr` on your PATH when it is the binary you ran,
  because a package manager's link there (`/opt/homebrew/bin`,
  `~/.nix-profile/bin`) survives upgrades. A path an upgrade or garbage
  collection removes -- `/nix/store`, a Homebrew `Cellar`, a temporary build
  -- is refused; put `git-mgr` on PATH or pass `--bin`.
- **Environment:** your current `PATH` is recorded, so `gh`, credential
  helpers and `ssh` are found. `SSH_AUTH_SOCK` is recorded when the agent's
  socket outlives your login (1Password, `$XDG_RUNTIME_DIR`); a per-login one
  isn't, and on macOS a LaunchAgent gets the session's agent from launchd. No
  other variable is recorded: tokens stay out of unit files, so use
  `token_command` rather than a `token_env` set only in your shell.
- **Priority:** runs at `nice 10` with idle/low-priority I/O.
- **Missed runs:** systemd schedules with `OnCalendar=` and `Persistent=true`,
  so a run missed while the machine was off happens at boot; launchd runs a
  missed interval on wake.
- **Without a login:** a systemd user timer stops at logout unless lingering
  is on (`--linger`, which the setup wizard offers); a macOS LaunchAgent runs
  only while you are logged in, so a Mac nobody logs in to needs `--daemon`.
  Before anyone logs in the login keychain is locked, so a `token_command`
  such as `gh auth token` fails there; read the token from a file instead.
- **crontab:** never rewritten if it can't be read (only "no crontab" counts
  as empty), and backed up to `backups/crontab-<time>.bak` beside the state
  file before each change. Only lines ending in the schedule's own marker
  (`# com.ashtonian.git-mgr[.<name>]`) are touched.

`git-mgr doctor` checks each recorded schedule: still installed, its binary
present, lingering on (systemd), and it runs its own checks again in the
schedule's environment -- recorded PATH and agent, nothing else -- so a `gh`
that isn't on the recorded PATH or a token that only your shell can read
shows up there instead of as silent failures in the log.

Exit codes are stable, so a scheduler or script can tell a flaky repo from a
broken setup. With `--quiet`, only failures are printed, to stderr.

| Code | Meaning |
| --- | --- |
| 0 | Everything succeeded. `sync` also exits 0 when another run holds the lock, since overlapping scheduled runs are routine. |
| 1 | The command failed: bad config, unreadable state, or every target failed. |
| 3 | Partial failure: some repos, members or changes failed and the rest succeeded. Each failure is printed to stderr. |
| 75 | Another git-mgr run held the state lock for longer than `lock_timeout`; try again (EX_TEMPFAIL). `git-mgr doctor` reports a stuck holder. |
| 80 | Usage error: unknown command or flag. |

## Resilience

GitHub and Gitea requests retry automatically on rate-limit (429 / secondary
limit) and 5xx responses, honoring `Retry-After` with capped back-off (the GitLab
client has equivalent retry built in). If one discovery rule fails, the others
still run, and stale detection is skipped for that run so a transient outage
can't mark healthy repos stale. A run filtered with `--tag` or `--rule` only
judges repos first found by the rules it ran, so `sync --tag personal` never
marks work repos stale; repos from a failed rule are left exactly as they are.
A rule whose result may be missing repos -- a search capped at 1000, a
language or topic lookup that failed or timed out -- still adds what it found,
but judges none of its repos stale that run.
An interrupted run is safe to repeat (see
[Config changes](#config-changes-plan-and-apply)). SIGINT/SIGTERM cancels
in-flight git and API operations cleanly.

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

### Code map

| Package | Owns |
| --- | --- |
| `main.go`, `internal/cmd` | The CLI: flags, wiring, output. Commands load config and state, call the packages below, print. |
| `internal/config` | Loading, defaults, validation, per-repo overrides, SSH key and protocol resolution. |
| `internal/discovery` | Provider clients (GitHub, GitLab, Gitea/Forgejo), rule filtering, stale detection. |
| `internal/reconcile` | Desired vs recorded vs on-disk: plans and applies repo and workgroup changes. |
| `internal/state` | The state file: schema, migrations, locking, atomic writes. |
| `internal/gitops` | Everything that runs git: clone, fetch, status, snapshots, backups, restore, archives, copy IDs, worktrees. |
| `internal/workgroup` | Workgroup specs, member creation, health, removal. |
| `internal/repourl` | Parsing remote URLs into host/owner/name keys. |
| `internal/pathtemplate`, `internal/pathutil`, `internal/shellutil`, `internal/ctxutil` | Path templates, deletion guards, shell quoting, optional deadlines. |
| `internal/scheduler` | launchd, systemd and cron installation. |
| `internal/oplog`, `internal/notify` | The operation log and desktop notifications. |

See [CONTRIBUTING.md](CONTRIBUTING.md) for the full workflow and
[SECURITY.md](SECURITY.md) for the security model and how to report issues.

## License

MIT — see [LICENSE](LICENSE).
