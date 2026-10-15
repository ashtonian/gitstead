# Operations and security

[Documentation](README.md) · [Quick start](../README.md#quick-start)

- [Troubleshooting](#troubleshooting)
- [Resilience](#resilience)
- [Security](#security)

## Troubleshooting

Start with `gitten doctor`: it checks tooling, each provider's token, each
SSH key, the config, every tracked repo and copy on disk, and each installed
schedule, and says what to do about each problem (`--json` for a script).
Then:

- **The background sync isn't running.** `gitten install-agent --list`
  shows what is installed. On macOS, `launchctl print gui/$(id -u)/com.ashtonian.gitten`
  shows the agent's state and last exit; its output is in
  `~/.local/share/gitten/gitten.log`. On Linux, `systemctl --user status
  gitten.timer` and `journalctl --user -u gitten`; if it stops at logout,
  reinstall with `--linger`. A binary that moved or was upgraded out of a Nix
  store or Homebrew Cellar path: `gitten install-agent` again from the
  `gitten` on your PATH.
- **It works in your terminal but not in the background sync.** The schedule
  has no shell environment: a provider using `token_env` gets nothing (`gitten
  setup` warns). Use `token_command` (`gh auth token --hostname H --user U`
  after `gh auth login`, or a password manager's CLI). An SSH key with a
  passphrase needs an agent the schedule can reach; give the account an
  `ssh_key` without one, or see [Installing a schedule](SCHEDULING.md#installing-a-schedule).
- **A command exits 75.** Another gitten command holds the state or a repo
  (deleting, moving, or making a workspace copy of it) past `lock_timeout`.
  Run it again; `doctor` names a stuck state-lock holder, and `lsof
  <repo>/.git/gitten.lock` a repo lock's.
- **A repo is kept instead of deleted.** The message says why: git is busy in
  it (a leftover `index.lock` from a crashed command: delete it once no git
  runs there), workspace copies are made from it (`gitten ws rm <name>
  --delete` first), or its work couldn't be archived (the error says why).
- **A repo shows as stale.** Discovery stopped finding it: renamed (the next
  sync usually re-keys it), deleted or transferred upstream, or your token
  lost access. `gitten status --stale` lists them; `cleanup.policy` decides
  what happens after the grace period.
- **Untracked files were left out of a backup.** Over `max_untracked_file_mb`
  or `max_untracked_total_mb`, unreadable, or a nested repository; the sync
  output and `gitten backup --list` name them. Raise the limits for that repo
  in `overrides`, or ignore the files.
- **Getting work back.** `gitten restore <repo>` prints the steps for the
  latest backup; `--into <dir>` restores into a scratch clone, `--apply` onto
  the working tree. Deleted copies and repos: `gitten archives ls`, then
  `archives restore <id>`. From another machine: `gitten remote-sync --list`.
- **An agent's copy is missing, or an agent shows as retiring.** `gitten ws
  ensure <name>` makes missing copies; `gitten ws retire <name> <agent>`
  finishes a retire that was cut short.

### Stale repos

A repo discovery no longer finds -- deleted or transferred upstream, or out
of your token's reach -- is marked stale and stays tracked; what happens next
is `cleanup.policy`'s call, and only for repos found by rules that ran
completely (see [Resilience](#resilience)):

| Key | Default | Meaning |
| --- | --- | --- |
| `cleanup.policy` | `mark_stale` | `mark_stale` keeps it, marked; `remove` stops tracking it after the grace period, leaving its files; `archive` stops tracking it and moves its clone into the archive ([Archiving instead of deleting](ARCHIVES.md#archiving-instead-of-deleting)); `remove_and_delete` archives its unique work and deletes it ([Deleting without losing work](ARCHIVES.md#deleting-without-losing-work)); `ignore` never marks anything stale. |
| `cleanup.grace_period` | `720h` | How long a repo stays stale before `remove`, `archive` or `remove_and_delete` acts. |
| `cleanup.protect_manual` | `true` | Never judge repos added by hand (`clone`, `adopt`, `repos:` entries). |

`discover --apply` and `sync` apply it; each repo kept rather than removed is
listed with the reason, and `sync` counts it as a failure until a later run
removes it.

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
`adopt`, `repos:` entries) are never judged while `cleanup.protect_manual` is
on (the default), and `cleanup.policy: ignore` judges nothing. A stale repo is no longer fetched or pushed offsite, but its
local work is still backed up.
An interrupted run is safe to repeat (see
[Config changes](RECONCILIATION.md#config-changes-plan-and-apply)). SIGINT/SIGTERM cancels
in-flight git and API operations cleanly.

## Security

gitten runs with your credentials, so in short: tokens come from exactly
the one source a provider configures and never go to another account's API;
credentials in remote URLs are refused and redacted from all output; SSH keys
are passed by path, never read; git and ssh never prompt; config and state
are written `0600`. Offsite refs are readable by anyone who can read the
remote, untracked files included, so point `remote_sync.remote` at a private
remote for repos where that matters. The full model, and how to report a
vulnerability privately, is in [SECURITY.md](../SECURITY.md).
