# Documentation

[Project overview and quick start](../README.md) · [Example configs](../examples/README.md) · [Annotated configuration](../config.example.yaml)

## Getting started

- [Overview and scope](OVERVIEW.md): capabilities, supported hosts and what gitstead changes.
- [Setup](SETUP.md): guided setup, scripted answers, bootstrapping and Docker.
- [Discovery](DISCOVERY.md): providers, finding repos, matching names, rules and raw queries.
- [Navigation and live status](SHELL.md): `gitstead top`, jumping, opening and shell completions.

## Configuration and accounts

- [Configuration](CONFIGURATION.md): layout, clone options, hooks, overrides, tags, sets and shared manifests.
- [Authentication and accounts](AUTHENTICATION.md): provider tokens, Git transport, account selection and signing.
- [Plan and apply](RECONCILIATION.md): desired configuration, drift, moves and removals.

## Protecting work

- [Backups](BACKUPS.md): snapshots, patch backups, retention, offsite refs and upstream mirrors.
- [Archives and safe removal](ARCHIVES.md): inspection, bundles, archiving whole clones and recovery.

## Coding agents

- [Workspaces](WORKSPACES.md): copies, agents, environments, ports, caches, waiting notifications, publishing and retiring.
- [Fleets and launchers](LAUNCHERS.md): tmux, iTerm, Muxy, remote hosts and launcher plugins.

## Operations and reference

- [Scheduling](SCHEDULING.md): sync policy, timeouts, logs, notifications, launchd, systemd, cron and home-manager.
- [Operations and security](OPERATIONS.md): diagnostics, stale repos, resilience and the security model.
- [Command reference](COMMANDS.md): commands, JSON output, exit codes and environment variables.
- [Detailed specification](../SPEC.md): command contracts and design details.
- [Security policy](../SECURITY.md): credentials and private vulnerability reporting.

## Contributing

- [Contributing](../CONTRIBUTING.md): development setup, checks and versioned builds.
- [Architecture and code map](ARCHITECTURE.md): components, data flow, locks and failure modes.
- [Releasing](RELEASING.md): cutting and publishing a release.
