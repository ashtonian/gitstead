# gitstead

Find, clone and keep your repos in sync across GitHub, GitLab and
Gitea/Forgejo, with the right account per repo. Launch coding agents in
separate workspace copies and back up their uncommitted work.

Runs on **macOS and Linux**. Requires `git` on `PATH`.

## Highlights

- **Discovery across accounts and hosts.** Find repos by owner, organization,
  search, name, language or topic; saved rules pick up new matches.
- **The right identity.** Apply each account's SSH key, commit identity and
  signing settings to the repos it covers.
- **Scheduled sync and backups.** Fetch repos and snapshot uncommitted work,
  with optional offsite copies and upstream mirrors.
- **Parallel coding agents.** Give each agent its own branch and worktree or
  clone, then open them side by side with tmux, iTerm or Muxy.
- **Portable setup.** Share configuration and a manifest across machines;
  preview configuration changes with `plan` before applying them.

Backups leave the working tree, index and branches untouched. Branch
fast-forwarding, offsite pushes and cleanup follow explicit configuration;
see [scope](docs/OVERVIEW.md) and the [backup and recovery guarantees](docs/BACKUPS.md).

## Install

```bash
brew install --cask ashtonian/tap/gitstead        # macOS
go install github.com/ashtonian/gitstead@latest   # Go version: see go.mod
docker pull ghcr.io/ashtonian/gitstead            # linux/amd64, linux/arm64
```

The [releases](https://github.com/ashtonian/gitstead/releases) have archives
for macOS and Linux on amd64 and arm64, with checksums. Name the cask in full,
`ashtonian/tap/gitstead`: Homebrew asks you to trust a third-party tap, and
naming its cask does.

gitstead shells out to `git`, so git must be on `PATH`.

## Quick start

```bash
gitstead setup     # a few questions, a review, then it writes the config, clones and installs the background sync
gitstead status    # what it tracks
gitstead cd api    # jump to a repo (a new terminal picks up the shell integration)
```

`setup` detects accounts, existing clones and tools, previews the proposed
configuration, and asks before writing it. It can configure your shell and
install background sync. Re-run it to change your setup; see the
[full setup guide](docs/SETUP.md).

Already have clones? Track one and sync it without writing a config:

```bash
gitstead adopt ~/code/myrepo
gitstead sync
gitstead status
```

## Find more repos

Preview a selection, then clone it and choose whether to save it as a rule:

```bash
gitstead clone --org acme --name billing --dry-run
gitstead clone --org acme --name billing
```

Saved discovery rules keep finding new matches on later syncs. See
[finding your repos](docs/DISCOVERY.md) for accounts, filters and searches.

## Work with coding agents

For a tracked repo, create four copies and open an agent in each:

```bash
gitstead ws create app --repo github.com/me/app --agents 4
gitstead ws open app --agent claude
gitstead top
```

This example needs Claude and a launcher such as tmux installed. Agent
profiles also support Codex and custom commands. See
[workspaces](docs/WORKSPACES.md), [launchers](docs/LAUNCHERS.md), and
[live status](docs/SHELL.md#live-view-gitstead-top).

## Documentation

| Task | Guide |
|---|---|
| Set up or move to another machine | [Setup](docs/SETUP.md), [shared manifests](docs/CONFIGURATION.md#several-machines) |
| Find and clone repos | [Discovery and filters](docs/DISCOVERY.md) |
| Configure layout, clone shapes, hooks and sets | [Configuration](docs/CONFIGURATION.md), [example configs](examples/README.md) |
| Manage accounts, SSH keys and signing | [Authentication and accounts](docs/AUTHENTICATION.md) |
| Understand configuration changes | [Plan and apply](docs/RECONCILIATION.md) |
| Protect and recover work | [Backups, offsite copies and mirrors](docs/BACKUPS.md), [archives](docs/ARCHIVES.md) |
| Run agents across one or more repos | [Workspaces](docs/WORKSPACES.md), [fleets and launchers](docs/LAUNCHERS.md) |
| Schedule sync and diagnose failures | [Scheduling](docs/SCHEDULING.md), [operations](docs/OPERATIONS.md) |
| Find a command, flag or exit code | [Command reference](docs/COMMANDS.md), [shell integration](docs/SHELL.md) |
| Contribute | [Contributing](CONTRIBUTING.md), [architecture](docs/ARCHITECTURE.md), [detailed spec](SPEC.md) |

See the [documentation index](docs/README.md), the fully annotated
[config.example.yaml](config.example.yaml), and [SECURITY.md](SECURITY.md).

## License

MIT — see [LICENSE](LICENSE).
