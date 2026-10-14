# Security Policy

## Reporting a Vulnerability

Please report security vulnerabilities privately via GitHub's
[security advisories](https://github.com/ashtonian/gitten/security/advisories/new)
rather than opening a public issue.

Include where possible: affected version, reproduction steps, and impact.
You can expect an initial acknowledgement within a few days.

## Supported Versions

Security fixes are applied to the latest released minor version. Older versions
are not maintained — upgrade to the latest release.

## Security Model

`gitten` handles credentials and runs unattended, so a few properties are worth
calling out:

- **Tokens** come from the one source a provider configures: `token`,
  `token_env` or `token_command` (run with a 30-second limit). A configured
  source that yields nothing is an error, never a reason to try another one,
  so one account's token is never sent to another account's API. Only a
  provider configuring no source uses the type's default env var
  (`GITHUB_TOKEN` / `GITLAB_TOKEN` / `GITEA_TOKEN`, `FORGEJO_TOKEN`). Prefer
  `token_env` or `token_command` over committing a literal `token:` to config.
- **Credentials in URLs are refused.** A clone, `repos:` entry, manifest entry,
  workspace repo or adopted origin whose URL embeds a credential
  (`https://user:token@…`, or any user info in an http(s) URL) is rejected, so
  none reaches state or the event log. Git errors and the event log have URL
  credentials redacted as a second line of defence.
- **SSH keys** are referenced by path and passed to `git` via `GIT_SSH_COMMAND`
  with `IdentitiesOnly=yes`, and written into each repo they apply to as
  `core.sshCommand`. gitten never reads key material itself.
- **git never prompts.** Every git command runs without a terminal, with
  `GIT_TERMINAL_PROMPT=0`, `GCM_INTERACTIVE=never` and
  `SSH_ASKPASS_REQUIRE=never`, so a missing credential fails instead of
  waiting, and nothing typed is ever collected. When gitten passes a key
  itself, ssh runs with `BatchMode=yes` and `StrictHostKeyChecking=accept-new`:
  an unknown host's key is recorded on first use, and a changed one fails the
  connection. Pre-populate `~/.ssh/known_hosts` if first use must be verified
  too.
- **Config and state files** may contain repo paths and (if you inline them)
  tokens. They are written with `0600`/`0700` permissions; keep them out of
  version control.
- **The discover/sync/backup subsystem is read-only** with respect to your
  working tree — it never pulls, merges, or commits. The opt-in workspace
  subsystem does mutate on request.
- **Backups** of uncommitted work are stored under `refs/gitten/backups/*` and
  in the patch directory; treat those as containing potentially sensitive
  source just like the repos themselves.
- **Offsite copies** (`remote_sync`) push uncommitted work -- untracked files
  included -- and unpushed commits to hidden refs on the remote. They don't
  appear in branch lists or trigger CI, but anyone who can read the remote can
  fetch them, so an untracked `.env` in a working copy reaches everyone with
  read access. Exclude such files with `.gitignore` (ignored files are never
  backed up), set `remote_sync.enabled: false` for those repos, or point
  `remote_sync.remote` at a private backup remote. gitten force-pushes only
  inside each copy's own namespace (and, with `wip: branch`, its own
  `gitten/wip/<machine>/<copy>` branch).
