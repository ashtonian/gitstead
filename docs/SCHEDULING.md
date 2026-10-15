# Running on a schedule

[Documentation](README.md) · [Quick start](../README.md#quick-start)

- [Logging and notifications](#logging-and-notifications)
- [Installing a schedule](#installing-a-schedule)
- [With Nix: home-manager](#with-nix-home-manager)

`gitten sync` is built to run unattended. Only one sync runs at a time; a
second one exits 0 at once. Clones, fetches and backups run without the state
lock, which a run takes only for the milliseconds it needs to record a result,
so `gitten ws create` or `gitten clone` work normally while a long sync is
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
| `sync.retries` | `2` | Further attempts at a failed fetch (0 to 10), within `sync.timeout_per_repo`. A fetch that finds git's lock taken isn't retried; the next run tries again. |
| `sync.retry_backoff` | `2s` | The wait before the first retry, doubling before each one after. |
| `lock_timeout` | `2m` | How long a command waits for another run's state write, or for a repo another command is deleting, moving or making a workspace copy of, before exiting 75. |
| `token_command` | `30s` (fixed) | Running a provider's `token_command`. |
| `notify.timeout` | `10s` | Sending one desktop notification, which can hang without a desktop session. |

A clone is made in a hidden `.gitten-clone-*` directory beside its
destination and renamed into place when complete. A failed or killed clone
never leaves half a repository where the repo belongs, and the next clone of
the same repo removes what a killed run left behind.

## Logging and notifications

Logs go to stderr as JSON (`slog`), and to `log.file` as well when it is set;
the operation log, `events.jsonl` beside the state file, is what `gitten
report` and `gitten log` read. `--verbose` logs debug on the terminal and in
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
| `notify.on_waiting` | `true` | When an agent is waiting for you (see [When an agent is waiting](WORKSPACES.md#when-an-agent-is-waiting-for-you)). |
| `notify.waiting_after` | `30s` | How long a working agent's pane must be quiet to count as waiting. |
| `notify.timeout` | `10s` | How long a notification may take to send; a notification is never worth stalling a run. |

## Installing a schedule

```bash
gitten install-agent                                   # every 15m: launchd (macOS) / systemd timer (Linux)
gitten install-agent --name work --tag work --interval 5m
gitten install-agent --list
gitten install-agent --uninstall --name work
gitten install-agent --print                           # show the unit files and commands; change nothing
```

Each schedule has a name (`default` unless you give one) and its own
selection of repos, so a work schedule and a personal one live side by side;
installing a name again replaces it. `bootstrap --profile work` installs a
`work` schedule that syncs `--tag work`. Schedules are recorded in the state
file, and `gitten doctor` checks each one.

| Flag | Default | Meaning |
| --- | --- | --- |
| `--name` | `default` | Schedule name: lowercase letters, digits, dashes. |
| `--interval` | `15m` | Must divide an hour or a day (5m, 10m, 15m, 20m, 30m, 1h, 2h, 3h, 4h, 6h, 8h, 12h, 24h), so every backend runs it exactly. |
| `--repos`, `--tag`, `--set` | all repos | What the schedule syncs. |
| `--kind` | per OS | `launchd`, `systemd` or `cron`. |
| `--bin` | the `gitten` on PATH | Binary the schedule runs. |
| `--daemon` | off | macOS: a LaunchDaemon that runs as you while nobody is logged in. Installed with `sudo`. |
| `--linger` | off | Linux: `loginctl enable-linger`, so the timer runs while you are logged out. |
| `--log` | `~/.local/share/gitten/gitten.log` | Output of launchd and cron runs; systemd runs log to the journal (`journalctl --user -u gitten`). |

What install does, so a background run behaves like one from your shell:

- **Binary:** the `gitten` on your PATH when it is the binary you ran,
  because a package manager's link there (`/opt/homebrew/bin`,
  `~/.nix-profile/bin`) survives upgrades. A path an upgrade or garbage
  collection removes -- `/nix/store`, a Homebrew `Cellar`, a temporary build
  -- is refused; put `gitten` on PATH or pass `--bin`.
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
  (`# com.ashtonian.gitten[.<name>]`) are touched.

## With Nix: home-manager

`install-agent` refuses a binary in `/nix/store`; with home-manager, declare
the schedules instead, and they are rebuilt with the package on every switch.
The flake exposes the module as `homeManagerModules.default`:

```nix
{
  inputs.gitten.url = "github:ashtonian/gitten";
  # in your home-manager configuration:
  imports = [ inputs.gitten.homeManagerModules.default ];
  programs.gitten = {
    enable = true;
    package = pkgs.gitten;                       # your build of gitten
    path = with pkgs; [ git openssh gh ];         # what the background sync runs
    sshAuthSock = "%t/ssh-agent";                 # optional
    schedules.default.interval = "15m";
    schedules.work = { interval = "5m"; tag = "work"; };
    settings = { base_dir = "~/projects"; layout = "owner/name"; };   # optional
  };
}
```

| `programs.gitten` | Default | Meaning |
| --- | --- | --- |
| `enable` | `false` | Install gitten and its schedules. |
| `package` | required | The gitten package. |
| `settings` | `{}` | Written to `~/.config/gitten/config.yaml` when set; the file is then read-only, so `setup` and `ws create --save` can't edit it. |
| `configFile`, `stateFile` | gitten's defaults | Passed to the schedules as `--config`, `--state`. |
| `path` | `[ git openssh ]` | The schedules' `PATH`, with gitten itself. |
| `sshAuthSock` | none | The schedules' `SSH_AUTH_SOCK` (systemd specifiers work). |
| `logFile` | `~/.local/share/gitten/gitten.log` | launchd agents' output; systemd logs to the journal. |
| `schedules.<name>` | none | A background sync: `interval` (`15m`; dividing an hour or a day), `repos`, `tag`, `set`, `extraArgs`, `environment`. |

Each schedule is a systemd user service and timer (`gitten` for `default`,
else `gitten-<name>`) with `OnCalendar=`, `Persistent=true`, `Nice=10` and
idle I/O on Linux, and a launchd agent with the same priority on macOS: the
units `install-agent` writes. `gitten doctor` checks only schedules
`install-agent` recorded; check these with `systemctl --user list-timers` or
`launchctl list`.

`gitten doctor` checks each recorded schedule: still installed, its binary
present, lingering on (systemd), and it runs its own checks again in the
schedule's environment -- recorded PATH and agent, nothing else -- so a `gh`
that isn't on the recorded PATH or a token that only your shell can read
shows up there instead of as silent failures in the log.
