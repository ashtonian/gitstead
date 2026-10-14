# Example configs

Each file here is a complete config for one way of using gitten, short
enough to read in a minute. Every one is loaded and validated by gitten's
tests, so they stay correct.

| Example | For |
| --- | --- |
| [personal.yaml](personal.yaml) | Every repo your GitHub account owns, kept in sync. |
| [work-and-personal.yaml](work-and-personal.yaml) | Two accounts on one machine, each repo with the right identity and key. |
| [org-filters.yaml](org-filters.yaml) | Only some of an organization's repos: by language, topic and name. |
| [searches.yaml](searches.yaml) | Repos found by search: open source you follow, a keyword across an org. |
| [self-hosted.yaml](self-hosted.yaml) | A self-hosted GitLab and a Forgejo instance, over HTTPS. |
| [agents.yaml](agents.yaml) | Coding agents in workspaces, opened side by side in tmux. |
| [safety-net.yaml](safety-net.yaml) | Backups, offsite copies, mirrors, and archiving instead of deleting. |

Preview any of them without changing anything, then make it yours:

```bash
gitten --config examples/searches.yaml discover    # what its rules find, and where each repo goes
gitten --config examples/agents.yaml plan          # everything it would clone and create
cp examples/personal.yaml ~/.config/gitten/config.yaml   # or let `gitten setup` write one
```

## How a config becomes repos on disk

```
config.yaml                      provider APIs                  your disk
-----------                      -------------                  ---------
discovery:                       GitHub, GitLab,                ~/code/
  - org: acme          ------->  Gitea/Forgejo      ------->      acme/svc-billing   (account: work)
    languages: [Go]              list + filter                    acme/svc-orders
    name_pattern: ^svc-          (or search)                      you/dotfiles       (account: personal)
  - affiliation: [owner]
                                       |
          gitten discover  -> shows the plan: what's new, where it goes
          gitten clone ... -> the same, once, from flags (no config needed)
          gitten sync      -> every 15m in the background: clone new matches,
                              fetch everything, follow renames, flag repos gone
                              upstream, back up uncommitted work
```

## searches.yaml: repos found by search

The rules select by search and by filters rather than by owner alone. This is
real output from the example's two public rules:

```
$ gitten discover
  [github.com] charm-tui: found 5 repos
  [github.com] k8s-operators: found 3 repos

8 new, 0 existing

Changes:
  + clone  github.com/CrunchyData/postgres-operator -> ~/code/CrunchyData/postgres-operator
  + clone  github.com/charmbracelet/bubbles -> ~/code/charmbracelet/bubbles
  + clone  github.com/charmbracelet/bubbletea -> ~/code/charmbracelet/bubbletea
  + clone  github.com/charmbracelet/bubbletea-app-template -> ~/code/charmbracelet/bubbletea-app-template
  + clone  github.com/charmbracelet/glamour -> ~/code/charmbracelet/glamour
  + clone  github.com/charmbracelet/lipgloss -> ~/code/charmbracelet/lipgloss
  + clone  github.com/cyclops-ui/cyclops -> ~/code/cyclops-ui/cyclops
  + clone  github.com/glasskube/glasskube -> ~/code/glasskube/glasskube
```

(Each line also shows the clone URL; trimmed here.) The same selections work
with no config at all, once:

```
$ gitten clone --org charmbracelet --language go --name '^bubble' --dry-run
3 repos match:
  + github.com/charmbracelet/bubbles
  + github.com/charmbracelet/bubbletea
  + github.com/charmbracelet/bubbletea-app-template
```

Without `--dry-run` it clones them and offers to save the selection as a rule.

## org-filters.yaml: part of an organization

```
acme (GitHub org, 240 repos)
 |
 |-- acme-services:  language Go, topic "service", name ^svc-      ->  svc-billing, svc-orders, ...
 |-- acme-terraform: language HCL, private, name ^terraform-       ->  terraform-network, ...
 `-- exclude:        names ending -sandbox or -experiment, svc-legacy-*
```

Tags and sets then name groups for bulk work:

```bash
gitten run --set services -- go test ./...     # in every service, at once
gitten status --tag infra
gitten sync --set services
```

## work-and-personal.yaml: two accounts, one machine

```
~/code/github.com/your-login/dotfiles     personal: you@example.com, ~/.ssh/id_personal
~/code/github.com/your-login/app
~/work/acme/api                           work: you@acme.com, ~/.ssh/id_work, signed commits
~/work/acme-labs/prototype
```

What each account puts in its repos' own git config, so plain `git commit`
and `git push` there use the right identity and key:

```ini
# ~/work/acme/api/.git/config
[core]
    sshCommand = ssh -i '/Users/you/.ssh/id_work' -o IdentitiesOnly=yes
[user]
    name = Your Name
    email = you@acme.com
[commit]
    gpgsign = true
# plus tag.gpgsign, gpg.format and user.signingkey for the signing key
```

`gitten accounts gitconfig --install` extends the same identities to repos
you clone yourself under `~/work/acme`.

## agents.yaml: agents in workspaces

Real `gitten plan` output for the example:

```
Changes:
  + clone            github.com/your-login/api -> ~/code/your-login/api
  + clone            github.com/your-login/app -> ~/code/your-login/app
  + clone            github.com/your-login/web -> ~/code/your-login/web
  + create-workspace app: ~/workspaces/app (worktree, 4 agent(s))
  + create-copy      app/agent-1: ~/workspaces/app/agent-1
  + create-copy      app/agent-2: ~/workspaces/app/agent-2
  + create-copy      app/agent-3: ~/workspaces/app/agent-3
  + create-copy      app/agent-4: ~/workspaces/app/agent-4
  + create-workspace platform: ~/workspaces/platform (worktree, 2 agent(s))
  + create-copy      platform/agent-1/api: ~/workspaces/platform/agent-1/api
  + create-copy      platform/agent-1/web: ~/workspaces/platform/agent-1/web
  + create-copy      platform/agent-2/api: ~/workspaces/platform/agent-2/api
  + create-copy      platform/agent-2/web: ~/workspaces/platform/agent-2/web
```

Each copy is a worktree of the repo's clone on its own branch
(`agents/app/agent-1`, ...), so nothing is downloaded twice. `gitten fleet
open daily` then opens them, one tmux window per workspace:

```
tmux session gitten-daily
+-- window: app ----------------------------+  +-- window: platform -----------------+
| app/agent-1  claude | app/agent-2  claude |  | platform/agent-1 | platform/agent-2 |
|---------------------+---------------------|  |  codex           |  codex           |
| app/agent-3  claude | app/agent-4  claude |  |  (api + web)     |  (api + web)     |
+-------------------------------------------+  +-------------------------------------+
```

`gitten top` shows every agent's state and branch live, and with
`notify.on_waiting` a desktop notification says when one is waiting for you.

## safety-net.yaml: where things are kept

```
every sync:
  working copy --snapshot--> refs/gitten/backups/<copy>/<id>   (in the repo, hidden)
               --patches---> ~/.local/share/gitten/patches/<copy>/<id>/
               --push------> backup remote: refs/gitten/<machine>/<copy>/wip/<id>
                                            refs/gitten/<machine>/<copy>/heads/<branch>
  upstream     --mirror----> ~/.local/share/gitten/mirrors/github.com/<owner>/<name>.git

repo deleted upstream, 30 days later:
  clone ------move whole---> ~/.local/share/gitten/archive/<repo>-<time>/
```

`gitten restore <repo>` brings back uncommitted work, `gitten archives
restore <id>` an archived clone, and `gitten remote-sync --list` shows what
another machine can fetch from the backup remote.
