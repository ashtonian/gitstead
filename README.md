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
  sync → agent) plus a shared manifest, so repos added by hand on one machine
  reach the others ([Several machines](#several-machines)); `doctor` diagnoses tooling/auth/state before things break.
- **Structured layout** — repos land at `{{.Host}}/{{.Owner}}/{{.Name}}` (fully
  templatable). Operate them in bulk with named **sets** (`--set`) and
  `run -- <cmd>`.
- **Clone shapes for big repos** — partial and shallow clones, sparse checkout,
  submodules and Git LFS skipping, per account or repo, for the repo's clone
  and every workspace copy alike ([Clone options](#clone-options)).
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

There is no release yet. git-mgr's config loader is
[pkg](https://github.com/ashtonian/pkg)'s, which `go.mod` takes from a
checkout beside this one (`replace => ../pkg/config`) until it is published as a
module of its own; `go install`, the release archives, the Homebrew cask and
the Docker image all start with the first release after that. Until then,
build it from source:

```bash
git clone https://github.com/ashtonian/pkg
git clone https://github.com/ashtonian/git-mgr
cd git-mgr && go install .        # Go version: see go.mod
```

Once released:

```bash
brew install --cask ashtonian/tap/git-mgr
go install github.com/ashtonian/git-mgr@latest
docker pull ashtonian/git-mgr     # linux/amd64, linux/arm64
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
| `git-mgr top` | A live view of every workspace copy (agent state, terminal activity, branch, dirty, ahead/behind, last backup, PR) and every repo needing attention. See [Live view](#live-view-git-mgr-top). |
| `git-mgr status` | Table of managed repos. `--live` inspects working trees now (dirty + ahead/behind, no network); `--json`, `--dirty`, `--stale`, `--errors`, `--tag`, `--repos`. |
| `git-mgr doctor` | Diagnose tooling, auth (tokens/SSH keys), config, and repo-on-disk health. Read-only; `plan`/`apply` fix what config can. |
| `git-mgr clone <url>` | Clone and track one repo by hand, at its templated path or `--path`. `--tag`. A repo added by hand keeps the path and URL it was given. |
| `git-mgr adopt <path>` | Track an existing clone where it is. `--scan` for batch, `--dry-run`, `--tag`. |
| `git-mgr remove <pattern>` | Stop tracking repos. `--tag`/`--set`, `--yes`. `--delete` also removes the clone from disk after archiving its unique work (see [Deleting without losing work](#deleting-without-losing-work)); `--archive` moves the clone, whole, into the archive instead (see [Archiving instead of deleting](#archiving-instead-of-deleting)). |
| `git-mgr run -- <cmd>` | Run a command in each matching repo (parallel). `--repos`/`--tag`/`--set`, `--fail-fast`. |
| `git-mgr manifest export\|import\|save` | Portable `{url,tags,path}` manifest: export and import to rehydrate a new machine, `save` to put the repos tracked by hand here into the shared manifest. See [Several machines](#several-machines). |
| `git-mgr remote-sync` | Back up and take WIP and unpushed commits offsite now, under `remote_sync`. `--repos`, `--tag`, `--set`, `--dry-run`; `--list` shows what every machine left offsite and how to restore it. |
| `git-mgr config` | Print resolved config. `--validate`, `--edit`, `--path`, `--for-repo <key>`, `--redact`. |
| `git-mgr report` / `log` | Summarize / list recent activity (clones, backups, syncs…) from the operation log. `--since`, `-n`. |
| `git-mgr workspace` (`ws`, `wg`) | Manage workspaces: repos × agents, each agent in a copy of its own. See [Workspaces](#workspaces). |
| `git-mgr fleet` | Open several workspaces' agents side by side with tmux, iTerm, Muxy or a plugin; `open`, `plan`, `ls`, `status`, `stop`. See [Fleets](#fleets-and-launchers). |
| `git-mgr mirror` | Make or update full mirrors of repos' upstreams now; `sync` does it every `mirror.interval`. `--repos`/`--tag`/`--set`, `--force`. See [Mirrors](#mirrors-surviving-the-host). |
| `git-mgr hooks post-clone\|post-create` | Run repos' `post_clone` or a workspace's `post_create` hooks again, output on the terminal. See [Hooks](#hooks). |
| `git-mgr accounts ls\|gitconfig` | List accounts and their folders; make each account's identity your git's own under its folders with `includeIf` (`--write`, `--install`). See [Accounts](#accounts). |
| `git-mgr archives ls\|show\|restore\|rm` | The catalog of what was saved before copies and repos were taken away -- bundles of unique work, and whole clones -- and how to restore each; `restore` moves an archived clone back or rebuilds a clone from a bundle, `rm` deletes archives for good. |
| `git-mgr cd\|path\|open <query>` | Jump to, print, or open in your editor (`--web`: the repo's page) a repo, workspace or agent's folder from part of its name. See [Jump and open](#jump-and-open). |
| `git-mgr shell-init bash\|zsh\|fish` | Print the shell function that makes `git-mgr cd` change directory. |
| `git-mgr install-agent` | Install, list (`--list`) or remove (`--uninstall`) background syncs (launchd/systemd/cron); `--name` and `--repos`/`--tag`/`--set` for several. |
| `git-mgr install-completions` | Print a shell completion script (bash/zsh/fish). See [Shell completions](#shell-completions). |
| `git-mgr version` | Print version, commit, build date. |

Global flags: `--config`, `--state`, `--verbose`/`-v`, `--quiet`/`-q`,
`--concurrency`/`-j`, `--json`.

### JSON output

`--json`, on any command, prints the command's result as one JSON document on
stdout -- a list, a status, or what a command changed -- and sends everything
printed for people to stderr, as quietly as `--quiet` makes it. The exit code
says what happened (see [Exit codes](#exit-codes)), so a script reads stdout
only when it wants the detail:

```bash
git-mgr sync --json | jq '.copies[] | select(.ok | not)'
git-mgr ws status app --json | jq -r '.[] | select(.agent_state == "running") | .label'
git-mgr doctor --json > /dev/null || echo "doctor found problems (exit $?)"
```

Commands that apply changes -- `apply`, `clone`, `adopt`, `discover --apply`,
`ws create`, `ws ensure`, `manifest import` -- print `changes` (each with its
`kind`, `target`, `ok`, `info` or `error`), `drift` and `blocked`, the same
actions `plan --json` shows. `sync` prints those with every repo and copy's
backup, fetch and offsite push, its mirrors and what went stale; `bootstrap`
one object of its steps' documents. Commands that would ask first (`remove`,
`apply` with moves, `ws rm`, `ws retire`, `ws prune`, `archives rm`) need
`--yes` under `--json`, and fail without it. Commands that take the terminal
or only change your shell -- `ws run`, `open`, `shell-init`, `config --edit`,
`ws create --open`, interactive `setup` -- refuse `--json`; `ws open` and
`fleet open` under `--json` open without attaching tmux and print what they
opened (`--print --json` prints the launch plan). A fleet on another host
passes `--json` there and its document through. `setup` includes the schedule
it installed as `schedule`.

Whatever happens, stdout holds exactly one document. A command that fails
before it has a result to print -- a config that doesn't load, a repo that
doesn't match, a refused `--json` -- prints `{"error": "...", "exit_code": N}`;
so does a command line that doesn't parse (exit 80), whose usage goes to
stderr, as `--help` does under `--json`. A command with nothing to report
prints `{}`.

## Live view: git-mgr top

`git-mgr top` is a full-screen view that looks again every `--every` (10s):

```
git-mgr top  14:03:22  ·  12 copies, 5 agents running  ·  140 repos, 3 need attention

COPY           AGENT    ACTIVITY         BRANCH              STATE  AHEAD/BEHIND  BACKUP  NOTES
app/agent-1    running  printed 8s ago   agents/app/agent-1  dirty  3/0           2m ago
app/agent-2    running  quiet ≥ 4m       agents/app/agent-2  clean  1/0           1h ago  https://github.com/me/app/pull/12
worker/agent-1 stopped  -                agents/worker/...   clean  0/2           1d ago  behind origin/HEAD by 2

REPO                  STATE  BRANCH  AHEAD/BEHIND  FETCHED  BACKUP  NOTES
github.com/me/notes   dirty  main    0/0           9m ago   9m ago
github.com/acme/api   clean  main    0/0           2d ago   -       fetch: Permission denied (publickey)
```

Every workspace copy shows its agent's state (see [Running agents](#running-agents))
and what its terminal shows. In tmux, an agent alone in its window shows when
the window last printed; agents sharing a window show when their pane's
screen was last seen to change (`quiet ≥ 4m` until a change is seen), so an
agent waiting for you stands out. In Muxy, `muxy activity list` gives each
agent's own state. Repos show only when they need a look -- not clean, ahead,
behind, or failing to fetch or back up -- unless you press `a` (or pass
`--all`). Everything is read from disk, never the network; `--repos`, `--tag`
and `--set` narrow the repos. `q` quits, `r` looks again now. `--once` prints
one view (also what happens when output isn't a terminal) and `--json` one
snapshot.

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

git-mgr follows the XDG base directories: its config in
`$XDG_CONFIG_HOME/git-mgr`, its state, operation log, backups, archives,
mirrors and scheduled-sync log in `$XDG_DATA_HOME/git-mgr`, and workspaces'
shared caches in `$XDG_CACHE_HOME/git-mgr`, each when the variable is set to
an absolute path, else in `~/.config/git-mgr`, `~/.local/share/git-mgr` and
`~/.cache/git-mgr` -- the defaults shown throughout this README.

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
-- even `~/.config/git-mgr/config.yaml` itself -- must exist and parse; only
with neither may the default file be missing, meaning the defaults: a missing, unreadable or malformed file is an error rather
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

A repo's key and `{{.Host}}` are its host without a port --
`gitea.company.com/acme/app` for a provider at `https://gitea.company.com:3000`
-- the same key a clone of it gets when adopted, over SSH or HTTPS, and the
host an account's `hosts:` names.

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
workspaces, adopt — so none lands in state or logs; use a credential helper.
One put into a clone's remote by hand afterwards is kept out too: remote URLs
git-mgr writes down (archive and backup descriptions) are stored without it,
plans show it redacted, and `--json` output redacts any URL credential.
`git-mgr doctor` reports whether each provider's token resolves and whether
configured SSH keys exist.

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

git-mgr writes those keys into the repos it tracks. For repos you clone
yourself into an account's folders, `git-mgr accounts gitconfig` makes the
same settings your git's own, with
[`includeIf`](https://git-scm.com/docs/git-config#_conditional_includes):

```bash
git-mgr accounts ls                    # accounts, what they cover, their folders
git-mgr accounts gitconfig             # print what it would write
git-mgr accounts gitconfig --write     # write it to ~/.config/git-mgr/
git-mgr accounts gitconfig --install   # and include it from your global git config
```

An account's folders are where config puts its repos: for each of its hosts
and owners, the leading part of their path that doesn't depend on the repo's
name (`~/repos/github.com/acme` under the default layout, `~/work/acme` with
a `base_dir` of its own). Each account with something to set gets
`~/.config/git-mgr/accounts/<name>-<digest>.gitconfig` (the name made a file
name; the digest of the name keeps apart names that make the same one), and
`~/.config/git-mgr/accounts.gitconfig` holds an `[includeIf "gitdir:<folder>/"]`
for each folder, deeper folders last so an owner's account wins over its
host's. git writes both files, quoting what it must, and a folder's wildcard
characters (`*`, `?`, `[`) are escaped so it matches only itself. A folder two accounts share is left out with a warning, and under
`layout: name`, where every repo sits directly in `base_dir`, there are no
folders to tell accounts apart by. `--install` adds one `include.path` line
to your global git config, once; nothing else touches it. Run it again after
changing accounts.

### Clone options

`clone` shapes how much of a repo git-mgr fetches and checks out: its history
(`filter`, `depth`), which directories (`sparse`), its submodules and its Git
LFS content. Set it at the top, per account (`accounts[].clone`) or per repo
(`overrides[].clone`); each level overrides the one before, field by field.

| Key | Default | Meaning |
| --- | --- | --- |
| `clone.filter` | `""` | Partial clone: `blob:none`, `blob:limit=<size>` or `tree:<depth>`; git fetches file contents as they are checked out. Empty clones every object. |
| `clone.depth` | `0` | Commits of history per branch; 0 keeps all. |
| `clone.sparse` | `[]` | Check out only these directories (cone mode: the files at the top, and directly in each listed directory's parents, come too). Relative to the repo's top, no wildcards. Empty checks out everything; an override's `sparse: []` undoes a sparse default. |
| `clone.submodules` | `false` | Check out submodules, recursively -- only those inside `sparse` -- cloned with `filter` and `depth` too. |
| `clone.lfs` | `auto` | `auto`: Git LFS downloads what each checkout needs. `skip`: LFS files stay pointers and nothing is downloaded, kept as `lfs.fetchexclude=*` in the repo's git config. |

```yaml
overrides:
  - repos: ["github.com/acme/monorepo"]
    clone:
      filter: blob:none            # with sparse: files outside it are never downloaded
      sparse: [services/api, libs/auth]
      submodules: true
      lfs: skip
```

`filter`, `depth`, `sparse` and `submodules` shape new checkouts: the repo's
clone, and every workspace copy made from it -- worktree or clone -- which
takes the shape config gives now, whatever the repo's own checkout looks like.
An existing clone keeps how it was made; change it with `git sparse-checkout`
or `git submodule update --init` yourself. `lfs: skip` is managed git config,
so it reaches existing clones too: `plan` shows it and the next sync or
`apply` writes it, and their later checkouts skip LFS content.

A sparse checkout is made empty and filled only once its directories are set,
so nothing outside them is ever checked out -- or, in a partial clone,
downloaded. git's own `submodule update` ignores the sparse cone, so git-mgr
names it the submodules inside. A clone or copy whose submodules can't all be
fetched (one you can't reach, say) is kept and recorded, and the failure
reported once, rather than cloned again on every sync; `git submodule update
--init --recursive` there finishes it. Workspace clones hardlink the repo's
Git LFS objects, so their checkout downloads none of them again. Submodules
with `filter` need git 2.36 or later.

Workspace worktrees share their repo's clone, filter and depth included.
Workspace clones can't be made from a partial clone, so `plan` blocks them.

### Hooks

Hooks are commands git-mgr runs in a checkout it has just made: `post_clone`
in a repo's new clone, `post_create` in a new workspace copy -- install
dependencies, `direnv allow`, copy a `.env` the repo doesn't track.

```yaml
hooks:
  post_clone: ["direnv allow"]
overrides:
  - repos: ["github.com/acme/web"]
    hooks:
      post_clone: ["direnv allow", "npm ci"]
      post_create: ["npm ci"]          # in every copy of web, in any workspace
workspace_defaults:
  hooks:
    post_create: ['cp "$GIT_MGR_SOURCE/.env" .env', "direnv allow"]
```

| Key | Default | Meaning |
| --- | --- | --- |
| `hooks.post_clone` | `[]` | Run in a repo's new clone. Never in a clone adopted where it was. |
| `hooks.post_create` | `[]` | Run in each new workspace copy of a repo, before the workspace's own. |
| `hooks.timeout` | `15m` | Each command's limit; 0 for none. |
| `workspace_defaults.hooks.post_create` | `[]` | A workspace's own post_create, after its repos'; `workspaces[].hooks.post_create` replaces it. |

`hooks` is set at the top, per account (`accounts[].hooks`) or per repo
(`overrides[].hooks`); the most specific list wins and an empty list runs
nothing. A copy runs its repo's `post_create`, then its workspace's. Each
command runs with `/bin/sh -c` in the checkout, in order, with no input and
no terminal, in a process group of its own that the timeout kills whole. Each
is told where it is:

| Variable | Value |
| --- | --- |
| `GIT_MGR_EVENT` | `post_clone` or `post_create` |
| `GIT_MGR_PATH` | The checkout (also the working directory) |
| `GIT_MGR_REPO` | The tracked repo's key |
| `GIT_MGR_SOURCE` | The repo's clone -- where a copy's untracked files can come from |
| `GIT_MGR_WORKSPACE`, `GIT_MGR_AGENT`, `GIT_MGR_BRANCH` | The copy's workspace, agent and branch (`post_create`) |

The first command that fails stops the rest. The checkout is kept and
recorded either way -- a failed `npm ci` doesn't cost the clone -- and the
failure is reported with the end of its output; the whole run is in
`<state dir>/hooks/<checkout>.<event>.log` (the last run of each). Fix it and
run the hooks again with `git-mgr hooks post-clone <repos>` or `git-mgr hooks
post-create <workspace> [agents]`. Moving a copy or a repo runs nothing.
Hooks come only from your config, never from a repository, so cloning
someone else's repo never runs their code.

### Manual repos, excludes, overrides, tags

```yaml
# Track individual repos without a discovery rule.
repos:
  - url: "git@github.com:someone/useful-fork.git"
    tags: ["libs"]
  - url: "git@github.com:me/notes.git"
    path: "~/notes"                    # not where path_template puts it: absolute,
                                       # ~-relative, or relative to its base_dir

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
behavior. Every setting an override sets is checked as the setting itself is,
and when several overrides match a repo the later one wins, for every setting.

Repo patterns -- in `overrides`, `sets`, workspaces' `repos` and `--repos` --
are a repo key (`host/owner/name`) or a glob over keys, in which `*` matches
any run of characters, slashes included, `?` one character and `[...]`
(`[!...]`) one in (or out of) a class: `ghes.company.com/*` is every repo on
that host, `github.com/acme/*` every repo of acme, subgroups included, and
`*/api` every repo named api. A malformed pattern fails config validation. The operation log can be turned off with `log: { events: false }`.

Tags come from discovery rules or manual entries and filter any command. For
recurring selections, define a **named set** and use `--set` (it composes with
`--repos`/`--tag`). A repo is in a set when any of its `repos` globs, `tags`,
`accounts` (the account covering the repo) or `rules` (the discovery rule that
found it) selects it, or when the set names none of them; `exclude_archived`
then drops archived repos. An unknown account or rule fails validation.

```yaml
sets:
  - name: core
    repos: ["github.com/acme/*"]
  - name: active-backend
    tags: ["backend"]
    exclude_archived: true
  - name: work
    accounts: [work]
  - name: from-gitlab
    rules: [gitlab-groups]
```

```bash
git-mgr status --tag work
git-mgr sync   --set core
git-mgr run    --set active-backend -- go test ./...
```

### Several machines

One config, kept in your dotfiles, gives every machine the same discovery
rules, `repos:`, overrides and declared workspaces. What it can't hold is
what you add by hand -- `git-mgr clone`, `adopt`, `manifest import` -- which
lives in one machine's state. Point `manifest:` at a file every machine can
read and write -- beside the config in a dotfiles repo, or in a synced folder
-- and git-mgr keeps it as the list of repos tracked by hand:

```yaml
manifest: ~/dotfiles/git-mgr/repos.yaml
```

- `clone`, `adopt` (and `adopt --scan`) and `manifest import` add an entry:
  the repo's URL, the tags you gave it, and its path when it isn't where
  `path_template` puts it -- relative to its `base_dir` when under it,
  `~/...` under your home, else absolute -- so it lands in the same place on
  every machine -- and the `account` or `ssh_key` an imported entry binds it
  to.
- Every machine reads the manifest as more `repos:` entries, so its next
  `sync` clones what another machine added.
- `git-mgr remove` takes the entry out, so no other machine clones it again;
  machines that already have it keep tracking it, as with a `repos:` entry you
  delete, until you remove it there too.
- `git-mgr manifest save` adds the repos this machine tracked by hand before
  the manifest existed; without `manifest:` it prints them as `repos:`
  entries to paste into config. `git-mgr doctor` names repos tracked by hand
  here that neither lists.

The config file itself is never written, so this works when it is
generated -- by the home-manager module, say -- and read-only. Without
`manifest:`, `manifest import` keeps an entry's `account` or `ssh_key` in
config's `repos:`, edited in place, since state can't hold it: a read-only
config fails that entry, naming what to add. `manifest export` writes each
repo's binding back out. Writes take a lock in your cache directory, never beside the manifest, and replace the file atomically, so a sync tool
copying it never sees half a file; two machines writing at the same moment
through a sync tool can still conflict, as any synced file can, and the
next `manifest save` puts back an entry a conflict lost.

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
- **Submodules too.** Each checked-out submodule with uncommitted work is
  backed up as a working copy of its own, and its parent's backup records
  which of the submodule's backups goes with it: `restore --apply` restores
  them in place, `restore --into` as clones of their own in their places.
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

A submodule's uncommitted work is backed up locally with its parent's (see
above) but not taken offsite; commit and push it in the submodule.

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

### Mirrors: surviving the host

Offsite refs protect your work; a mirror protects the repo itself. With
`mirror` on, git-mgr keeps a full mirror of each repo's upstream -- every
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
| `mirror.dir` | `~/.local/share/git-mgr/mirrors` | Holds `<host>/<owner>/<name>.git`. |
| `mirror.interval` | `24h` | How long `sync` leaves a mirror before updating it again; a failing one is retried every sync. |
| `mirror.lfs` | `false` | Also mirror Git LFS content (`git lfs fetch --all`); per account or repo too. |
| `mirror.keep_rewritten` | `2160h` (90 days) | How long a mirror keeps the old tip of a ref its upstream rewrote (a force-pushed branch), under `refs/git-mgr-rewritten/<time>/`; `0s` keeps none. |

A mirror is made with `git clone --mirror`, staged like any clone, from the
repo's upstream URL with its SSH key -- not from your clone, which may be
partial, shallow or behind. Updates fetch every ref and never prune, whatever
`fetch.prune` says: a branch deleted upstream stays in the mirror. A branch
moved upstream follows the remote; when the move rewrote its history -- a
force-push -- the old tip is kept as `refs/git-mgr-rewritten/<time>/heads/<branch>`
for `mirror.keep_rewritten`, so the rewritten commits stay too. `sync` updates the mirrors that are due after its
fetches (`--force` all of them, `--no-fetch` none); `git-mgr mirror` does it
now (`--repos`, `--tag`, `--set`, `--force`). A stale repo's mirror is left as
it was: its upstream is gone, which is what the mirror is for. A renamed repo's
mirror moves with it; git-mgr never deletes a mirror. Failing mirrors show in
`git-mgr status`, `git-mgr top` and `doctor`.

To use one: `git clone /Volumes/backup/git-mirrors/github.com/acme/app.git`
for a working copy, or `git push --mirror <new remote>` from it to re-host.

### Deleting without losing work

Every command that deletes a clone or worktree — `remove --delete`,
`ws rm --delete`, `ws retire`, `ws prune` and the `remove_and_delete`
stale-cleanup policy — first inspects it for work that exists nowhere else:
changed or untracked files, stashes, commits no remote has (on a branch, a
detached HEAD, a tag or any other ref of its own) and an unfinished rebase or
merge. If there is any, it is written to one git bundle in `backup.archive_dir`
(default `~/.local/share/git-mgr/archive/`) and verified before anything is
deleted. Each checked-out submodule is inspected the same way, and gets its own
bundle; a clone's Git LFS objects are kept beside its bundle (hardlinked).
Every archive is recorded in the catalog, `git-mgr archives ls`.

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
counts too: delete it once no git command is running there. If git-mgr can't
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

`git-mgr archives restore <id> --to <dir>` rebuilds a clone from a bundle:
its branches, tags and remotes, HEAD, the stash list, the index, working tree
and untracked files, its LFS objects, and its submodules' archives in their
places. The bundle stays in the catalog. Each bundle has a `.json` file beside
it recording the source path, remotes, branch, what was saved and how to
restore it by hand:

```bash
git init restored && cd restored     # a bundle that leans on a repo: git clone <that repo> restored
git fetch ~/.local/share/git-mgr/archive/<name>-<time>.bundle '+refs/*:refs/restored/*'
git for-each-ref refs/restored   # heads/*, tags/*, git-mgr/archive/<id>/{head,worktree,stash-N}
```

The `worktree` ref is a stash-shaped commit of the index, working tree and
untracked files; `git stash apply --index <ref>` brings them back. Archives are
never pruned automatically; `git-mgr archives rm <id>` deletes one for good.

### Archiving instead of deleting

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
nothing in the archive. `adopt --scan` skips the archive directory.

```bash
git-mgr archives ls                    # ID, where it was, what it held
git-mgr archives restore <id>          # move it back and track it again (a bundle: rebuild a clone from it)
git-mgr archives restore <id> --to ~/scratch/app   # the repo is tracked again meanwhile
git-mgr archives rm <id>               # delete it for good
```

A restored clone is tracked by hand, so discovery never judges it stale
again. If its repo is tracked again meanwhile -- found upstream once more and
cloned afresh -- `restore` refuses its old place and `--to` puts it elsewhere,
untracked.

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
the profile's `env` and then the agent's environment set (see below). When the
agent ran in the replica before, the profile's `resume` command
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
`running` (and since when it has been waiting for you, if it is), `stopping`,
`stopped` (with its last exit status), `missing` (a copy gone from disk) or
`retiring` (a retire that didn't finish), then the pull requests its copies
opened and the agents recently retired.

### Agent environment: ports, env and shared caches

Agents working side by side collide on the same port, database and build
output. Each agent's commands -- the agent `ws run` starts, the shell it
leaves, `ws exec`, `post_create` hooks -- run with an environment of its own:

| Variable | Value |
| --- | --- |
| `GIT_MGR_WORKSPACE`, `GIT_MGR_AGENT`, `GIT_MGR_AGENT_N`, `GIT_MGR_REPLICA` | The workspace, the agent, its number (from 1) and its folder |
| `GIT_MGR_PORT`, `GIT_MGR_PORTS` | The first of the agent's own block of ports, and how many |
| `GIT_MGR_CACHE` | A directory the workspace's agents share for build caches |

Then comes the workspace's `env`, whose values are templates of the same:
`{{.Workspace}}`, `{{.Agent}}`, `{{.N}}`, `{{.Replica}}`, `{{.Port}}`,
`{{.Ports}}`, `{{.Cache}}`, and `{{add .Port 1}}` for the next port.

```yaml
workspace_defaults:
  ports: {base: 41000, count: 10}       # agent-1 41000-41009, agent-2 41010-41019, ...
  env:
    PORT: "{{.Port}}"                   # the dev server
    API_PORT: "{{add .Port 1}}"
    DATABASE_URL: "postgres://localhost/app_{{.N}}"
    TURBO_CACHE_DIR: "{{.Cache}}/turbo"
    SCCACHE_DIR: "{{.Cache}}/sccache"
workspaces:
  - name: app
    repos: [github.com/acme/app]
    agents: 4
    env: {NODE_ENV: test}               # added to the defaults', name by name
```

| Key | Default | Meaning |
| --- | --- | --- |
| `workspace_defaults.ports.base` | `41000` | The lowest port handed out; 0 hands out none. |
| `workspace_defaults.ports.count` | `10` | Ports per agent. `workspaces[].ports` overrides either. |
| `workspace_defaults.env` | `{}` | Env templates for every agent; a workspace's `env` adds to them, name by name. `GIT_MGR_*` names are git-mgr's. |
| `workspace_defaults.cache_root` | `$XDG_CACHE_HOME/git-mgr/workspaces` | Holds `<workspace>/`, the cache its agents share. |

An agent gets its block the first time it needs one, the lowest block from
`base` that no other agent on the machine holds and nothing is listening on,
and keeps it for its folder's life, so a dev server's URL doesn't change
between runs. The shared cache directory goes when the workspace does
(`ws rm --delete`, or a declared workspace removed on apply); point
tools that share a cache safely between concurrent builds at it -- ccache,
sccache, Turborepo, Nx -- rather than at a build output directory.

`git-mgr ws env` prints an agent's environment as shell exports, for a shell
or editor of your own in its folder: `eval "$(git-mgr ws env)"` in an agent's
folder works out which agent it is (`ws env <workspace> <agent>` names one;
`--json` for an object). With direnv, `.envrc` in a replica can hold that
line.

### When an agent is waiting for you

git-mgr notices an agent waiting for you two ways, records it for `ws status`
and `git-mgr top`, and with `notify.enabled` sends a desktop notification
(`notify.on_waiting`, on by default).

- **The agent says so.** Agents can run a command when they need you; point
  it at `git-mgr ws waiting`, which reads which agent from the environment
  `ws run` sets and does nothing outside one, so it is safe in your global
  agent settings. Claude Code, in `~/.claude/settings.json`:

  ```json
  {
    "hooks": {
      "Notification": [{"hooks": [{"type": "command", "command": "git-mgr ws waiting"}]}],
      "Stop": [{"hooks": [{"type": "command", "command": "git-mgr ws waiting"}]}],
      "UserPromptSubmit": [{"hooks": [{"type": "command", "command": "git-mgr ws waiting --clear"}]}]
    }
  }
  ```

  Codex, at the top of `~/.codex/config.toml`, before any `[table]`:
  `notify = ["git-mgr", "ws", "waiting"]`. The message comes from what the
  agent passes (Claude's on stdin, Codex's as an argument): "Claude needs your
  permission to use Bash", or "finished: …" and the first line of its last
  reply when it ends its turn. `--message` sets one yourself. `ws waiting`
  prints nothing on stdout -- Claude Code would add it to the prompt -- and
  always exits 0, so it never blocks an agent.
- **Its pane goes quiet.** For agents in tmux, `ws run` watches its own pane:
  output for 5s or more -- working -- then none for `notify.waiting_after`
  (30s) counts as waiting, and output again clears it. The screen an agent
  prints as it starts doesn't count. `waiting_after: 0` turns watching off.
  Muxy tracks and notifies about its agents itself.

A wait the agent reported isn't reported again by its pane, and one reported
twice within 10 seconds notifies once.

### Publishing and retiring

When an agent's work is ready, `git-mgr ws publish <workspace> [agent...]`
pushes each of its copies' branches to origin, tracking them, and with `--pr`
opens a pull request per repo -- one per copy, against the repo's default
branch (`--base`), titled after the branch's last commit and listing its
commits (`--title`, `--body`, `--draft`). It runs `gh` for GitHub and `glab`
for GitLab with the token of the provider that acts for the repo, so the
request is opened by the right account: the provider whose rule discovered
the repo, else the one bound to the repo's account on its host
(`providers[].account`), else the only one on its host.

| Host | gh/glab gets |
| --- | --- |
| github.com, `*.ghe.com` | `GH_TOKEN` (and `GH_HOST` for `*.ghe.com`) |
| GitHub Enterprise Server | `GH_ENTERPRISE_TOKEN`, `GH_HOST` |
| GitLab | `GITLAB_TOKEN`, `GITLAB_HOST` |

The token is the only one gh or glab sees: `GH_TOKEN`, `GITHUB_TOKEN`,
`GH_ENTERPRISE_TOKEN`, `GITHUB_ENTERPRISE_TOKEN`, `GITLAB_TOKEN`,
`GITLAB_ACCESS_TOKEN` and `OAUTH_TOKEN` from your shell are left out. Nothing
is opened -- the copy reports why -- when that provider's token doesn't
resolve, when several providers serve the host and nothing says which acts
for the repo, or when no provider serves the host but several accounts cover
it, since gh's or glab's own login may be another account's. With one
account, or none, and no provider, their own login is used. A request that is
already open is found instead. For other hosts it prints the compare URL. The
request's URL is recorded and shown by `ws status`.

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
repos `remove --delete`, `remove --archive` or stale cleanup took away;
`git-mgr archives show <id>` prints the restore steps.

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
| `agents[].status` | none | A status probe: a shell command run in a running agent's replica, with its environment, within `sync.inspect_timeout`, whose first line of output `ws status` and `fleet status` show as the agent's status (`agent_status` under `--json`) -- what it is working on, say. A failing probe shows why. |
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
can't sign headless would stall on its first commit. `ws run` makes the same
check for its agent's copies (`--no-check` skips it, with the copy checks).

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
picks up new repos. It never merges, it updates a branch only if you ask
(`sync.update`, below), and it moves or deletes a repo only if you configure
it to (`reconcile.auto: all`, `cleanup.policy: remove_and_delete` or
`archive`).

With `sync.update: ff-only-clean` (off by default; `setup` offers it), sync
fast-forwards each tracked clone it fetched to its upstream, when that loses
nothing: the branch is behind and not ahead, no tracked file has changes, and
no git command, rebase or merge is under way in it. Untracked and ignored
files stay: git refuses rather than overwrite one, an ignored file the
upstream starts tracking included. A clone left behind is reported with
why (`update_skipped` under `--json`); workspace copies, on agents' own
branches, are never updated. `overrides[].sync.update` sets it per repo.

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
| `sync.timeout_per_repo` | `5m` | One repo's fetch, retries included, and its fast-forward under `sync.update`. |
| `sync.clone_timeout` | `30m` | One clone; one archive of a clone or copy, and its deletion, before it is removed (stale cleanup, `remove --delete`/`--archive`, `ws retire`, `ws prune`). |
| `sync.inspect_timeout` | `1m` | One look at a working copy -- its git config and identity -- while planning (`plan`, `apply`, `sync`, `ws ensure`). |
| `sync.api_timeout` | `60s` | One provider API request attempt. |
| `sync.discovery_timeout` | `5m` | One discovery rule: listing plus language/topic lookups, retries and rate-limit waits. |
| `lock_timeout` | `2m` | How long a command waits for another run's state write, or for a repo another command is deleting, moving or making a workspace copy of, before exiting 75. |
| `token_command` | `30s` (fixed) | Running a provider's `token_command`. |
| `notify.timeout` | `10s` | Sending one desktop notification, which can hang without a desktop session. |

A clone is made in a hidden `.git-mgr-clone-*` directory beside its
destination and renamed into place when complete. A failed or killed clone
never leaves half a repository where the repo belongs, and the next clone of
the same repo removes what a killed run left behind.

### Logging and notifications

Logs go to stderr as JSON (`slog`), and to `log.file` as well when it is set;
the operation log, `events.jsonl` beside the state file, is what `git-mgr
report` and `git-mgr log` read. `--verbose` logs debug on the terminal and in
the file; `--quiet` (and `--json`) keeps only errors on the terminal, while the
file keeps `log.level`.

| Setting | Default | Meaning |
| --- | --- | --- |
| `log.level` | `info` | `debug`, `info`, `warn` or `error`. |
| `log.format` | `json` | `json` or `text`. |
| `log.file` | `""` | Also append logs here (made `0600`, its directory `0700`); empty for stderr only. A file that can't be opened leaves logging on stderr and says so. |
| `log.events` | `true` | Write the operation log. |
| `notify.enabled` | `false` | Desktop notifications (`osascript` on macOS, `notify-send` on Linux); nothing elsewhere. |
| `notify.on_error` | `true` | When a sync has failures. |
| `notify.on_stale` | `true` | When a sync finds repos gone upstream and marks them stale. |
| `notify.on_completion` | `false` | After every sync, with what it fetched, backed up and failed. |
| `notify.on_waiting` | `true` | When an agent is waiting for you (see [When an agent is waiting](#when-an-agent-is-waiting-for-you)). |
| `notify.waiting_after` | `30s` | How long a working agent's pane must be quiet to count as waiting. |
| `notify.timeout` | `10s` | How long a notification may take to send; a notification is never worth stalling a run. |

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

## Exit codes

Exit codes are stable, so a scheduler or script can tell a flaky repo from a
broken setup. With `--quiet`, only failures are printed, to stderr.

| Code | Meaning |
| --- | --- |
| 0 | Everything succeeded. `sync` also exits 0 when another run holds the lock, since overlapping scheduled runs are routine. |
| 1 | The command failed: bad config, unreadable state, every target failed, or it has no `--json` output or needs `--yes`. |
| 2 | A check ran and found problems: `doctor`, `ws check`. |
| 3 | Partial failure: some repos, copies or changes failed and the rest succeeded, or `bootstrap` finished but couldn't install its schedule. Each failure is printed to stderr. |
| 75 | Another git-mgr run held the state lock, or a repo's lock (`.git/git-mgr.lock`, held while a repo is deleted, moved or copied into a workspace), for longer than `lock_timeout`; try again (EX_TEMPFAIL). `git-mgr doctor` reports a stuck state-lock holder; `lsof <repo>/.git/git-mgr.lock` shows a repo lock's. |
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
| `GIT_MGR_CONFIG` | Config file path (same as `--config`), which must exist; unset, `~/.config/git-mgr/config.yaml` is read if it exists. |
| `GIT_MGR_STATE` | State file path (same as `--state`); unset, `$XDG_DATA_HOME/git-mgr/state.yaml` (`~/.local/share/git-mgr/state.yaml`). |
| `XDG_CONFIG_HOME`, `XDG_DATA_HOME`, `XDG_CACHE_HOME` | Where the default config, data and cache directories are (see [Configuration](#configuration)). |
| `GIT_MGR_BASE_DIR` | Override `base_dir`. |
| `GITHUB_TOKEN` / `GITLAB_TOKEN` / `GITEA_TOKEN` / `FORGEJO_TOKEN` | Default provider tokens. |

## Docker

From the first release, `ashtonian/git-mgr` runs git-mgr with git in a
container:

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
| `internal/hook` | Running `post_clone` and `post_create` hooks: environment, timeouts, logs. |
| `internal/agent` | Running agents: profile commands, the lease that marks an agent running, and `ws run` itself. |
| `internal/launch` | Fleets' terminals: the launch plan, and the tmux, iTerm, Muxy and plugin launchers. |
| `internal/activity` | Reading agents' terminal activity (tmux panes, Muxy), and watching a pane for an agent waiting. |
| `internal/view` | `git-mgr top`: collecting every repo and copy's state and terminal activity, and laying it out. |
| `internal/jump` | Matching a short query to a repo, workspace, agent or copy, for `cd`, `path` and `open`. |
| `internal/setup` | `git-mgr setup`'s logic: detecting what the machine has, proposing answers, rendering them into config. |
| `internal/repourl` | Parsing remote URLs into host/owner/name keys. |
| `internal/pathtemplate`, `internal/pathutil`, `internal/shellutil`, `internal/ctxutil` | Path templates, deletion guards and moves across filesystems, shell quoting, optional deadlines. |
| `internal/scheduler` | launchd, systemd and cron installation. |
| `internal/oplog`, `internal/notify` | The operation log and desktop notifications. |
| `nix/home-manager.nix`, `flake.nix` | The home-manager module: package, config and schedules as systemd timers or launchd agents. |

See [CONTRIBUTING.md](CONTRIBUTING.md) for the full workflow and
[SECURITY.md](SECURITY.md) for the security model and how to report issues.

## License

MIT — see [LICENSE](LICENSE).
