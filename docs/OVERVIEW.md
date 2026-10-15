# Overview and scope

[Documentation](README.md) · [Quick start](../README.md#quick-start)

- [Highlights](#highlights)
- [What it does, and what it doesn't](#what-it-does-and-what-it-doesnt)

Pick repos by org, user or search, by words in their names, language or
topics -- see what that finds, pick from the list, and they land in one layout
with the right account per repo. `gitten setup` asks a few questions and clones
what your accounts track; `gitten clone --org acme --name billing` adds a set
later, the same way ([Finding your repos](DISCOVERY.md#finding-your-repos)). From then on
a background sync fetches everything, clones what newly matches, follows
renames and transfers, and flags what disappears upstream -- and backs up
uncommitted work without ever touching your branches, working tree or index.
A new machine is one command away.

For coding agents it makes **workspaces** -- a repo, or several related ones,
with each agent in a copy of its own -- and launches them side by side in
tmux, iTerm or Muxy, their work backed up and never deleted until it's
archived.

## Highlights

- **Discovery that keeps up** — GitHub, GitHub Enterprise, GitLab (incl.
  self-hosted), and Gitea/Forgejo, by user, org, group or raw search, filtered
  by words in the name (`billing` matches `billing-api`, not `rebilling`),
  globs, language, topics, visibility, forks and archived. Every search shows
  what it finds before anything is cloned, with how many repos each name
  matched, and lets you pick from the list. Saved as rules, every sync clones
  what newly matches, follows renames and transfers, and flags what
  disappears ([Finding your repos](DISCOVERY.md#finding-your-repos)).
- **Quick setup** — `gitten setup` asks a few questions and clones; `gitten
  bootstrap` sets up a machine that already has a config, and a shared
  manifest carries repos added by hand from machine to machine
  ([Several machines](CONFIGURATION.md#several-machines)); `doctor` diagnoses tooling, auth and
  state before things break.
- **The right account everywhere** — each repo gets its account's identity,
  SSH key and signing, so you and your agents commit as the right person.
- **Structured layout** — repos land at `{{.Host}}/{{.Owner}}/{{.Name}}` (fully
  templatable). Operate them in bulk with named **sets** (`--set`) and
  `run -- <cmd>`.
- **Clone shapes for big repos** — partial and shallow clones, sparse checkout,
  submodules and Git LFS skipping, per account or repo, for the repo's clone
  and every workspace copy alike ([Clone options](CONFIGURATION.md#clone-options)).
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

## What it does, and what it doesn't

gitten **does**:

- find your repositories on GitHub, GitHub Enterprise, GitLab and
  Gitea/Forgejo, by owner, organization, group or search, for any number of
  accounts, and clone them into one layout with each account's identity, SSH
  key and signing written into the repos it covers;
- keep them current -- fetch, and optionally fast-forward clean clones -- on a
  schedule, and bring them in line when config changes (`plan`, `apply`);
- back up uncommitted work without touching your working tree, index or
  branches, locally and optionally offsite, and restore it exactly;
- delete nothing until what only it holds is archived and verified;
- put several coding agents on a repo at once, each in a copy of its own, and
  open them side by side.

It **doesn't**:

- pull, merge, rebase or commit on your branches (the one exception is the
  opt-in fast-forward of a clone with no local work), or push them (offsite
  copies go to hidden refs; `ws publish` pushes agents' branches when you ask);
- create, change or delete anything on a forge, beyond the pull requests `ws
  publish --pr` opens;
- run as a server or daemon: the host's scheduler runs `gitten sync`;
- support Bitbucket or Windows: it runs on macOS and Linux.
