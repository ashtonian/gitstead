# Navigation and live status

[Documentation](README.md) · [Quick start](../README.md#quick-start)

- [Live view: gitstead top](#live-view-gitstead-top)
- [Jump and open](#jump-and-open)
- [Shell completions](#shell-completions)

## Live view: gitstead top

`gitstead top` is a full-screen view that looks again every `--every` (10s):

```
gitstead top  14:03:22  ·  12 copies, 5 agents running  ·  140 repos, 3 need attention

COPY           AGENT    ACTIVITY         BRANCH              STATE  AHEAD/BEHIND  BACKUP  NOTES
app/agent-1    running  printed 8s ago   agents/app/agent-1  dirty  3/0           2m ago
app/agent-2    running  quiet ≥ 4m       agents/app/agent-2  clean  1/0           1h ago  https://github.com/me/app/pull/12
worker/agent-1 stopped  -                agents/worker/...   clean  0/2           1d ago  behind origin/HEAD by 2

REPO                  STATE  BRANCH  AHEAD/BEHIND  FETCHED  BACKUP  NOTES
github.com/me/notes   dirty  main    0/0           9m ago   9m ago
github.com/acme/api   clean  main    0/0           2d ago   -       fetch: Permission denied (publickey)
```

Every workspace copy shows its agent's state (see [Running agents](WORKSPACES.md#running-agents))
and what its terminal shows. In tmux, an agent alone in its window shows when
the window last printed; agents sharing a window show when their pane's
screen was last seen to change (`quiet ≥ 4m` until a change is seen), so an
agent waiting for you stands out. In Muxy, `muxy activity list` gives each
agent's own state. Repos show only when they need a look -- not clean, ahead,
behind, or failing to fetch or back up -- unless you press `a` (or pass
`--all`). Everything is read from disk, never the network; `--repos`, `--tag`
and `--set` narrow the repos. `q` quits, `r` looks again now. `--once` prints
one view (also what happens when output isn't a terminal) and `--json` one
snapshot.

## Jump and open

```bash
eval "$(gitstead shell-init zsh)"     # setup adds this to ~/.zshrc (or bash; fish: gitstead shell-init fish | source)
gitstead cd app                        # the repo app
gitstead cd acme/api                   # by owner/name, when two repos share a name
gitstead cd app 2                      # workspace app, agent-2's folder
gitstead open platform worker          # agent-1's worker copy in platform, in your editor
gitstead open app --web                # the repo's page on its host
vim "$(gitstead path app)/go.mod"
```

A query is part of a name. It is matched against every tracked repo (by key,
`owner/name`, name and folder), workspace, agent's folder and copy: a whole
name fits best, then a path suffix (`acme/api`), a prefix, a substring, and last
the letters in order (`wkr` for `worker`). Several words must all fit. When a
repo and a workspace fit equally, the repo wins (`--kind workspace` picks the
workspace); when two things of one kind do, gitstead asks which on a terminal,
and otherwise lists them and exits 1. A program can't change its shell's
directory, so `cd` comes from the function `shell-init` prints; without it,
`gitstead cd` prints the path. `open` runs `editor` from config, else `$VISUAL`,
else `$EDITOR`, with the path as its last argument (so `editor: code -n`
works), in the target's directory.

| Key | Default | Meaning |
| --- | --- | --- |
| `editor` | `$VISUAL`, then `$EDITOR` | The command `gitstead open` runs, a shell command with the path added as its last argument. |

## Shell completions

Tab-completion for commands, flags, and dynamic values — repo keys, tags, named
sets, workspace and fleet names, and `cd`/`path`/`open` targets are pulled live
from your state/config. `shell-init` (see [Jump and open](#jump-and-open)),
which `setup` adds to your shell, includes it; in zsh it loads the completion
system (`compinit`) first if your configuration hasn't. For completion alone:

```bash
# zsh / bash — add to your rc file
eval "$(gitstead install-completions)"

# fish
gitstead install-completions | source
```

The dynamic value predictors honor `GITSTEAD_CONFIG` / `GITSTEAD_STATE`, so
completion reflects the same state your commands use. Note: to complete workspace
subcommands, spell out `gitstead workspace …` — the `ws` and `wg` aliases don't
expand their subcommands during completion.
