# Commands

[Documentation](README.md) · [Quick start](../README.md#quick-start)

- [JSON output](#json-output)
- [Exit codes](#exit-codes)
- [Environment variables](#environment-variables)

| Command | Description |
|---|---|
| `gitten setup` (`init`) | Set up gitten, or change your setup: layout, accounts, what to track (showing what it finds, to keep, pick from or change), features. `--yes`, `--answers`, `--save-answers`, `--dry-run`, `--offline`. |
| `gitten bootstrap` | Set up a fresh machine in one command: setup → doctor → discover --apply → sync → install agent. `--profile`, `--no-agent`, `--dry-run`. |
| `gitten discover` | Discover repos from providers and show the plan, changing nothing; `--apply` makes the changes that move and delete nothing (clone new repos, update URLs, managed git config, tags). `--rule`, `--tag` to scope. |
| `gitten sync` | Discover, apply config changes as far as `reconcile.auto` allows, fetch all repos, back up uncommitted changes and, with `remote_sync.enabled`, take them offsite (the cron command). `--no-backup`, `--no-fetch`, `--no-discover`, `--force`. |
| `gitten plan` | Show what config would change on disk and in state, without changing anything. `--json`. See [Config changes](RECONCILIATION.md#config-changes-plan-and-apply). |
| `gitten apply` | Make repos and workspaces match config, including the moves and removals sync leaves for you. Asks first; `--yes`. |
| `gitten backup` | Back up only (no fetch). `--list` to see backups, `--restore <id>` for instructions. |
| `gitten restore <repo> [id]` | Recover a backup: print steps (default), `--into <dir>` to clone+apply into a scratch dir, or `--apply` onto the tree (dirty-guarded). |
| `gitten top` | A live view of every workspace copy (agent state, terminal activity, branch, dirty, ahead/behind, last backup, PR) and every repo needing attention. See [Live view](SHELL.md#live-view-gitten-top). |
| `gitten status` | Table of managed repos. `--live` inspects working trees now (dirty + ahead/behind, no network); `--json`, `--dirty`, `--stale`, `--errors`, `--tag`, `--repos`. |
| `gitten doctor` | Diagnose tooling, auth (tokens/SSH keys), config, and repo-on-disk health. Read-only; `plan`/`apply` fix what config can. |
| `gitten clone <url>` | Clone and track one repo by hand, at its templated path or `--path`. `--tag`. A repo added by hand keeps the path and URL it was given. |
| `gitten clone --org\|--user\|--group\|--mine\|--query …` | Find a set of repos -- narrowed by `--name` (words or globs), `--name-pattern`, `--language`, `--topic`, `--visibility`, `--forks`, `--archived` -- list it, and on a terminal ask: clone them all, pick from the list, or none. Offers to save the selection as a discovery rule, so every sync clones what newly matches (`--save <name>`, `--no-save`); `--dry-run` only lists. See [Finding your repos](DISCOVERY.md#finding-your-repos). |
| `gitten adopt <path>` | Track an existing clone where it is. `--scan` for batch, `--dry-run`, `--tag`. |
| `gitten remove <pattern>` | Stop tracking repos. `--tag`/`--set`, `--yes`. `--delete` also removes the clone from disk after archiving its unique work (see [Deleting without losing work](ARCHIVES.md#deleting-without-losing-work)); `--archive` moves the clone, whole, into the archive instead (see [Archiving instead of deleting](ARCHIVES.md#archiving-instead-of-deleting)). |
| `gitten run -- <cmd>` | Run a command in each matching repo (parallel). `--repos`/`--tag`/`--set`, `--fail-fast`. |
| `gitten manifest export\|import\|save` | Portable `{url,tags,path}` manifest: export and import to rehydrate a new machine, `save` to put the repos tracked by hand here into the shared manifest. See [Several machines](CONFIGURATION.md#several-machines). |
| `gitten remote-sync` | Back up and take WIP and unpushed commits offsite now, under `remote_sync`. `--repos`, `--tag`, `--set`, `--dry-run`; `--list` shows what every machine left offsite and how to restore it. |
| `gitten config` | Print resolved config. `--validate`, `--edit`, `--path`, `--for-repo <key>`, `--redact`. |
| `gitten report` / `log` | Summarize / list recent activity (clones, backups, syncs…) from the operation log. `--since`, `-n`. |
| `gitten workspace` (`ws`, `wg`) | Manage workspaces: repos × agents, each agent in a copy of its own. See [Workspaces](WORKSPACES.md#workspaces). |
| `gitten fleet` | Open several workspaces' agents side by side with tmux, iTerm, Muxy or a plugin; `open`, `plan`, `ls`, `status`, `stop`. See [Fleets](LAUNCHERS.md#fleets-and-launchers). |
| `gitten mirror` | Make or update full mirrors of repos' upstreams now; `sync` does it every `mirror.interval`. `--repos`/`--tag`/`--set`, `--force`. See [Mirrors](BACKUPS.md#mirrors-surviving-the-host). |
| `gitten hooks post-clone\|post-create` | Run repos' `post_clone` or a workspace's `post_create` hooks again, output on the terminal. See [Hooks](CONFIGURATION.md#hooks). |
| `gitten accounts ls\|gitconfig` | List accounts and their folders; make each account's identity your git's own under its folders with `includeIf` (`--write`, `--install`). See [Accounts](AUTHENTICATION.md#accounts). |
| `gitten archives ls\|show\|restore\|rm` | The catalog of what was saved before copies and repos were taken away -- bundles of unique work, and whole clones -- and how to restore each; `restore` moves an archived clone back or rebuilds a clone from a bundle, `rm` deletes archives for good. |
| `gitten cd\|path\|open <query>` | Jump to, print, or open in your editor (`--web`: the repo's page) a repo, workspace or agent's folder from part of its name. See [Jump and open](SHELL.md#jump-and-open). |
| `gitten shell-init bash\|zsh\|fish` | Print the shell integration: the function that makes `gitten cd` change directory, and tab completion. `setup` adds it to your shell. |
| `gitten install-agent` | Install, list (`--list`) or remove (`--uninstall`) background syncs (launchd/systemd/cron); `--name` and `--repos`/`--tag`/`--set` for several. |
| `gitten install-completions` | Print tab completion alone, for your login shell. See [Shell completions](SHELL.md#shell-completions). |
| `gitten version` | Print version, commit, build date. |

Global flags: `--config`, `--state`, `--verbose`/`-v`, `--quiet`/`-q`,
`--concurrency`/`-j`, `--json`.

## JSON output

`--json`, on any command, prints the command's result as one JSON document on
stdout -- a list, a status, or what a command changed -- and sends everything
printed for people to stderr, as quietly as `--quiet` makes it. The exit code
says what happened (see [Exit codes](#exit-codes)), so a script reads stdout
only when it wants the detail:

```bash
gitten sync --json | jq '.copies[] | select(.ok | not)'
gitten ws status app --json | jq -r '.[] | select(.agent_state == "running") | .label'
gitten doctor --json > /dev/null || echo "doctor found problems (exit $?)"
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
stderr. `--help` under `--json` prints `{"help": "..."}`, the text it prints
on stderr. A command with nothing to report prints `{}`.

## Exit codes

Exit codes are stable, so a scheduler or script can tell a flaky repo from a
broken setup. With `--quiet`, only failures are printed, to stderr.

| Code | Meaning |
| --- | --- |
| 0 | Everything succeeded. `sync` also exits 0 when another run holds the lock, since overlapping scheduled runs are routine. |
| 1 | The command failed: bad config, unreadable state, every target failed, or it has no `--json` output or needs `--yes`. |
| 2 | A check ran and found problems: `doctor`, `ws check`. |
| 3 | Partial failure: some repos, copies or changes failed and the rest succeeded, or `bootstrap` finished but couldn't install its schedule. Each failure is printed to stderr. |
| 75 | Another gitten run held the state lock, or a repo's lock (`.git/gitten.lock`, held while a repo is deleted, moved or copied into a workspace), for longer than `lock_timeout`, and nothing else was done; try again (EX_TEMPFAIL). When other targets succeeded meanwhile it is a partial failure (3), and when others failed for another reason, a failure (1). `gitten doctor` reports a stuck state-lock holder; `lsof <repo>/.git/gitten.lock` shows a repo lock's. |
| 80 | Usage error: unknown command or flag. |

## Environment variables

Every setting can be set by an environment variable: `GITTEN_` and the
setting's key, upper-cased, with `.` as `_` -- `GITTEN_BASE_DIR` sets
`base_dir`, `GITTEN_SYNC_TIMEOUT_PER_REPO` sets `sync.timeout_per_repo`. A
variable overrides the config file, and a flag that sets the same setting
(`--concurrency`) overrides both; whichever set it, the value is validated as
if it were in the file. A list takes comma-separated values. A map entry is
`GITTEN_<MAP>_<KEY>` with its key lower-cased, so a map whose keys need
capitals -- `workspace_defaults.env`, keyed by variable names -- belongs in the
file. Settings inside lists of entries -- `providers`, `accounts`,
`discovery`, `workspaces` -- are the file's alone. A `GITTEN_*` variable that names no setting is ignored,
so the variables gitten sets for agents (`GITTEN_REPO`, `GITTEN_AGENT`, ...)
never change a gitten run inside a workspace. Notable ones:

| Variable | Effect |
|---|---|
| `GITTEN_CONFIG` | Config file path (same as `--config`), which must exist; unset, `~/.config/gitten/config.yaml` is read if it exists. |
| `GITTEN_STATE` | State file path (same as `--state`); unset, `$XDG_DATA_HOME/gitten/state.yaml` (`~/.local/share/gitten/state.yaml`). |
| `XDG_CONFIG_HOME`, `XDG_DATA_HOME`, `XDG_CACHE_HOME` | Where the default config, data and cache directories are (see [Configuration](CONFIGURATION.md#configuration)). |
| `GITTEN_BASE_DIR` | Override `base_dir`. |
| `GITHUB_TOKEN` / `GITLAB_TOKEN` / `GITEA_TOKEN` / `FORGEJO_TOKEN` | Default provider tokens. |
