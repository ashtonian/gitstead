# Configuration

[Documentation](README.md) · [Quick start](../README.md#quick-start)

- [Clone options](#clone-options)
- [Hooks](#hooks)
- [Manual repos, excludes, overrides, tags](#manual-repos-excludes-overrides-tags)
- [Several machines](#several-machines)

Config lives at `~/.config/gitten/config.yaml`. [`examples/`](../examples) has
short, complete configs for common setups -- searches and filters, two
accounts, self-hosted forges, agents, a full safety net -- with what each one
produces; [`config.example.yaml`](../config.example.yaml) is the fully annotated
reference of every key.

gitten follows the XDG base directories: its config in
`$XDG_CONFIG_HOME/gitten`, its state, operation log, backups, archives,
mirrors and scheduled-sync log in `$XDG_DATA_HOME/gitten`, and workspaces'
shared caches and the shared manifest's lock file in `$XDG_CACHE_HOME/gitten`,
each when the variable is set to
an absolute path, else in `~/.config/gitten`, `~/.local/share/gitten` and
`~/.cache/gitten` -- the defaults shown throughout these guides.

If that default file doesn't exist, gitten runs on defaults and `GITTEN_*`
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
two repos with the same name can't both be placed and are reported as blocked. A path you choose with `--config` or `GITTEN_CONFIG`
-- even `~/.config/gitten/config.yaml` itself -- must exist and parse; only
with neither may the default file be missing, meaning the defaults: a missing, unreadable or malformed file is an error rather
than a silent fall back to defaults, which would otherwise run with no discovery
rules and mark every discovered repo stale. A key that isn't a setting -- a typo,
or one renamed since -- is an error too, naming every such key with the
setting it most likely misspells:
`"sync.timout_per_repo" is not a setting (did you mean "sync.timeout_per_repo"?)`.

## Clone options

`clone` shapes how much of a repo gitten fetches and checks out: its history
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
downloaded. git's own `submodule update` ignores the sparse cone, so gitten
names it the submodules inside. A clone or copy whose submodules can't all be
fetched (one you can't reach, say) is kept and recorded, and the failure
reported once, rather than cloned again on every sync; `git submodule update
--init --recursive` there finishes it. Workspace clones hardlink the repo's
Git LFS objects, so their checkout downloads none of them again. Submodules
with `filter` need git 2.36 or later.

Workspace worktrees share their repo's clone, filter and depth included.
Workspace clones can't be made from a partial clone, so `plan` blocks them.

## Hooks

Hooks are commands gitten runs in a checkout it has just made: `post_clone`
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
    post_create: ['cp "$GITTEN_SOURCE/.env" .env', "direnv allow"]
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
| `GITTEN_EVENT` | `post_clone` or `post_create` |
| `GITTEN_PATH` | The checkout (also the working directory) |
| `GITTEN_REPO` | The tracked repo's key |
| `GITTEN_SOURCE` | The repo's clone -- where a copy's untracked files can come from |
| `GITTEN_WORKSPACE`, `GITTEN_AGENT`, `GITTEN_BRANCH` | The copy's workspace, agent and branch (`post_create`) |

The first command that fails stops the rest. The checkout is kept and
recorded either way -- a failed `npm ci` doesn't cost the clone -- and the
failure is reported with the end of its output; the whole run is in
`<state dir>/hooks/<checkout>.<event>.log` (the last run of each). Fix it and
run the hooks again with `gitten hooks post-clone <repos>` or `gitten hooks
post-create <workspace> [agents]`. Moving a copy or a repo runs nothing.
Hooks come only from your config, never from a repository, so cloning
someone else's repo never runs their code.

## Manual repos, excludes, overrides, tags

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
  - name_pattern: "^deprecated-"                # a regular expression on the name
  - names: [sandbox, "*-experiment"]             # words in the name, or globs, as rules' names
  - repos: ["github.com/my-org/legacy-*"]        # repo keys or globs over them, as in overrides

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
gitten status --tag work
gitten sync   --set core
gitten run    --set active-backend -- go test ./...
```

## Several machines

One config, kept in your dotfiles, gives every machine the same discovery
rules, `repos:`, overrides and declared workspaces. What it can't hold is
what you add by hand -- `gitten clone`, `adopt`, `manifest import` -- which
lives in one machine's state. Point `manifest:` at a file every machine can
read and write -- beside the config in a dotfiles repo, or in a synced folder
-- and gitten keeps it as the list of repos tracked by hand:

```yaml
manifest: ~/dotfiles/gitten/repos.yaml
```

- `clone`, `adopt` (and `adopt --scan`) and `manifest import` add an entry:
  the repo's URL, the tags you gave it, and its path when it isn't where
  `path_template` puts it -- relative to its `base_dir` when under it,
  `~/...` under your home, else absolute -- so it lands in the same place on
  every machine -- and the `account` or `ssh_key` an imported entry binds it
  to.
- Every machine reads the manifest as more `repos:` entries, so its next
  `sync` clones what another machine added.
- `gitten remove` takes the entry out, so no other machine clones it again;
  machines that already have it keep tracking it, as with a `repos:` entry you
  delete, until you remove it there too.
- `gitten manifest save` adds the repos this machine tracked by hand before
  the manifest existed; without `manifest:` it prints them as `repos:`
  entries to paste into config. `gitten doctor` names repos tracked by hand
  here that neither lists.

These commands never write the config file, so this works when it is
generated -- by the home-manager module, say -- and read-only (`setup` and
`clone --save` edit it, and only when you ask). Without
`manifest:`, `manifest import` keeps an entry's `account` or `ssh_key` in
config's `repos:`, edited in place, since state can't hold it: a read-only
config fails that entry, naming what to add. `manifest export` writes each
repo's binding back out. Writes take a lock in your cache directory, never beside the manifest, and replace the file atomically, so a sync tool
copying it never sees half a file; two machines writing at the same moment
through a sync tool can still conflict, as any synced file can, and the
next `manifest save` puts back an entry a conflict lost.
