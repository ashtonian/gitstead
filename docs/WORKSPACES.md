# Workspaces

[Documentation](README.md) · [Quick start](../README.md#quick-start)

- [Running agents](#running-agents)
- [Agent environment: ports, env and shared caches](#agent-environment-ports-env-and-shared-caches)
- [When an agent is waiting for you](#when-an-agent-is-waiting-for-you)
- [Publishing and retiring](#publishing-and-retiring)

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

`gitten apply` (or `gitten ws ensure`) makes them. A workspace's repos are
tracked repo keys, globs over them, a named `set:`, local paths or URLs; a
local repo or URL gitten doesn't track yet is adopted where it is, or cloned
where config puts repos, first. Every copy is made from the repo's tracked
clone, its *source*, so nothing is downloaded twice:

| `isolation` | Each copy is | Disk | Notes |
| --- | --- | --- | --- |
| `worktree` (default) | a linked worktree of the source | the checkout only | Shares the source's objects, branches and fetches. Works with partial and shallow sources. |
| `clone` | a clone with objects of its own | hardlinked from the source (copied across filesystems) | Independent history; refreshed from the source without the network. |
| `reference-clone` | a clone borrowing the source's objects | the checkout and its own new objects | gitten sets `gc.pruneExpire=never` in the source so `git gc` there can't prune objects a copy uses; `doctor` checks it. |
| `shared` | none: every agent works in one folder | — | The workspace's `path`, or its one repo's clone. |

A clone's `origin` is the repo's upstream, and a `gitten` remote points at the
source (its branches show as `gitten/<branch>`). `gitten sync` fetches each
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
gitten ws create app --repo github.com/me/app --agents 4
gitten ws create review --set backend                 # every repo in a set, one agent
gitten ws create spike --repo ~/code/spike --agents 2 --isolation clone
gitten ws create app --repo github.com/me/app --agents 4 --save   # write it to config
gitten ws open app --agent claude                     # tmux session, a pane per agent
```

## Running agents

`gitten ws run <workspace> <agent>` runs an agent in its replica, in the
current terminal; every pane `ws open` makes runs it. It checks the agent's
copies, then types the agent profile's command into an interactive shell there
(`$SHELL -i -c`), so shell functions such as a wrapper around `claude` work, with
the profile's `env` and then the agent's environment set (see below). When the
agent ran in the replica before, the profile's `resume` command
is typed instead (`--fresh` to start over). When the agent exits, its exit
status is recorded and a shell is left in the replica (`--no-shell` to exit
too).

While it runs, the agent holds a *lease*: a lock on
`<state dir>/runs/<workspace>/<agent>.lease` that the system releases when the
run ends, however it ends. A second `ws run` of the same agent is refused, and
no command removes a copy whose agent holds its lease (`ws rm --delete`,
`ws prune`, `remove-copy` on apply). `gitten ws stop <workspace> [agent]`
ends runs the way closing their terminal would -- SIGHUP to the agent and its
shell -- waiting up to `--timeout` (30s); `--kill` then kills what is left.

`ws status` shows each agent's state: `ready` (copies made, never run),
`running` (and since when it has been waiting for you, if it is), `stopping`,
`stopped` (with its last exit status), `missing` (a copy gone from disk) or
`retiring` (a retire that didn't finish), then the pull requests its copies
opened and the agents recently retired.

## Agent environment: ports, env and shared caches

Agents working side by side collide on the same port, database and build
output. Each agent's commands -- the agent `ws run` starts, the shell it
leaves, `ws exec`, `post_create` hooks -- run with an environment of its own:

| Variable | Value |
| --- | --- |
| `GITTEN_WORKSPACE`, `GITTEN_AGENT`, `GITTEN_AGENT_N`, `GITTEN_REPLICA` | The workspace, the agent, its number (from 1) and its folder |
| `GITTEN_PORT`, `GITTEN_PORTS` | The first of the agent's own block of ports, and how many |
| `GITTEN_CACHE` | A directory the workspace's agents share for build caches |

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
| `workspace_defaults.env` | `{}` | Env templates for every agent; a workspace's `env` adds to them, name by name. `GITTEN_*` names are gitten's. |
| `workspace_defaults.cache_root` | `$XDG_CACHE_HOME/gitten/workspaces` | Holds `<workspace>/`, the cache its agents share. |

An agent gets its block the first time it needs one, the lowest block from
`base` that no other agent on the machine holds and nothing is listening on,
and keeps it for its folder's life, so a dev server's URL doesn't change
between runs. The shared cache directory goes when the workspace does
(`ws rm --delete`, or a declared workspace removed on apply); point
tools that share a cache safely between concurrent builds at it -- ccache,
sccache, Turborepo, Nx -- rather than at a build output directory.

`gitten ws env` prints an agent's environment as shell exports, for a shell
or editor of your own in its folder: `eval "$(gitten ws env)"` in an agent's
folder works out which agent it is (`ws env <workspace> <agent>` names one;
`--json` for an object). With direnv, `.envrc` in a replica can hold that
line.

## When an agent is waiting for you

gitten notices an agent waiting for you two ways, records it for `ws status`
and `gitten top`, and with `notify.enabled` sends a desktop notification
(`notify.on_waiting`, on by default).

- **The agent says so.** Agents can run a command when they need you; point
  it at `gitten ws waiting`, which reads which agent from the environment
  `ws run` sets and does nothing outside one, so it is safe in your global
  agent settings. Claude Code, in `~/.claude/settings.json`:

  ```json
  {
    "hooks": {
      "Notification": [{"hooks": [{"type": "command", "command": "gitten ws waiting"}]}],
      "Stop": [{"hooks": [{"type": "command", "command": "gitten ws waiting"}]}],
      "UserPromptSubmit": [{"hooks": [{"type": "command", "command": "gitten ws waiting --clear"}]}]
    }
  }
  ```

  Codex, at the top of `~/.codex/config.toml`, before any `[table]`:
  `notify = ["gitten", "ws", "waiting"]`. The message comes from what the
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

## Publishing and retiring

When an agent's work is ready, `gitten ws publish <workspace> [agent...]`
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

When an agent is done, `gitten ws retire <workspace> <agent...>` (or
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
archived is kept, and its agent shows as `retiring` until a retire finishes;
meanwhile `ws ensure` and `apply` leave the agent alone and report it, so a
retire cut short is never undone by making its copies again.
`ws rm --delete`, `ws prune` and removals on `gitten apply` retire copies the
same way. `gitten archives ls` lists what was saved, from copies and from
repos `remove --delete`, `remove --archive` or stale cleanup took away;
`gitten archives show <id>` prints the restore steps.

A workspace created from flags or adopted is kept as recorded: `ws ensure`
makes its missing copies again, including those `ws create` couldn't make
(recorded first, as not made yet; such a copy doesn't keep its repo from being
removed), and only `ws rm` removes it. Declared
workspaces follow config: agents or repos removed from a declaration, a new
`root`, or a removed declaration are moves and removals that wait for
`gitten apply`; each removed copy's unique work is archived first. Commands
that act on copies take `--agent` to pick one agent's.

| `ws` subcommand | Description |
|---|---|
| `create <name>` | Create a workspace from flags (`--repo`, `--set` or `--path`; `--agents`, `--isolation`, `--branch`, `--base-ref`, `--root`), or the one config declares. `--save` (`--force`) writes it to config; `--open` with `--agent`/`--cmd` opens it. |
| `ensure [name]` | Make what is missing of one workspace or all of them; list moves and removals for `gitten apply`. Idempotent. |
| `open <name>` | Open the agents with a launcher, as a fleet of one (see [Fleets](LAUNCHERS.md#fleets-and-launchers)), after making missing copies. `--launcher`, `--agent`, `--cmd`, `--fresh`, `--no-attach`, `--print`, `--no-ensure`. |
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

`gitten sync` backs up every copy (and, with `remote_sync`, takes it
offsite) under the policy of the repo it is a copy of, then the workspace's
own `remote_sync`, after its repos are fetched.

| Key | Default | Meaning |
| --- | --- | --- |
| `agents[].name`, `.command` | `claude`, `codex` when on PATH | An agent profile. The command is typed into an interactive shell in the replica, so shell functions and aliases work. Declaring any replaces the detected ones. |
| `agents[].resume` | none | Typed instead of `command` when the agent ran in the replica before. |
| `agents[].env` | none | Extra environment for the agent. |
| `agents[].status` | none | A status probe: a shell command run in a running agent's replica, with its environment, within `sync.inspect_timeout`, whose first line of output `ws status` and `fleet status` show as the agent's status (`agent_status` under `--json`) -- what it is working on, say. A probe that prints nothing and fails, or runs out of time, shows why; one that prints shows its first line whatever its exit status. |
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
