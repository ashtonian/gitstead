# Fleets and launchers

[Documentation](README.md) · [Quick start](../README.md#quick-start)

A *fleet* opens several workspaces' agents side by side with one launcher:

```yaml
fleets:
  - name: daily
    workspaces: [app, platform, notes]
    agent: claude                     # optional: one profile for every agent
    launcher: tmux                    # tmux | iterm | muxy | auto | a plugin
```

```bash
gitstead fleet open daily              # make what is missing, then open every agent
gitstead fleet open daily --launcher iterm --fresh
gitstead fleet open daily --print      # show the tmux commands / iTerm AppleScript
gitstead fleet ls                      # fleets, running agents, last opened
gitstead fleet status daily            # each workspace's agents and copies
gitstead fleet stop daily              # end every running agent
gitstead ws open app                   # one workspace, as a fleet of one
```

`fleet open` first makes what is missing of the fleet's declared workspaces,
as `ws ensure` does (`--no-ensure` skips it). Each agent's terminal runs
`gitstead ws run <workspace> <agent>` (see [Running agents](WORKSPACES.md#running-agents)),
so an agent that is already running elsewhere says so and leaves a shell.
When an account the workspaces' repos need has `signing.mode: required`, a
test signature is made first, and nothing opens if it fails: an agent that
can't sign headless would stall on its first commit. `ws run` makes the same
check for its agent's copies (`--no-check` skips it, with the copy checks).

**Workspaces on another machine.** With `host:` set, `fleet open` asks gitstead
on that machine for the launch plan (`ssh <host> gitstead fleet plan <name>
--json`; `remote_command:` if gitstead needs another path or flags there), and
opens it here: tmux runs on the host through ssh, iTerm types `ssh -t <host>
…` into each split, and Muxy talks to the Muxy server on the host. The host
needs this gitstead and the same workspaces. `fleet status` and `fleet stop` run
there too.

## Launchers

Every launcher gets the same plan; `launchers.title` names each terminal
(`{{.Fleet}}`, `{{.Workspace}}`, `{{.Agent}}`, `{{.Profile}}`), `check` makes
it check its copies first and `keep_shell` leaves a shell when the agent
exits. `launchers.default` (`auto`) picks tmux inside tmux, Muxy inside a Muxy
terminal, iTerm inside iTerm, else tmux.

**tmux**: a session per fleet, a window per workspace (or agent), a pane per
agent. Each pane is marked with its agent, so opening the fleet again adds only
the agents that aren't there (`on_exists: add-missing`); inside tmux it
switches the client to the session, inside iTerm it attaches with `tmux -CC`
so panes are native splits.

| `launchers.tmux` | Default | Effect |
| --- | --- | --- |
| `session` | `gitstead-{{.Fleet}}` | Session name; `.` and `:` become `-`. |
| `window_per` | `workspace` | `workspace` or `agent`. |
| `layout` | `tiled` | `tiled`, `even-horizontal`, `even-vertical`, `main-vertical`, `main-horizontal`. |
| `max_panes` | `4` | Agents past this spill into `<workspace>-2`, `-3`, … |
| `pane_titles` | `true` | Each pane's border shows its title; agents can't retitle it. |
| `control_mode` | `auto` | `tmux -CC` inside iTerm; `always` or `never` to force. |
| `socket` | none | A separate tmux server (`-L`). |
| `on_exists` | `add-missing` | `attach`, `add-missing` or `recreate`. |
| `remain_on_exit` | `true` | Keep a pane whose shell exits. |

**iTerm**: a window per fleet, a tab per workspace (or agent), a split per
agent, driven with AppleScript (`osascript`). Needs macOS, iTerm2, and the
Automation permission for the terminal you run gitstead in, which macOS asks for
the first time.

| `launchers.iterm` | Default | Effect |
| --- | --- | --- |
| `window` | `new` | `new`, or `current` to add tabs to the front window. |
| `tab_per` | `workspace` | `workspace` or `agent`. |
| `split` | `grid` | `grid` (√n columns, in reading order), `columns` or `rows`. |
| `max_panes` | `4` | Agents past this spill into another tab. |
| `profile` | `Default` | iTerm profile for new windows, tabs and splits. |
| `badge` | `{{.Agent}}` | Pane watermark; empty turns it off. |
| `focus` | `true` | Bring iTerm to the front. |
| `type_delay` | `150ms` | Pause before typing into a new split, while its shell starts. |

**Muxy**: a project per workspace (`muxy project add --reuse`) and a tab per
agent, on this machine or a Muxy server over SSH. Muxy has no command that
opens tabs in the desktop app, so `open` picks how agents appear: `layout`
writes `.muxy/layouts/<name>.json` into each project for Apply Layout;
`sessions` starts each agent that isn't running as a server session, listed
under Existing Terminals; `state` quits the desktop app, adds the tabs to
projects that have none in its saved state, and relaunches it, which starts
them. `state` only edits the state format it knows (version 3) and keeps the
previous file as `desktop-state.json.gitstead-backup`.

| `launchers.muxy` | Default | Effect |
| --- | --- | --- |
| `host` | the fleet's host | SSH destination of the Muxy server. |
| `project_name` | `{{.Workspace}}` | Each project's name. |
| `open` | `layout` | `layout`, `sessions` or `state`. |
| `layout_name` | the agents' profile | Layout file name. |
| `restart` | `ask` | With `open: state`: `ask`, `always` or `never` quit the running app. |
| `color` | `auto` | `auto` (one per workspace), `none` or `#RRGGBB`. |
| `prune_projects` | `false` | Remove the projects made for workspaces no longer in the fleet (their files stay). |
| `command` | `muxy`, else the app's | The muxy CLI. |

**Plugins**: any other terminal is a `gitstead-launch-<name>` executable on
`PATH`, named as a fleet's `launcher:`. It gets the launch plan as JSON on
stdin -- `fleet`, `host`, and per workspace its `name`, `dir` and `agents`,
each with `name`, `dir`, `title`, `profile`, `command` (the shell line to run)
and `state` -- plus `options` from `launchers.plugins.<name>`. `gitstead fleet
plan <name> --json` prints the same plan.
