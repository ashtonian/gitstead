# Authentication and accounts

[Documentation](README.md) · [Quick start](../README.md#quick-start)

- [Authentication](#authentication)
- [Accounts](#accounts)

## Authentication

gitten separates two auth surfaces and **does not store credentials** itself:

- **Provider APIs** (discovery) use a PAT from the provider's one token source
  (e.g. `token_command: "gh auth token"`).
- **Git transport** (clone/fetch) uses `clone_protocol` — `ssh` (default; your
  SSH agent/keys, or a provider/override `ssh_key`) or `https`. Over HTTPS, auth
  flows through git's own credential helper (`gh auth setup-git`, osxkeychain, a
  Git Credential Manager, or a PAT stored there). Set it globally or per repo set:

```yaml
clone_protocol: https            # default ssh
overrides:
  - repos: ["github.com/work-org/*"]
    clone_protocol: ssh
    ssh_key: "~/.ssh/id_work"
```

`gitten clone <url>` honors the scheme you pass (an `https://` URL clones over
HTTPS, `git@…`/`ssh://` over SSH). A URL embedding a credential
(`https://user:token@…`) is refused everywhere — clone, `repos:`, manifests,
workspaces, adopt — so none lands in state or logs; use a credential helper.
One put into a clone's remote by hand afterwards is kept out too: remote URLs
gitten writes down (archive and backup descriptions) are stored without it,
plans show it redacted, and `--json` output redacts any URL credential.
`gitten doctor` reports whether each provider's token resolves and whether
configured SSH keys exist.

## Accounts

An account is one identity on one or more hosts: the SSH key and the commit
identity for the repos it covers. With work and personal repos on the same
host, each repo pushes with the right key and commits with the right email —
yours and your agents'.

```yaml
accounts:
  - name: personal
    hosts: [github.com]                # every github.com owner no other account claims
    ssh_key: ~/.ssh/id_personal
    git: {name: Your Name, email: you@example.com}
  - name: work
    hosts: [github.com]
    owners: [acme, acme-labs]          # these owners, and their GitLab subgroups
    ssh_key: ~/.ssh/id_work
    git: {name: Your Name, email: you@acme.com}
    signing:
      mode: required                   # inherit (default) | required | off
      key: ~/.ssh/id_work.pub          # .pub or key:: means SSH signing; a key ID means GPG
      program: /Applications/1Password.app/Contents/MacOS/op-ssh-sign   # optional
  - name: ghe
    ssh_key: ~/.ssh/id_ghe             # covers only what names it below
providers:
  - {name: ghes, type: github, url: https://ghes.acme.com, account: ghe}
```

A repo's account is the one named by the most specific of: a matching
override's `account:`, its `repos:` entry, its discovery rule, that rule's
provider; otherwise the account whose `hosts` and `owners` match the repo (an
account listing the owner beats one covering the whole host). Its SSH key
follows the same order, where an `ssh_key:` set directly at any of those places
counts too. `gitten config --for-repo <key>` shows what a repo resolves to.

gitten uses the key for its own clones and fetches, and writes the account
into the repo's own git config, so your own git commands there behave the same:

| Key | Written when |
| --- | --- |
| `core.sshCommand = ssh -i <key> -o IdentitiesOnly=yes` | the repo has an SSH remote and a key |
| `user.name`, `user.email` | the account sets them; otherwise your global identity applies |
| `commit.gpgsign`, `tag.gpgsign`, `gpg.format`, `user.signingkey`, `gpg.ssh.program`/`gpg.program`/`gpg.x509.program`, `gpg.ssh.allowedSignersFile` | `signing.mode: required` |
| `commit.gpgsign = false`, `tag.gpgsign = false` | `signing.mode: off`, so an agent never stalls on a passphrase prompt |

| Key | Default | Meaning |
| --- | --- | --- |
| `accounts[].signing.mode` | `inherit` | `inherit` leaves signing to your global git config; `required` signs every commit and tag; `off` never signs in these repos. |
| `accounts[].signing.key` | | `user.signingkey`: an SSH key (`.pub` path or `key::...`), a GPG key ID or an X.509 certificate ID. Required with `mode: required`, except for GPG, which can use its default key. |
| `accounts[].signing.format` | from the key | `gpg.format`: `ssh` for a `.pub` or `key::` key, `openpgp` otherwise, or `x509`. |
| `accounts[].signing.program` | | A signer in place of `ssh-keygen`, `gpg` or `gpgsm`, such as 1Password's `op-ssh-sign`. |
| `accounts[].signing.allowed_signers` | | `gpg.ssh.allowedSignersFile`, for verifying SSH signatures. |

With `signing.mode: required`, `gitten doctor` makes a test signature the way
an agent would — no terminal, 20-second limit — and fails if it can't: SSH
signing through an agent or a passphrase-less key works unattended; GPG needs
a gpg-agent holding the passphrase.

Workspace clones get the same keys as the repo they come from, so agents
commit as the right account; worktrees share their repo's config. If you set one of these keys in a repo yourself, gitten leaves it and
reports the difference (see [Config changes](RECONCILIATION.md#config-changes-plan-and-apply)).
Changing an account updates every repo it covers on the next `sync` or `apply`.

gitten writes those keys into the repos it tracks. For repos you clone
yourself into an account's folders, `gitten accounts gitconfig` makes the
same settings your git's own, with
[`includeIf`](https://git-scm.com/docs/git-config#_conditional_includes):

```bash
gitten accounts ls                    # accounts, what they cover, their folders
gitten accounts gitconfig             # print what it would write
gitten accounts gitconfig --write     # write it to ~/.config/gitten/
gitten accounts gitconfig --install   # and include it from your global git config
```

An account's folders are where config puts its repos: for each of its hosts
and owners, the leading part of their path that doesn't depend on the repo's
name (`~/repos/github.com/acme` under the default layout, `~/work/acme` with
a `base_dir` of its own). Each account with something to set gets
`~/.config/gitten/accounts/<name>-<digest>.gitconfig` (the name made a file
name; the digest of the name keeps apart names that make the same one), and
`~/.config/gitten/accounts.gitconfig` holds an `[includeIf "gitdir:<folder>/"]`
for each folder, deeper folders last so an owner's account wins over its
host's. git writes both files, quoting what it must, and a folder's wildcard
characters (`*`, `?`, `[`) are escaped so it matches only itself. A folder two accounts share is left out with a warning, and under
`layout: name`, where every repo sits directly in `base_dir`, there are no
folders to tell accounts apart by. `--install` adds one `include.path` line
to your global git config, once; nothing else touches it. Run it again after
changing accounts.
