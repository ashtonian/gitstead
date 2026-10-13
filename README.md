# git-mgr

Discover, clone, sync, and back up git repositories across multiple GitHub,
GitLab, and Gitea/Forgejo instances — into one structured local layout.

git-mgr is built to run two ways: **interactively** when bootstrapping a new
machine, and as a **cron job** that fetches everything and transparently backs
up uncommitted work without ever touching your branches or working tree. It also
manages **workspaces** — a repo, or several related ones, worked on by several
agents at once, each in a copy of its own — keeping their WIP safe.

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
- **Workspaces for parallel agents** — one repo or several related ones × N
  agents, each agent in its own worktree (or clone) on its own branch, made from
  the repo's tracked clone so nothing is downloaded twice
  (`ws create app --repo github.com/me/app --agents 4 --open --agent claude`);
  per-copy backup, safe teardown, recover and prune keep agent WIP from ever
  being lost.
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
4. **Agents** — whether to run coding agents in [workspaces](#workspaces) (on
   when `claude` or `codex` is on your `PATH`): which agents, plus a command of
   your own such as a shell-function wrapper, how many per workspace, where
   workspaces live, and the [launcher](#fleets-and-launchers) that opens them
   (`auto`, or tmux, iTerm or Muxy when installed). It writes `agents:`,
   `workspace_defaults:` and `launchers.default`; workspaces themselves are made
   afterwards with `git-mgr ws create … --save`, once their repos are tracked.
5. **Existing clones** — those discovery won't find where they are can be
   tracked in place.
6. **Review** — the diff of your config file and checks: each token works, each
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
git-mgr ws create app --repo ~/code/myrepo --agents 4 --open --cmd claude
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
| `git-mgr sync` | Discover, apply config changes as far as `reconcile.auto` allows, fetch all repos, back up uncommitted changes and, with `remote_sync.enabled`, take them offsite (the cron command). `--no-backup`, `--no-fetch`, `--no-discover`, `--force`. |
| `git-mgr plan` | Show what config would change on disk and in state, without changing anything. `--json`. See [Config changes](#config-changes-plan-and-apply). |
| `git-mgr apply` | Make repos and workspaces match config, including the moves and removals sync leaves for you. Asks first; `--yes`. |
| `git-mgr backup` | Back up only (no fetch). `--list` to see backups, `--restore <id>` for instructions. |
| `git-mgr restore <repo> [id]` | Recover a backup: print steps (default), `--into <dir>` to clone+apply into a scratch dir, or `--apply` onto the tree (dirty-guarded). |
| `git-mgr status` | Table of managed repos. `--live` inspects working trees now (dirty + ahead/behind, no network); `--json`, `--dirty`, `--stale`, `--errors`, `--tag`, `--repos`. |
| `git-mgr doctor` | Diagnose tooling, auth (tokens/SSH keys), config, and repo-on-disk health. Read-only; `plan`/`apply` fix what config can. |
| `git-mgr clone <url>` | Clone and track one repo by hand, at its templated path or `--path`. `--tag`. A repo added by hand keeps the path and URL it was given. |
| `git-mgr adopt <path>` | Track an existing clone where it is. `--scan` for batch, `--dry-run`, `--tag`. |
| `git-mgr remove <pattern>` | Stop tracking repos. `--tag`/`--set`, `--yes`. `--delete` also removes the clone from disk after archiving its unique work (see [Deleting without losing work](#deleting-without-losing-work)). |
| `git-mgr run -- <cmd>` | Run a command in each matching repo (parallel). `--repos`/`--tag`/`--set`, `--fail-fast`. |
| `git-mgr manifest export\|import` | Portable `{url,tags,path}` manifest to rehydrate the repo set on a new machine. |
| `git-mgr remote-sync` | Back up and take WIP and unpushed commits offsite now, under `remote_sync`. `--repos`, `--tag`, `--set`, `--dry-run`; `--list` shows what every machine left offsite and how to restore it. |
| `git-mgr config` | Print resolved config. `--validate`, `--edit`, `--path`, `--for-repo <key>`, `--redact`. |
| `git-mgr report` / `log` | Summarize / list recent activity (clones, backups, syncs…) from the operation log. `--since`, `-n`. |
| `git-mgr workspace` (`ws`, `wg`) | Manage workspaces: repos × agents, each agent in a copy of its own. See [Workspaces](#workspaces). |
| `git-mgr fleet` | Open several workspaces' agents side by side with tmux, iTerm, Muxy or a plugin; `open`, `plan`, `ls`, `status`, `stop`. See [Fleets](#fleets-and-launchers). |
| `git-mgr archives ls\|show` | The catalog of unique work saved before copies and repos were deleted, and how to restore each. |
| `git-mgr cd\|path\|open <query>` | Jump to, print, or open in your editor (`--web`: the repo's page) a repo, workspace or agent's folder from part of its name. See [Jump and open](#jump-and-open). |
| `git-mgr shell-init bash\|zsh\|fish` | Print the shell function that makes `git-mgr cd` change directory. |
| `git-mgr install-agent` | Install, list (`--list`) or remove (`--uninstall`) background syncs (launchd/systemd/cron); `--name` and `--repos`/`--tag`/`--set` for several. |
| `git-mgr install-completions` | Print a shell completion script (bash/zsh/fish). See [Shell completions](#shell-completions). |
| `git-mgr version` | Print version, commit, build date. |

Global flags: `--config`, `--state`, `--verbose`/`-v`, `--quiet`/`-q`,
`--concurrency`/`-j`.

## Jump and open

```bash
eval "$(git-mgr shell-init zsh)"     # in ~/.zshrc (or bash; fish: git-mgr shell-init fish | source)
git-mgr cd app                        # the repo app
git-mgr cd acme/api                   # by owner/name, when two repos share a name
git-mgr cd app 2                      # workspace app, agent-2's folder
git-mgr open platform worker          # agent-1's worker copy in platform, in your editor
git-mgr open app --web                # the repo's page on its host
vim "$(git-mgr path app)/go.mod"
```

A query is part of a name. It is matched against every tracked repo (by key,
`owner/name`, name and folder), workspace, agent's folder and copy: a whole
name fits best, then a path suffix (`acme/api`), a prefix, a substring, and last
the letters in order (`wkr` for `worker`). Several words must all fit. When a
repo and a workspace fit equally, the repo wins (`--kind workspace` picks the
workspace); when two things of one kind do, git-mgr asks which on a terminal,
and otherwise lists them and exits 1. A program can't change its shell's
directory, so `cd` comes from the function `shell-init` prints; without it,
`git-mgr cd` prints the path. `open` runs `editor` from config, else `$VISUAL`,
else `$EDITOR`, with the path as its last argument (so `editor: code -n`
works), in the target's directory.

| Key | Default | Meaning |
| --- | --- | --- |
| `editor` | `$VISUAL`, then `$EDITOR` | The command `git-mgr open` runs, a shell command with the path added as its last argument. |

## Shell completions

Tab-completion for commands, flags, and dynamic values — repo keys, tags, named
sets, workspace and fleet names, and `cd`/`path`/`open` targets are pulled live
from your state/config.

```bash
# zsh / bash — add to your rc file
eval "$(git-mgr install-completions)"

# fish
git-mgr install-completions | source
```

The dynamic value predictors honor `GIT_MGR_CONFIG` / `GIT_MGR_STATE`, so
completion reflects the same state your commands use. Note: to complete workspace
subcommands, spell out `git-mgr workspace …` — the `ws` and `wg` aliases don't
expand their subcommands during completion.

## Configuration

Config lives at `~/.config/git-mgr/config.yaml`. See
[`config.example.yaml`](config.example.yaml) for a fully annotated reference.

If that default file doesn't exist, git-mgr runs on defaults and `GIT_MGR_*`
environment variables. Only what's switched on runs: discovery needs providers
and rules, workspaces exist once declared, background sync once the agent is
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
rules and mark every discovered repo stale. A key that isn't a setting -- a typo,
or one renamed since -- is an error too, with the closest setting suggested:
`"sync.timout_per_repo" is not a setting (did you mean "timeout_per_repo"?)`.

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
workspaces, adopt — so none lands in state or logs; use a credential helper. `git-mgr doctor` reports whether each
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

Workspace clones get the same keys as the repo they come from, so agents
commit as the right account; worktrees share their repo's config. If you set one of these keys in a repo yourself, git-mgr leaves it and
reports the difference (see [Config changes](#config-changes-plan-and-apply)).
Changing an account updates every repo it covers on the next `sync` or `apply`.

### Partial and shallow clones

Big repos can be cloned without every file's history: `clone.filter` makes a
partial clone (git fetches file contents as they are checked out) and
`clone.depth` keeps only recent commits, on every branch. Set them at the top,
per account (`accounts[].clone`) or per repo (`overrides[].clone`). They apply
to new clones; an existing clone keeps how it was made.

| Key | Default | Meaning |
| --- | --- | --- |
| `clone.filter` | `""` | `blob:none`, `blob:limit=<size>` or `tree:<depth>`; empty clones every object. |
| `clone.depth` | `0` | Commits of history per branch; 0 keeps all. |

Workspace worktrees share their repo's clone, filter and depth included.
Workspace clones can't be made from a partial clone, so `plan` blocks them.

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
it last set for every repo and workspace: path, origin URL, tags, the
discovering rule, the provider's repo ID, the copy ID, the repo-local git config
it wrote, and each workspace's agents and their copies. It also keeps what the last run found --
status, branch, the last fetch, backup and offsite push and their errors --
which is what `git-mgr status` shows. On every run it compares three things:

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
  - remove-copy      app/agent-3: ~/workspaces/app/agent-3 (no longer wanted; unique work archived first)   [needs git-mgr apply]
  + create-copy      app/agent-4: ~/workspaces/app/agent-4   [git-mgr apply or ws ensure]
Changed by hand, left alone (reconcile.drift: overwrite restores them):
  ! github.com/acme/tools origin URL: git-mgr set git@github.com:acme/tools.git, now git@work:acme/tools.git; config wants git@github.com:acme/tools.git

sync applies 3 of 6 on its own (reconcile.auto: safe); `git-mgr apply` makes them all.
```

| Config change | What happens |
| --- | --- |
| A new repo matches a rule or `repos:` entry | Cloned (`+ clone`). |
| `base_dir`, `path_template` or an override's path | The repo moves (`> move`); its linked worktrees, and the workspace clones that fetch or borrow objects from it, are reconnected. |
| `clone_protocol` | The origin URL changes (`~ url`). |
| An `ssh_key` or account added, changed or removed | `core.sshCommand`, `user.name` and `user.email` are written, changed or unset in repos (`~ config`) and workspace clones (`~ copy-config`). |
| A rule's tags, or the rule that finds a repo | Tags and rule are updated in state (`~ tags`). |
| A repo renamed or transferred upstream | Matched by the provider's repo ID: rekeyed, re-pointed, moved; its clone, work and backups stay (`~ rename`). |
| A tracked clone deleted by hand | Cloned again. |
| A workspace declared, or an agent or repo added to one | Created (`+ create-workspace`, `+ create-copy`); a repo it names by path or URL that isn't tracked is adopted or cloned first. |
| Agents, repos or a whole workspace removed from config | Each copy's unique work is archived, then it is removed (`- remove-copy`, `- remove-workspace`). |
| A workspace's `root` | Its copies move (`> move-copy`). |
| A workspace's `isolation` | Not changed in place (`x`): remove the workspace and apply again to make it the new way. |

Changes marked `>` and `-` move or delete directories that a shell, editor or
agent may be using, so they wait for `git-mgr apply`, which lists them and asks
first. Everything else is applied by `sync` and `discover --apply` as it comes
up. `sync` and `discover` handle repos only; workspaces change through `apply`
and `ws ensure`.

Repos added by hand (`clone`, `adopt`, `manifest import`) keep the path and URL
they were given; only their tags and managed git config follow config. A
workspace created from flags or adopted is kept as recorded: `ws ensure <name>`
makes its missing copies again, and nothing removes it but `ws rm`.

Changes to different repos run at once, up to `concurrency` doing disk work; a
repo's own changes run in order, and stop at the first that fails. Each change
is recorded in state as soon as it is made, and the changes recorded while one
state write runs share the next, so a first run that clones a thousand repos
writes the state a handful of times rather than a thousand. Workspace changes
run after the repos', one at a time, from a plan made afresh once the repos are
in place.

An interrupted run is safe to repeat. Every change looks at the disk first: a
clone that finished before the interruption is adopted, a move that finished is
recorded, a copy that was made is recorded.

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
  `backup`, `sync` and `ws backup` exit non-zero, is stored as
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

### Offsite: surviving a lost disk

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

`<namespace>` is `refs/git-mgr/<machine>/<copy>`: every machine and every copy
-- each agent's clone or worktree -- owns its refs, so copies on one branch
never overwrite each other, and git-mgr force-pushes nowhere else. Refs
outside `refs/heads` and `refs/tags` don't show in the host's branch lists and
don't trigger CI, but anyone who can read the remote can fetch them; point
`remote` at a private backup remote to keep WIP off a shared one.

| Key | Default | Meaning |
| --- | --- | --- |
| `remote_sync.enabled` | `false` | Sync takes copies offsite on its own. `git-mgr remote-sync` does it on demand either way. |
| `remote_sync.remote` | `origin` | A remote name, or a URL template (`git@backup:{{.Host}}/{{.FullName}}.git`) for a separate backup remote. |
| `remote_sync.wip` | `ref` | `ref` (hidden refs), `branch` (a visible branch per copy, `git-mgr/wip/<machine>/<copy>`: tracked changes only, on top of HEAD) or `off`. |
| `remote_sync.unpushed` | `push` | `push` (hidden refs), `bundle` (a local bundle in `backup.archive_dir/unpushed/`, which protects against a deleted branch but not a lost disk) or `off`. |
| `remote_sync.namespace` | `refs/git-mgr/{{.Machine}}/{{.Copy}}` | Must stay under `refs/`, outside branches and tags, and include `{{.Copy}}`. |
| `remote_sync.keep` | `720h` | How long a pushed WIP ref stays. |

Any of these can be set per account (`accounts[].remote_sync`) and per repo
(`overrides[].remote_sync`); a workspace copy follows the repo it is a copy of,
then its workspace's `remote_sync`. The machine name is chosen once (host name plus a random suffix) and kept
in the state file. The push runs after the fetch, on `sync.timeout_per_repo`.

```bash
git-mgr remote-sync --dry-run          # what would go offsite, from the latest backups
git-mgr remote-sync --repos "*/my-repo"
git-mgr remote-sync --list             # what every machine left offsite, with restore commands
git-mgr ws rm app --delete --offsite   # take each copy offsite before deleting it
```

Restoring on another machine needs only the remote:

```bash
git clone <remote> restored && cd restored
git fetch origin refs/git-mgr/<machine>/<copy>/heads/main:refs/heads/recovered
git checkout recovered
git fetch origin refs/git-mgr/<machine>/<copy>/wip/<id>
git stash apply --index FETCH_HEAD     # the uncommitted work, staged and unstaged as it was
```

### Deleting without losing work

Every command that deletes a clone or worktree — `remove --delete`,
`ws rm --delete`, `ws prune` and the `remove_and_delete` stale-cleanup policy —
first inspects it for work that exists nowhere else: changed or untracked files,
stashes, commits no remote has, commits on a detached HEAD and an unfinished
rebase or merge. If there is any, it is written to one git bundle in
`backup.archive_dir` (default `~/.local/share/git-mgr/archive/`) and verified with
`git bundle verify` before anything is deleted. For a worktree, its branches stay
in the source repo, so only the working tree and detached commits are archived.
Each checked-out submodule is inspected the same way, and gets its own bundle;
a clone's Git LFS objects are kept beside its bundle (hardlinked). Every archive
is recorded in the catalog, `git-mgr archives ls`.
If git-mgr can't inspect the repo or write the bundle, nothing is deleted and the
repo stays tracked. A repo that workspace copies are made from, or that has
linked worktrees, is never untracked or deleted until they are gone. Stale cleanup lists each repo it kept and why: `discover
--apply` prints them, and `sync` reports each as a failure (exit code 3) until a
later run deletes it.

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

## Workspaces

A *workspace* is a repo, or several related ones, worked on by several agents
at once. Each agent gets a *replica*: its own folder holding a *copy* of each of
the workspace's repos, on a branch of its own. Workspaces are independent of
each other; open several side by side when you work on unrelated repos at the
same time.

```yaml
agents:
  - name: claude
    command: claude
    resume: "claude --continue"

workspace_defaults:
  root: ~/workspaces
  agents: 4

workspaces:
  - name: app                       # ~/workspaces/app/agent-1 .. agent-4
    repos: [github.com/me/app]
  - name: platform                  # ~/workspaces/platform/agent-N/{api,web}
    repos: [github.com/me/api, github.com/me/web]
    agents: 2
  - name: notes                     # every agent works in ~/notes
    path: ~/notes
```

`git-mgr apply` (or `git-mgr ws ensure`) makes them. A workspace's repos are
tracked repo keys, globs over them, a named `set:`, local paths or URLs; a
local repo or URL git-mgr doesn't track yet is adopted where it is, or cloned
where config puts repos, first. Every copy is made from the repo's tracked
clone, its *source*, so nothing is downloaded twice:

| `isolation` | Each copy is | Disk | Notes |
| --- | --- | --- | --- |
| `worktree` (default) | a linked worktree of the source | the checkout only | Shares the source's objects, branches and fetches. Works with partial and shallow sources. |
| `clone` | a clone with objects of its own | hardlinked from the source (copied across filesystems) | Independent history; refreshed from the source without the network. |
| `reference-clone` | a clone borrowing the source's objects | the checkout and its own new objects | git-mgr sets `gc.pruneExpire=never` in the source so `git gc` there can't prune objects a copy uses; `doctor` checks it. |
| `shared` | none: every agent works in one folder | — | The workspace's `path`, or its one repo's clone. |

A clone's `origin` is the repo's upstream, and a `git-mgr` remote points at the
source (its branches show as `git-mgr/<branch>`). `git-mgr sync` fetches each
repo once and then brings its clones' view of origin up to date from the
source, without the network; `ws fetch` does the same for one workspace. A
source made with a partial clone filter (`clone.filter`) can only have
worktrees: a clone can't fetch the objects it lacks through it.

Each new copy gets the branch `agents/<workspace>/<agent>` (`branch:`), made at
the upstream's default branch (`base_ref:` to change it). A branch the remote
already has is checked out tracking it, so a copy made again resumes where its
agent left off. Existing copies keep their branch when the template changes.

Create one from the command line without editing config:

```bash
git-mgr ws create app --repo github.com/me/app --agents 4
git-mgr ws create review --set backend                 # every repo in a set, one agent
git-mgr ws create spike --repo ~/code/spike --agents 2 --isolation clone
git-mgr ws create app --repo github.com/me/app --agents 4 --save   # write it to config
git-mgr ws open app --agent claude                     # tmux session, a pane per agent
```

### Running agents

`git-mgr ws run <workspace> <agent>` runs an agent in its replica, in the
current terminal; every pane `ws open` makes runs it. It checks the agent's
copies, then types the agent profile's command into an interactive shell there
(`$SHELL -i -c`), so shell functions such as a `claudemore` wrapper work, with
`GIT_MGR_WORKSPACE`, `GIT_MGR_AGENT`, `GIT_MGR_REPLICA` and the profile's `env`
set. When the agent ran in the replica before, the profile's `resume` command
is typed instead (`--fresh` to start over). When the agent exits, its exit
status is recorded and a shell is left in the replica (`--no-shell` to exit
too).

While it runs, the agent holds a *lease*: a lock on
`<state dir>/runs/<workspace>/<agent>.lease` that the system releases when the
run ends, however it ends. A second `ws run` of the same agent is refused, and
no command removes a copy whose agent holds its lease (`ws rm --delete`,
`ws prune`, `remove-copy` on apply). `git-mgr ws stop <workspace> [agent]`
ends runs the way closing their terminal would -- SIGHUP to the agent and its
shell -- waiting up to `--timeout` (30s); `--kill` then kills what is left.

`ws status` shows each agent's state: `ready` (copies made, never run),
`running`, `stopping`, `stopped` (with its last exit status), `missing` (a
copy gone from disk) or `retiring` (a retire that didn't finish), then the
pull requests its copies opened and the agents recently retired.

### Publishing and retiring

When an agent's work is ready, `git-mgr ws publish <workspace> [agent...]`
pushes each of its copies' branches to origin, tracking them, and with `--pr`
opens a pull request per repo -- one per copy, against the repo's default
branch (`--base`), titled after the branch's last commit and listing its
commits (`--title`, `--body`, `--draft`). It runs `gh` for GitHub and `glab`
for GitLab with the token of the provider the repo was discovered through
(`GH_TOKEN`/`GH_HOST`, `GITLAB_TOKEN`/`GITLAB_HOST`), so the request is opened
by the right account; a request that is already open is found instead. For
other hosts it prints the compare URL. The request's URL is recorded and shown
by `ws status`.

When an agent is done, `git-mgr ws retire <workspace> <agent...>` (or
`--all`) removes its copies without losing anything:

1. It takes the agent's lease, so no run starts meanwhile; a running agent is
   kept unless `--stop` ends its run first.
2. Each copy is inspected afresh -- changed and untracked files, stashes,
   commits no remote has, a detached HEAD, an unfinished rebase or merge, and
   the same inside each submodule -- and what would be lost is written to a
   verified bundle in `backup.archive_dir`, each submodule's to its own; a
   clone's Git LFS objects, which a bundle only points to, are kept beside it.
3. The copy is removed, and a tombstone (where it was, its branch, its pull
   request, its archive) and the archive's catalog entry are kept in state.

`--force` discards the unique work instead. A copy that can't be inspected or
archived is kept, and its agent shows as `retiring` until a retire finishes.
`ws rm --delete`, `ws prune` and removals on `git-mgr apply` retire copies the
same way. `git-mgr archives ls` lists what was saved, from copies and from
repos `remove --delete` or stale cleanup deleted; `git-mgr archives show <id>`
prints the restore steps.

A workspace created from flags or adopted is kept as recorded: `ws ensure`
makes its missing copies again, and only `ws rm` removes it. Declared
workspaces follow config: agents or repos removed from a declaration, a new
`root`, or a removed declaration are moves and removals that wait for
`git-mgr apply`; each removed copy's unique work is archived first. Commands
that act on copies take `--agent` to pick one agent's.

| `ws` subcommand | Description |
|---|---|
| `create <name>` | Create a workspace from flags (`--repo`, `--set` or `--path`; `--agents`, `--isolation`, `--branch`, `--base-ref`, `--root`), or the one config declares. `--save` (`--force`) writes it to config; `--open` with `--agent`/`--cmd` opens it. |
| `ensure [name]` | Make what is missing of one workspace or all of them; list moves and removals for `git-mgr apply`. Idempotent. |
| `open <name>` | Open the agents with a launcher, as a fleet of one (see [Fleets](#fleets-and-launchers)), after making missing copies. `--launcher`, `--agent`, `--cmd`, `--fresh`, `--no-attach`, `--print`, `--no-ensure`. |
| `run <name> <agent>` | Run the agent in its replica in this terminal, under its lease, then leave a shell there. `--profile`, `--command`, `--fresh`, `--no-check`, `--no-shell`. |
| `stop <name> [agent]` | End running agents as closing their terminals would. `--timeout`, `--kill`. |
| `publish <name> [agent...]` | Push agents' branches; `--pr` opens a pull request per repo (`--draft`, `--base`, `--title`, `--body`). |
| `retire <name> <agent...>` | Archive the agents' copies' unique work, remove the copies, keep tombstones. `--all`, `--stop`, `--timeout`, `--force`, `--yes`. |
| `check <name>` | Exit non-zero when a copy is missing, on the wrong branch, mid-rebase or merge, or behind. `--agent`. |
| `status <name>` / `overview` | Agents' states and copies' status for one workspace / across all of them (`--json`). |
| `fetch <name>` | Fetch each source once, then refresh the clones from it. |
| `commit`, `pull`, `push`, `exec` | Run across copies. `pull` backs up dirty copies first. |
| `backup <name>` / `recover <name>` | Back up / restore each copy's WIP (`--offsite`; `recover --into`/`--apply`). |
| `prune <name>` | Remove copies with no unique work: clean, no stash, nothing unpushed, verified by a full inspection. `--dry-run`, `--all`. |
| `ls` | List workspaces, created and declared. |
| `rm <name>` | Forget the workspace, leaving its files. `--delete` retires every copy; `--force` discards unique work instead; `--offsite` takes each copy offsite first and keeps any it can't; `--stop` ends running agents first. |
| `adopt <name> <dir>` | Record a directory of existing checkouts as a workspace, one agent per checkout. |

`git-mgr sync` backs up every copy (and, with `remote_sync`, takes it
offsite) under the policy of the repo it is a copy of, then the workspace's
own `remote_sync`, after its repos are fetched.

| Key | Default | Meaning |
| --- | --- | --- |
| `agents[].name`, `.command` | `claude`, `codex` when on PATH | An agent profile. The command is typed into an interactive shell in the replica, so shell functions and aliases work. Declaring any replaces the detected ones. |
| `agents[].resume` | none | Typed instead of `command` when the agent ran in the replica before. |
| `agents[].env` | none | Extra environment for the agent. |
| `workspace_defaults.root` | `~/workspaces` | Holds a directory per workspace. |
| `workspace_defaults.isolation` | `worktree` | `worktree`, `clone`, `reference-clone` or `shared`. |
| `workspace_defaults.agents` | `1` | Agents per workspace (1–64). |
| `workspace_defaults.agent_name` | `agent-{{.N}}` | Names agent N (`{{.N}}`, `{{.Workspace}}`). |
| `workspace_defaults.branch` | `agents/{{.Workspace}}/{{.Agent}}` | A new copy's branch (`{{.Workspace}}`, `{{.Agent}}`, `{{.N}}`, `{{.Repo}}`). |
| `workspace_defaults.base_ref` | upstream's default branch | Where new branches start. |
| `workspace_defaults.agent` | the first profile | The agent profile workspaces' agents run. |
| `workspaces[].name` | required | Letters, digits, `-` and `_`. |
| `workspaces[].repos` / `.set` / `.path` | one required | The repos, or the shared folder. |
| `workspaces[].root`, `.isolation`, `.agents`, `.agent_name`, `.branch`, `.base_ref`, `.agent` | `workspace_defaults` | Per-workspace settings. |
| `workspaces[].remote_sync` | the repo's | `remote_sync` for the workspace's copies. |

## Fleets and launchers

A *fleet* opens several workspaces' agents side by side with one launcher:

```yaml
fleets:
  - name: daily
    workspaces: [app, platform, notes]
    agent: claude                     # optional: one profile for every agent
    launcher: tmux                    # tmux | iterm | muxy | auto | a plugin
```

```bash
git-mgr fleet open daily              # make what is missing, then open every agent
git-mgr fleet open daily --launcher iterm --fresh
git-mgr fleet open daily --print      # show the tmux commands / iTerm AppleScript
git-mgr fleet ls                      # fleets, running agents, last opened
git-mgr fleet status daily            # each workspace's agents and copies
git-mgr fleet stop daily              # end every running agent
git-mgr ws open app                   # one workspace, as a fleet of one
```

`fleet open` first makes what is missing of the fleet's declared workspaces,
as `ws ensure` does (`--no-ensure` skips it). Each agent's terminal runs
`git-mgr ws run <workspace> <agent>` (see [Running agents](#running-agents)),
so an agent that is already running elsewhere says so and leaves a shell.
When an account the workspaces' repos need has `signing.mode: required`, a
test signature is made first, and nothing opens if it fails: an agent that
can't sign headless would stall on its first commit.

**Workspaces on another machine.** With `host:` set, `fleet open` asks git-mgr
on that machine for the launch plan (`ssh <host> git-mgr fleet plan <name>
--json`; `remote_command:` if git-mgr needs another path or flags there), and
opens it here: tmux runs on the host through ssh, iTerm types `ssh -t <host>
…` into each split, and Muxy talks to the Muxy server on the host. The host
needs this git-mgr and the same workspaces. `fleet status` and `fleet stop` run
there too.

### Launchers

Every launcher gets the same plan; `launchers.title` names each terminal
(`{{.Fleet}}`, `{{.Workspace}}`, `{{.Agent}}`, `{{.Profile}}`), `check` makes
it check its copies first and `keep_shell` leaves a shell when the agent
exits. `launchers.default` (`auto`) picks tmux inside tmux, Muxy inside a Muxy
terminal, iTerm inside iTerm, else tmux.

**tmux**: a session per fleet, a window per workspace (or agent), a pane per
agent. Each pane is marked with its agent, so opening the fleet again adds only
the agents that aren't there (`on_exists: add-missing`); inside tmux it
switches the client to the session, inside iTerm it attaches with `tmux -CC`
so panes are native splits.

| `launchers.tmux` | Default | Effect |
| --- | --- | --- |
| `session` | `git-mgr-{{.Fleet}}` | Session name; `.` and `:` become `-`. |
| `window_per` | `workspace` | `workspace` or `agent`. |
| `layout` | `tiled` | `tiled`, `even-horizontal`, `even-vertical`, `main-vertical`, `main-horizontal`. |
| `max_panes` | `4` | Agents past this spill into `<workspace>-2`, `-3`, … |
| `pane_titles` | `true` | Each pane's border shows its title; agents can't retitle it. |
| `control_mode` | `auto` | `tmux -CC` inside iTerm; `always` or `never` to force. |
| `socket` | none | A separate tmux server (`-L`). |
| `on_exists` | `add-missing` | `attach`, `add-missing` or `recreate`. |
| `remain_on_exit` | `true` | Keep a pane whose shell exits. |

**iTerm**: a window per fleet, a tab per workspace (or agent), a split per
agent, driven with AppleScript (`osascript`). Needs macOS, iTerm2, and the
Automation permission for the terminal you run git-mgr in, which macOS asks for
the first time.

| `launchers.iterm` | Default | Effect |
| --- | --- | --- |
| `window` | `new` | `new`, or `current` to add tabs to the front window. |
| `tab_per` | `workspace` | `workspace` or `agent`. |
| `split` | `grid` | `grid` (√n columns, in reading order), `columns` or `rows`. |
| `max_panes` | `4` | Agents past this spill into another tab. |
| `profile` | `Default` | iTerm profile for new windows, tabs and splits. |
| `badge` | `{{.Agent}}` | Pane watermark; empty turns it off. |
| `focus` | `true` | Bring iTerm to the front. |
| `type_delay` | `150ms` | Pause before typing into a new split, while its shell starts. |

**Muxy**: a project per workspace (`muxy project add --reuse`) and a tab per
agent, on this machine or a Muxy server over SSH. Muxy has no command that
opens tabs in the desktop app, so `open` picks how agents appear: `layout`
writes `.muxy/layouts/<name>.json` into each project for Apply Layout;
`sessions` starts each agent that isn't running as a server session, listed
under Existing Terminals; `state` quits the desktop app, adds the tabs to
projects that have none in its saved state, and relaunches it, which starts
them. `state` only edits the state format it knows (version 3) and keeps the
previous file as `desktop-state.json.git-mgr-backup`.

| `launchers.muxy` | Default | Effect |
| --- | --- | --- |
| `host` | the fleet's host | SSH destination of the Muxy server. |
| `project_name` | `{{.Workspace}}` | Each project's name. |
| `open` | `layout` | `layout`, `sessions` or `state`. |
| `layout_name` | the agents' profile | Layout file name. |
| `restart` | `ask` | With `open: state`: `ask`, `always` or `never` quit the running app. |
| `color` | `auto` | `auto` (one per workspace), `none` or `#RRGGBB`. |
| `prune_projects` | `false` | Remove the projects made for workspaces no longer in the fleet (their files stay). |
| `command` | `muxy`, else the app's | The muxy CLI. |

**Plugins**: any other terminal is a `git-mgr-launch-<name>` executable on
`PATH`, named as a fleet's `launcher:`. It gets the launch plan as JSON on
stdin -- `fleet`, `host`, and per workspace its `name`, `dir` and `agents`,
each with `name`, `dir`, `title`, `profile`, `command` (the shell line to run)
and `state` -- plus `options` from `launchers.plugins.<name>`. `git-mgr fleet
plan <name> --json` prints the same plan.

## Running on a schedule

`git-mgr sync` is built to run unattended. Only one sync runs at a time; a
second one exits 0 at once. Clones, fetches and backups run without the state
lock, which a run takes only for the milliseconds it needs to record a result,
so `git-mgr ws create` or `git-mgr clone` work normally while a long sync is
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
| `sync.api_timeout` | `60s` | One provider API request attempt. |
| `sync.discovery_timeout` | `5m` | One discovery rule: listing plus language/topic lookups, retries and rate-limit waits. |
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

### With Nix: home-manager

`install-agent` refuses a binary in `/nix/store`; with home-manager, declare
the schedules instead, and they are rebuilt with the package on every switch.
The flake exposes the module as `homeManagerModules.default`:

```nix
{
  inputs.git-mgr.url = "github:ashtonian/git-mgr";
  # in your home-manager configuration:
  imports = [ inputs.git-mgr.homeManagerModules.default ];
  programs.git-mgr = {
    enable = true;
    package = pkgs.git-mgr;                       # your build of git-mgr
    path = with pkgs; [ git openssh gh ];         # what the background sync runs
    sshAuthSock = "%t/ssh-agent";                 # optional
    schedules.default.interval = "15m";
    schedules.work = { interval = "5m"; tag = "work"; };
    settings = { base_dir = "~/projects"; layout = "owner/name"; };   # optional
  };
}
```

| `programs.git-mgr` | Default | Meaning |
| --- | --- | --- |
| `enable` | `false` | Install git-mgr and its schedules. |
| `package` | required | The git-mgr package. |
| `settings` | `{}` | Written to `~/.config/git-mgr/config.yaml` when set; the file is then read-only, so `setup` and `ws create --save` can't edit it. |
| `configFile`, `stateFile` | git-mgr's defaults | Passed to the schedules as `--config`, `--state`. |
| `path` | `[ git openssh ]` | The schedules' `PATH`, with git-mgr itself. |
| `sshAuthSock` | none | The schedules' `SSH_AUTH_SOCK` (systemd specifiers work). |
| `logFile` | `~/.local/share/git-mgr/git-mgr.log` | launchd agents' output; systemd logs to the journal. |
| `schedules.<name>` | none | A background sync: `interval` (`15m`; dividing an hour or a day), `repos`, `tag`, `set`, `extraArgs`, `environment`. |

Each schedule is a systemd user service and timer (`git-mgr` for `default`,
else `git-mgr-<name>`) with `OnCalendar=`, `Persistent=true`, `Nice=10` and
idle I/O on Linux, and a launchd agent with the same priority on macOS: the
units `install-agent` writes. `git-mgr doctor` checks only schedules
`install-agent` recorded; check these with `systemctl --user list-timers` or
`launchctl list`.

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
| 3 | Partial failure: some repos, copies or changes failed and the rest succeeded. Each failure is printed to stderr. |
| 75 | Another git-mgr run held the state lock for longer than `lock_timeout`; try again (EX_TEMPFAIL). `git-mgr doctor` reports a stuck holder. |
| 80 | Usage error: unknown command or flag. |

## Resilience

GitHub and Gitea requests retry on rate limits (429, GitHub's primary and
secondary limits), 5xx responses and attempts that time out (the GitLab client
has equivalent retry built in). The wait a server asks for -- `Retry-After`, or
until `X-RateLimit-Reset` -- is honoured in full; one that would outlast the
rule's `discovery_timeout` fails the rule at once with the time the limit
lifts, rather than sleeping into the deadline. If one discovery rule fails, the others
still run, and stale detection is skipped for that run so a transient outage
can't mark healthy repos stale. A run filtered with `--tag` or `--rule` only
judges repos first found by the rules it ran, so `sync --tag personal` never
marks work repos stale; repos from a failed rule are left exactly as they are.
A rule whose result may be missing repos -- a search capped at 1000, a
language or topic lookup that failed or timed out -- still adds what it found,
but judges none of its repos stale that run. Repos added by hand (`clone`,
`adopt`, `repos:` entries) are never judged, and `cleanup.policy: ignore`
judges nothing. A stale repo is no longer fetched or pushed offsite, but its
local work is still backed up.
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
| `internal/reconcile` | Desired vs recorded vs on-disk for tracked repos, and the plan engine: applies changes, several repos at once, then serial ones. |
| `internal/state` | The state file: schema, migrations, locking, atomic writes, and `GroupCommit`, which saves concurrent changes in one write. |
| `internal/gitops` | Everything that runs git: clone, fetch, status, snapshots, backups, restore, archives, copy IDs, worktrees. |
| `internal/workspace` | Workspaces: resolving config into replicas and copies, planning their changes on the reconcile engine, making, moving and removing copies, health. Depends on the inventory, never the reverse. |
| `internal/repourl` | Parsing remote URLs into host/owner/name keys. |
| `internal/pathtemplate`, `internal/pathutil`, `internal/shellutil`, `internal/ctxutil` | Path templates, deletion guards, shell quoting, optional deadlines. |
| `internal/scheduler` | launchd, systemd and cron installation. |
| `internal/oplog`, `internal/notify` | The operation log and desktop notifications. |
| `nix/home-manager.nix`, `flake.nix` | The home-manager module: package, config and schedules as systemd timers or launchd agents. |

See [CONTRIBUTING.md](CONTRIBUTING.md) for the full workflow and
[SECURITY.md](SECURITY.md) for the security model and how to report issues.

## License

MIT — see [LICENSE](LICENSE).
