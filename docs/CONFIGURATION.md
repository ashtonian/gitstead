# Configuration

[Documentation](README.md) · [Quick start](../README.md#quick-start)

- [Clone options](#clone-options)
- [Hooks](#hooks)
- [Manual repos, excludes, overrides, tags](#manual-repos-excludes-overrides-tags)
- [Several machines](#several-machines)
  - [Per-machine sections](#per-machine-sections)
- [Config fragments](#config-fragments)
- [Upkeep](#upkeep)

Config lives at `~/.config/gitstead/config.yaml`, with any fragments in
`~/.config/gitstead/config.d/` merged into it ([Config
fragments](#config-fragments)). [`examples/`](../examples) has
short, complete configs for common setups -- searches and filters, two
accounts, self-hosted forges, agents, a full safety net -- with what each one
produces; [`config.example.yaml`](../config.example.yaml) is the fully annotated
reference of every key.

gitstead follows the XDG base directories: its config in
`$XDG_CONFIG_HOME/gitstead`, its state, operation log, backups, archives,
mirrors and scheduled-sync log in `$XDG_DATA_HOME/gitstead`, and workspaces'
shared caches and the shared manifest's lock file in `$XDG_CACHE_HOME/gitstead`,
each when the variable is set to
an absolute path, else in `~/.config/gitstead`, `~/.local/share/gitstead` and
`~/.cache/gitstead` -- the defaults shown throughout these guides.

If that default file doesn't exist, gitstead runs on defaults and `GITSTEAD_*`
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
two repos with the same name can't both be placed and are reported as blocked. A path you choose with `--config` or `GITSTEAD_CONFIG`
-- even `~/.config/gitstead/config.yaml` itself -- must exist and parse; only
with neither may the default file be missing, meaning the defaults: a missing, unreadable or malformed file is an error rather
than a silent fall back to defaults, which would otherwise run with no discovery
rules and mark every discovered repo stale. A key that isn't a setting -- a typo,
or one renamed since -- is an error too, naming every such key with the
setting it most likely misspells:
`"sync.timout_per_repo" is not a setting (did you mean "sync.timeout_per_repo"?)`.

## Clone options

`clone` shapes how much of a repo gitstead fetches and checks out: its history
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
downloaded. git's own `submodule update` ignores the sparse cone, so gitstead
names it the submodules inside. A clone or copy whose submodules can't all be
fetched (one you can't reach, say) is kept and recorded, and the failure
reported once, rather than cloned again on every sync; `git submodule update
--init --recursive` there finishes it. Workspace clones hardlink the repo's
Git LFS objects, so their checkout downloads none of them again. Submodules
with `filter` need git 2.36 or later.

Workspace worktrees share their repo's clone, filter and depth included.
Workspace clones can't be made from a partial clone, so `plan` blocks them.

## Hooks

Hooks are commands gitstead runs in a checkout it has just made: `post_clone`
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
    post_create: ['cp "$GITSTEAD_SOURCE/.env" .env', "direnv allow"]
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
| `GITSTEAD_EVENT` | `post_clone` or `post_create` |
| `GITSTEAD_PATH` | The checkout (also the working directory) |
| `GITSTEAD_REPO` | The tracked repo's key |
| `GITSTEAD_SOURCE` | The repo's clone -- where a copy's untracked files can come from |
| `GITSTEAD_WORKSPACE`, `GITSTEAD_AGENT`, `GITSTEAD_BRANCH` | The copy's workspace, agent and branch (`post_create`) |

The first command that fails stops the rest. The checkout is kept and
recorded either way -- a failed `npm ci` doesn't cost the clone -- and the
failure is reported with the end of its output; the whole run is in
`<state dir>/hooks/<checkout>.<event>.log` (the last run of each). Fix it and
run the hooks again with `gitstead hooks post-clone <repos>` or `gitstead hooks
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
gitstead status --tag work
gitstead sync   --set core
gitstead run    --set active-backend -- go test ./...
```

## Several machines

One config, kept in your dotfiles, gives every machine the same discovery
rules, `repos:`, overrides and declared workspaces. What it can't hold is
what you add by hand -- `gitstead clone`, `adopt`, `manifest import` -- which
lives in one machine's state. Point `manifest:` at a file every machine can
read and write -- beside the config in a dotfiles repo, or in a synced folder
-- and gitstead keeps it as the list of repos tracked by hand:

```yaml
manifest: ~/dotfiles/gitstead/repos.yaml
```

- `clone`, `adopt` (and `adopt --scan`) and `manifest import` add an entry:
  the repo's URL, the tags you gave it, and its path when it isn't where
  `path_template` puts it -- relative to its `base_dir` when under it,
  `~/...` under your home, else absolute -- so it lands in the same place on
  every machine -- and the `account` or `ssh_key` an imported entry binds it
  to.
- Every machine reads the manifest as more `repos:` entries, so its next
  `sync` clones what another machine added.
- `gitstead remove` takes the entry out, so no other machine clones it again;
  machines that already have it keep tracking it, as with a `repos:` entry you
  delete, until you remove it there too.
- `gitstead manifest save` adds the repos this machine tracked by hand before
  the manifest existed; without `manifest:` it prints them as `repos:`
  entries to paste into config. `gitstead doctor` names repos tracked by hand
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

### Per-machine sections

Machines that share a config still differ: which drive clones and workspaces
go on, how many agents each runs, repos only one of them works on. A
`machines:` section holds what one machine does differently, and is laid over
the rest of the config on that machine:

```yaml
base_dir: ~/code
workspace_defaults: { root: ~/workspaces, agents: 2 }
workspaces:
  - { name: app, repos: [github.com/your-login/app] }

machines:
  desktop:
    base_dir: /Volumes/Fast/code
    workspace_defaults: { root: /Volumes/Fast/workspaces, agents: 4 }
    overrides:
      - { repos: ["github.com/acme/*"], base_dir: /Volumes/Bulk/code }
    workspaces:
      - { name: app, agents: 6 }
  laptop:
    workspace_defaults: { agents: 1 }
```

A machine's name is `machine:` -- set it with `GITSTEAD_MACHINE` or
`--machine`, not in a file every machine reads (a [fragment](#config-fragments)
can't set it) -- and without it, on macOS
the local host name (System Settings > General > Sharing, which DHCP doesn't
change), elsewhere the host name up to its first dot. Section names match it
ignoring case, and are letters, digits, `-` and `_`.

| Key in a section | How it applies |
| --- | --- |
| `base_dir` | Replaces the top-level one. |
| `workspace_defaults` | Each key it sets replaces the top-level one; `env` adds its names to the top level's, replacing those it shares. |
| `overrides`, `repos`, `discovery` | Added after the top-level entries. Overrides apply in order, so a section's override wins over a top-level one for the same repos. |
| `workspaces` | An entry with a top-level workspace's name changes the keys it sets of that one -- `repos`, `set` and `path` replace each other together; `env` adds names -- and keeps the rest. A new name is a workspace on this machine only. |
| `fleets` | As workspaces: by name, the keys it sets; a new name is this machine's own. |

Lists are added to rather than replaced, so a section never has to repeat
what every machine shares; to drop a top-level entry on one machine, move it
into the sections of the machines that keep it. Any other key in a section is
an error, as a misspelt one anywhere is. Every section is checked on every
machine -- merged over the top level as on its own machine, and validated --
so a mistake in the laptop's section shows on the desktop too.

A machine with no section of its own gets the top-level settings: a new
machine works before it has one, and `gitstead doctor` notes it. `gitstead
config` prints the settings in effect, headed by the files it read and then
the machine and the section it took; `gitstead --machine laptop config` (or
`plan`) shows another
machine's without going there.

| Key | Default | Meaning |
| --- | --- | --- |
| `machine` | this machine's name | The section to lay over the rest; `GITSTEAD_MACHINE`, `--machine`. |
| `machines` | `{}` | Each machine's own settings, by name: `base_dir`, `workspace_defaults`, `overrides`, `repos`, `discovery`, `workspaces`, `fleets`. |

## Config fragments

One machine can hold work from several contexts -- your own, and each
client's -- whose repos, accounts and workspaces must each live in that
context's own private dotfiles, never in another's. One `config.yaml` would
have to hold them all. Instead, each context keeps a fragment: a YAML file in
`config.d` beside the config file, which gitstead reads with it and merges
into one config. A public dotfiles repo links a generic `config.yaml`, and
each context's private overlay links its own fragment:

```
~/.config/gitstead/
  config.yaml          -> ~/dotfiles/gitstead/config.yaml              the shared base: layout, sync, defaults
  config.d/
    personal.yaml      -> ~/dotfiles-personal/gitstead/personal.yaml   your accounts, repos and workspaces
    work.yaml          -> ~/dotfiles-work/gitstead/work.yaml           one client's, and nothing else
```

Every file is a config file in its own right -- any key, `machines:`
sections included -- so each context says where its repos and workspaces go
on each machine. [`examples/fragments`](../examples/fragments) is a complete
set:

```yaml
# config.yaml: names no account and no repo.
base_dir: ~/code
layout: owner/name
sync:
  update: ff-only-clean
workspace_defaults:
  root: ~/workspaces
  agents: 2
machines:
  desktop:
    base_dir: /Volumes/Fast/code
    workspace_defaults:
      root: /Volumes/Fast/workspaces
  laptop:
    workspace_defaults:
      agents: 1
```

```yaml
# config.d/personal.yaml
accounts:
  - name: personal
    hosts: [github.com]
    owners: [your-login]
    ssh_key: ~/.ssh/id_personal
repos:
  - url: git@github.com:your-login/app.git
workspaces:
  - name: app
    repos: [github.com/your-login/app]
machines:
  desktop:
    workspaces:
      - name: app
        agents: 4
```

```yaml
# config.d/work.yaml
accounts:
  - name: work
    hosts: [github.com]
    owners: [acme]
    ssh_key: ~/.ssh/id_work
repos:
  - url: git@github.com:acme/api.git
overrides:
  - repos: ["github.com/acme/*"]
    base_dir: ~/work
workspaces:
  - name: acme
    repos: [github.com/acme/api]
machines:
  desktop:
    overrides:
      - repos: ["github.com/acme/*"]
        base_dir: /Volumes/Bulk/code
    workspaces:
      - name: acme
        root: /Volumes/Bulk/workspaces
        agents: 3
```

On the desktop, acme's repos go to `/Volumes/Bulk/code` and its workspace has
three agents; on the laptop they go to `~/work`, every workspace with one.
Neither fragment mentions the other, and the base mentions neither.

**Which files.** The fragments are the files in `config.d` beside the config
file in use -- the default `~/.config/gitstead/config.yaml`, or the one
`--config` or `GITSTEAD_CONFIG` names, whose own `config.d` is read -- whose
names end in `.yaml` or `.yml`, without hidden ones (a leading `.`). They are
read after `config.yaml`, in byte order of their names (`B.yaml` before
`a.yaml`; prefix names with numbers to choose the order). Links are followed,
to a fragment and to `config.d` itself. Anything else in `config.d` is left
alone. When only fragments exist, they are the config; a file `--config` or
`GITSTEAD_CONFIG` names must still exist. A `config.d` or fragment that can't
be read -- a broken link, a fragment that is a directory, a directory you
can't list -- is an error, never a config without it.

**Checks.** Each file must parse and hold only settings, checked on its own,
so an error names the file it is in:
`config /Users/you/.config/gitstead/config.d/work.yaml is invalid: "sync.timout_per_repo" is not a setting (did you mean "sync.timeout_per_repo"?)`.
Everything else is checked once, on the merged config, so a fragment may name
an account, profile or provider another file defines, and an error there
names every file read.

**How the files merge.** Each fact comes from one file: nothing set in one
file is silently replaced by another.

| What | Keys | Merge |
| --- | --- | --- |
| A setting | Every single value, a section's keys included (`base_dir`, `sync.update`, `backup.archive_retention`), and a list that is one setting's value (`hooks.post_clone`, `clone.sparse`) | One file sets it. Set in two -- even to the same value -- it is an error. |
| A section | `sync`, `backup`, `launchers`, `workspace_defaults`, `remote_sync` and the rest | Key by key: different files may set different keys of it. |
| Entries | `repos`, `overrides`, `exclude` | Every file's, in file order: `config.yaml`'s first, then each fragment's. Overrides apply in order, so a later file's override wins for the repos it shares with an earlier one, as a later override does within a file. |
| Named entries | `accounts`, `providers`, `discovery`, `sets`, `agents`, `workspaces`, `fleets` | Every file's, in file order; a name defined in two files is an error. |
| Maps | `workspace_defaults.env`, `launchers.plugins` | Name by name; a name in two files is an error. |
| Machine sections | `machines` | By machine name, ignoring case and spelled as the first file spells it. One machine's sections from several files merge by these same rules: `machines.desktop.base_dir` in two files is an error, `overrides` and `repos` are added to, and `discovery`, `workspaces` and `fleets` merge by name. The merged section is then laid over the rest as [Per-machine sections](#per-machine-sections) says. |
| The machine | `machine` | Only in `config.yaml`. A fragment, which every machine reads, can't choose the machine: use `GITSTEAD_MACHINE` or `--machine`. |

Environment variables and flags still override the merged files, as they
override one file. Every machine checks every section, whichever files they
came from.

**What gitstead writes.** `setup`, `clone --save`, `manifest import` and
`ws create --save` edit `config.yaml` alone, never a fragment, which belongs to
whoever links it. A change to something a fragment sets -- a setting, or a
named entry such as an account or a workspace -- is refused, naming the
fragment, unless the fragment already has it so; a new entry, such as another
repo or rule, goes into `config.yaml` as ever. The edited config is merged
with the fragments and checked before anything is written. So `setup` stops
with `account "work": accounts entry "work" is set in
/Users/you/.config/gitstead/config.d/work.yaml, and gitstead writes only
/Users/you/.config/gitstead/config.yaml: change it there` when its answers
would change an account a fragment holds.

**Seeing what was read.** `gitstead config` heads its output with the files it
read, in merge order, before the machine line:

```
$ gitstead --machine desktop config | head -3
# Files read, in merge order: ~/.config/gitstead/config.yaml, ~/.config/gitstead/config.d/personal.yaml, ~/.config/gitstead/config.d/work.yaml
# Machine "desktop": its machines: section is applied below (sections: desktop, laptop).
base_dir: /Volumes/Fast/code
```

`gitstead config --json` (and `config --validate --json`) lists them as
`files`; `gitstead doctor`'s `config valid:` line names them too.

**When it goes wrong:**

- `base_dir is set in both <file> and <file>: each setting may be set in only
  one file` -- two files set one setting or define one name. Keep it in the
  file of the context it belongs to and delete it from the other; for a
  per-machine difference, put it in that machine's `machines:` section in one
  file.
- `"machine" can't be set in a fragment` -- move `machine:` to
  `GITSTEAD_MACHINE` (or `config.yaml`, on a machine whose config is its own).
- `reading config fragments <dir>/config.d: ...` or `reading config
  <dir>/config.d/work.yaml: ... no such file or directory`
  -- a link points at an overlay that isn't there (an unmounted drive, a
  removed checkout). Restore its target or remove the link; gitstead won't run
  without a context's config.
- A fragment seems ignored -- check its name ends in `.yaml` or `.yml` and
  doesn't start with `.`, and that `config.d` sits beside the config file in
  use (`gitstead config --path`); `gitstead config | head -1` lists what was
  read.

The home-manager module writes fragments from
`programs.gitstead.fragments.<name>` ([Scheduling](SCHEDULING.md#with-nix-home-manager)).

| Key | Default | Meaning |
| --- | --- | --- |
| `config.d/*.yaml`, `config.d/*.yml` | none | Fragments, read after the config file in byte order of their names and merged with it, as above. Not keys: files. |

## Upkeep

A fetch or repack that is interrupted -- a timeout, a laptop going to sleep,
a killed run -- leaves its half-written pack in the repo's `objects/pack`, as
`tmp_pack_*`, `tmp_idx_*` or `.tmp-<pid>-pack-*`. git deletes the `tmp_`
ones only when `gc` prunes, which it never does in a clone that workspace
copies borrow objects from (gitstead sets `gc.pruneExpire=never` there so
their objects stay), and a dead repack's `.tmp-` ones never. They add up to
gigabytes on a large repo that is fetched often.

After its backups and fetches, `sync` gives each tracked clone and workspace
copy -- not a worktree, whose objects are its repo's clone's -- upkeep: it
deletes the temporary packs older than `maintenance.temp_pack_age`, then runs
`git maintenance run --auto`, which repacks and writes the commit-graph when
git judges it due, and returns at once when nothing is. A repo another git
command is at work in (a lock file, a running gc) or that another gitstead
command is deleting, archiving or moving is skipped until the next run, not
failed. `gitstead maintain` does the same now, enabled or not, for the repos
`--repos`, `--tag` or `--set` select and their copies; `--dry-run` lists the
temporary packs it would delete. `sync --no-maintain` skips upkeep for one
run.

| Key | Default | Meaning |
| --- | --- | --- |
| `maintenance.enabled` | `true` | Upkeep in every `sync`. |
| `maintenance.temp_pack_age` | `24h` | A temporary pack older than this was left behind; a younger one may still be being written. At least `1h`. |
| `maintenance.timeout` | `10m` | The limit on one clone's or copy's upkeep; 0 for none. |
