# Quick start

[Documentation](README.md) · [Quick start](../README.md#quick-start)

```bash
gitstead setup     # a few questions, a review, then it writes the config, clones and installs the background sync
gitstead status    # what it tracks
gitstead cd api    # jump to a repo (a new terminal picks up the shell integration)
```

`setup` (or `gitstead init`, the same command) starts every answer at what it
finds on your machine, so on a typical machine you mostly press Enter:

1. **Layout** — where repos live and how they're arranged (`owner/name`,
   `host/owner/name` or `name`), guessed from where your own repos already sit.
2. **Accounts** — every account `gh` is logged in to, plus any you add by hand
   (GitLab, Gitea/Forgejo, another GitHub). For each: whose repos to track
   (your own, including private ones, and your organizations, listed from the
   provider) and, if you like, only those whose names have some words
   (`billing, payments`) or match some globs (`svc-*`), in some languages or
   with some topics. It then looks up what that finds and lists it -- how many
   repos, how many each name matched, and their names -- and asks: track them
   all, pick from the list, or change the choice; nothing is written or cloned
   yet ([Finding your repos](DISCOVERY.md#finding-your-repos)). Then: how git
   reaches the host (an SSH key — setup asks each host which
   account your keys sign in as — or HTTPS), the commit identity (from your
   global git config or `includeIf` files), signing, and optionally a separate
   base dir.
3. **Features** — backups (on), the background sync (on) and how often it
   runs (15 minutes; on Linux, whether it keeps running while you are logged
   out), offsite backup refs (off), fast-forwarding clean clones that are behind
   (off), what `sync` may change by itself, what happens when a repo disappears
   upstream.
4. **Agents** — whether to run coding agents in [workspaces](WORKSPACES.md#workspaces) (on
   when `claude` or `codex` is on your `PATH`): which agents, plus a command of
   your own such as a shell-function wrapper, how many per workspace, where
   workspaces live, and the [launcher](LAUNCHERS.md#fleets-and-launchers) that opens them
   (`auto`, or tmux, iTerm or Muxy when installed). It writes `agents:`,
   `workspace_defaults:` and `launchers.default`; workspaces themselves are made
   afterwards with `gitstead ws create … --save`, once their repos are tracked.
5. **Existing clones** — those discovery won't find where they are can be
   tracked in place.
6. **Finishing touches** — add `gitstead cd` and tab completion to your shell's
   startup file (on, unless it has them: one line in `~/.zshrc`,
   `~/.bashrc` -- `~/.bash_profile` on macOS -- or fish's `config.fish`);
   give each account's identity to repos you clone yourself under its folders
   ([`accounts gitconfig --install`](AUTHENTICATION.md#accounts), on with several accounts);
   clone what your accounts track now (on; it says how many repos that is).
7. **Review** — the diff of your config file, everything else it will do, and
   checks: each token works, each key signs in as the right account, signing
   works without a terminal, and the background sync will have each token (one
   in an environment variable it won't). Nothing is written until you confirm.

Then it writes the config and does the rest in this order -- tracks the
clones you picked, writes your git identities, adds the shell line, clones,
and installs the background sync last, so its first run doesn't race the
clone -- and ends with what it set up and what to run next. A step that fails
is reported and the others still run; running `setup` again finishes it.

A `gh` login whose token comes from an environment variable (`GITHUB_TOKEN`,
`GH_TOKEN`) is set up to read it from there (`token_env`), since `gh auth
token` reads only the logins gh stores; `gh auth login` stores one.

Re-run `setup` any time to change your answers; it edits the config in place,
keeps your comments and touches only what it manages. Every answer starts at
what is set up now: the background sync stays as it is unless you change it
(turning it off removes it), and lines it added before are replaced, not
repeated. Scripted:

| Flag | Effect |
| --- | --- |
| `--yes` | Take the detected and recommended answers without asking. |
| `--answers <file>` | Take the answers from a YAML file (the format `--save-answers` writes). |
| `--save-answers <file>` | Also write the answers used, to repeat this setup on another machine. |
| `--dry-run` | Show the diff and checks; change nothing. |
| `--offline` | Skip `ssh -T` probes and provider API calls. |

No config is needed for the basics either — if git and your auth already work,
gitstead does too, with `~/repos` as the base dir:

```bash
gitstead adopt ~/code/myrepo            # track a clone you already have
gitstead clone git@github.com:me/app    # clone + track by URL (your SSH/HTTPS auth)
gitstead clone --org acme --language go # clone every Go repo in acme (see Cloning a selection)
gitstead sync                           # fetch all tracked repos + back up WIP
gitstead status                         # table of what you're managing
gitstead ws create app --repo ~/code/myrepo --agents 4 --open --cmd claude
```

`gitstead bootstrap` is for a machine that gets its config another way --
your dotfiles, a home-manager module, `setup --save-answers` from another
machine: it runs `setup --yes` only if there's no config yet, then `doctor`,
`discover --apply`, `sync`, and installs the background sync (`--profile`
scopes all of it to a tag).

## Docker

`ghcr.io/ashtonian/gitstead` runs gitstead with git in a container, for
linux/amd64 and linux/arm64: `:<version>` and `:latest` for each release,
`:edge` for `master`.

```bash
docker run -v ~/.config/gitstead:/root/.config/gitstead \
           -v ~/repos:/root/repos \
           -v ~/.ssh:/root/.ssh:ro \
           ghcr.io/ashtonian/gitstead sync
```
