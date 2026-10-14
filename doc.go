// git-mgr keeps every git repository you work on -- across GitHub, GitHub
// Enterprise, GitLab and Gitea/Forgejo, and several accounts on each -- in
// one layout on disk, and keeps it safe and current.
//
// Run by hand or on a schedule, it discovers repositories from providers,
// clones and tracks them with each account's identity and key, fetches them,
// and backs up uncommitted work without touching your working tree, index or
// branches -- locally, and optionally offsite under hidden refs. Config is
// declarative: `git-mgr plan` shows what a change to it would do on disk and
// `git-mgr apply` does it. Workspaces put several coding agents on a repo at
// once, each in a worktree or clone of its own, and nothing is deleted
// without its unique work archived first.
//
// main parses the command line (internal/cmd) and runs the command. See
// README.md for using it and docs/ARCHITECTURE.md for how it works.
package main
