# Security Policy

## Reporting a Vulnerability

Please report security vulnerabilities privately via GitHub's
[security advisories](https://github.com/ashtonian/git-mgr/security/advisories/new)
rather than opening a public issue.

Include where possible: affected version, reproduction steps, and impact.
You can expect an initial acknowledgement within a few days.

## Supported Versions

Security fixes are applied to the latest released minor version. Older versions
are not maintained — upgrade to the latest release.

## Security Model

`git-mgr` handles credentials and runs unattended, so a few properties are worth
calling out:

- **Tokens** are resolved from (in order) an explicit config value, an env var,
  any provider default env var (`GITHUB_TOKEN` / `GITLAB_TOKEN` /
  `GITEA_TOKEN`,`FORGEJO_TOKEN`), or the output of `token_command`. Prefer
  `token_env` or `token_command` over committing a literal `token:` to config.
- **SSH keys** are referenced by path and passed to `git` via `GIT_SSH_COMMAND`
  with `IdentitiesOnly=yes`. git-mgr never reads key material itself.
- **Config and state files** may contain repo paths and (if you inline them)
  tokens. They are written with `0600`/`0700` permissions; keep them out of
  version control.
- **The discover/sync/backup subsystem is read-only** with respect to your
  working tree — it never pulls, merges, or commits. The opt-in workgroup
  subsystem does mutate on request.
- **Backups** of uncommitted work are stored under `refs/git-mgr/backups/*` and
  in the patch directory; treat those as containing potentially sensitive
  source just like the repos themselves.
