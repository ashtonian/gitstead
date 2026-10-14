// gitten finds, clones and keeps in sync every git repository you work on
// across GitHub, GitHub Enterprise, GitLab and Gitea/Forgejo -- whole
// organizations, users or searches, filtered by language, topics, name or
// keywords, in one layout with the right account per repo -- then launches
// coding agents on them side by side.
//
// Run by hand or on a schedule, it discovers repositories from providers,
// clones and tracks them with each account's identity and key, fetches them,
// and backs up uncommitted work without touching your working tree, index or
// branches -- locally, and optionally offsite under hidden refs. Config is
// declarative: `gitten plan` shows what a change to it would do on disk and
// `gitten apply` does it. Workspaces put several coding agents on a repo at
// once, each in a worktree or clone of its own, and nothing is deleted
// without its unique work archived first.
//
// main parses the command line (internal/cmd) and runs the command. See
// README.md for using it and docs/ARCHITECTURE.md for how it works.
package main
