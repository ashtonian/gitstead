# Config changes: plan and apply

[Documentation](README.md) · [Quick start](../README.md#quick-start)

gitten keeps a state file (`~/.local/share/gitten/state.yaml`) recording what
it last set for every repo and workspace: path, origin URL, tags, the
discovering rule, the provider's repo ID, the copy ID, the repo-local git config
it wrote, and each workspace's agents and their copies. It also keeps what the last run found --
status, branch, the last fetch, backup and offsite push and their errors --
which is what `gitten status` shows. On every run it compares three things:

- **desired** — what config and this run's discovery ask for;
- **recorded** — what gitten set, from state;
- **on disk** — the directory, its origin URL and its git config.

Desired against recorded gives the changes. On disk against recorded gives
*drift*: something gitten set was changed by hand. Drift is reported and left
alone unless `reconcile.drift: overwrite`.

```console
$ gitten plan
Changes:
  ~ url              github.com/acme/api: git@github.com:acme/api.git -> https://github.com/acme/api.git
  ~ config           github.com/acme/api core.sshcommand: (unset) -> ssh -i '~/.ssh/id_work' -o IdentitiesOnly=yes
  ~ rename           github.com/acme/api-v2: github.com/acme/api -> github.com/acme/api-v2 (same repository (provider ID 4242))
  > move             github.com/acme/web: ~/repos/github.com/acme/web -> ~/repos/acme/web   [needs gitten apply]
  - remove-copy      app/agent-3: ~/workspaces/app/agent-3 (no longer wanted; unique work archived first)   [needs gitten apply]
  + create-copy      app/agent-4: ~/workspaces/app/agent-4   [gitten apply or ws ensure]
Changed by hand, left alone (reconcile.drift: overwrite restores them):
  ! github.com/acme/tools origin URL: gitten set git@github.com:acme/tools.git, now git@work:acme/tools.git; config wants git@github.com:acme/tools.git

sync applies 3 of 6 on its own (reconcile.auto: safe); `gitten apply` makes them all.
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
agent may be using, so they wait for `gitten apply`, which lists them and asks
first. Everything else is applied by `sync` and `discover --apply` as it comes
up. `sync` and `discover` handle repos only; workspaces change through `apply`
and `ws ensure`.

Repos added by hand (`clone`, `adopt`, `manifest import`) keep the path and URL
they were given; only their tags and managed git config follow config. A
workspace created from flags or adopted is kept as recorded: `ws ensure <name>`
makes its missing copies again -- including any `ws create` couldn't make, as
its copies are recorded before they are made -- and nothing removes it but
`ws rm`.

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
| `reconcile.drift` | `report` | `report` leaves values changed by hand; `overwrite` puts gitten's back. |
