# Finding your repos

[Documentation](README.md) · [Quick start](../README.md#quick-start)

- [Matching names](#matching-names)
- [Providers](#providers)
- [Discovery rules](#discovery-rules)
- [Cloning a selection](#cloning-a-selection)
- [Raw search queries](#raw-search-queries)

gitstead finds repos by owner -- you, an organization, a GitLab group -- or by
search, narrowed by words in their names, language or topics. Every way in
shows what that finds before anything is cloned, and lets you pick from the
list. What you keep becomes a discovery rule: every sync clones what newly
matches it.

**The first time**, `gitstead setup` walks through it (prompts condensed; each
answer starts at what it found, so Enter mostly accepts):

```
$ gitstead setup
Looking at this machine: gh accounts, SSH keys and config, git identities, existing clones...

? Whose repos should this account track?
    [ ] jdoe   (your own repos, private ones included)
  > [x] acme   (organization)
? Track only some of their repos?  Yes
? Only names like:  billing, payments
Looking up what jdoe tracks...
  acme, named billing or payments: 23 repos
    by name: billing 15, payments 9
    billing-api, billing-ledger, billing-worker, ..., and 11 more
? Track these 23 repos with jdoe?
  > Yes, all of them
    Pick from the list
    No, change whose repos or the filters
...
? Clone the 23 repos your accounts track now?  Yes
```

`by name` shows what each word matched, so one that catches more than you
meant stands out. **Pick from the list** opens a checklist of every match,
all ticked: untick the ones you don't want and they are skipped by name from
then on (the rule's `skip`), while new matches still arrive.

**Later**, add more the same way -- another team's repos, another org --
without touching what you have:

```
$ gitstead clone --org acme --name ledger --save acme-ledger
Searching acme's repos on github.com...
4 repos match:
  + github.com/acme/ledger-api
  + github.com/acme/ledger-exports
  + github.com/acme/ledger-legacy
  + github.com/acme/ledger-web
? Clone these 4 repos?  Pick from the list
? Which to clone  [x] ledger-api  [x] ledger-exports  [ ] ledger-legacy  [x] ledger-web
Saved discovery rule "acme-ledger" in ~/.config/gitstead/config.yaml
```

Each saved rule keeps working on its own: every `sync` clones what newly
matches it. To see or change what's tracked:

- `gitstead discover` shows what every rule finds now and what would change,
  and ends saying nothing was changed, how many repos would be cloned and what
  would clone them -- and whether a background sync will.
- `gitstead setup` again changes an account's owners and filters, starting from
  what is set up; rules you added with `clone` stay as they are.
- `gitstead clone ... --dry-run` lists a selection without asking anything.

## Matching names

| You write | Matches | Doesn't match |
| --- | --- | --- |
| `billing` | `billing`, `billing-api`, `team-billing`, `BillingPortal`, `billing_v2` | `rebilling`, `billings` |
| `billing-api` | `billing-api`, `old-billing-api-v2` (those words, in order) | `billing-web-api` |
| `billing-*` | names starting `billing-` | `team-billing-api` |
| `*bill*` | anything containing `bill` (`rebilling` too) | |

A word without `*`, `?` or `[...]` matches whole words of the name, which is
split at `-`, `_`, `.` and other punctuation, where camelCase changes case,
and where letters meet digits; with one, it's a glob over the whole name.
Case never matters. Several are alternatives: a repo matching any of them is
kept. A `name_pattern` regular expression narrows further, and a rule's
`skip` leaves out repos by their exact names.

## Providers

Any number of GitHub, GitLab, and Gitea/Forgejo instances:

```yaml
providers:
  - name: "github.com"
    type: "github"                    # github | gitlab | gitea | forgejo
    token_command: "gh auth token"

  - name: "ghes.company.com"
    type: "github"
    url: "https://ghes.company.com"   # GitHub Enterprise
    token_env: "GHES_TOKEN"
    ssh_key: "~/.ssh/id_work"

  - name: "gitlab.company.com"
    type: "gitlab"
    url: "https://gitlab.company.com"
    token_env: "GITLAB_COMPANY_TOKEN"

  - name: "gitea.company.com"
    type: "gitea"                     # "forgejo" is an alias
    url: "https://gitea.company.com"  # /api/v1 is handled by the SDK
    token_env: "GITEA_TOKEN"
```

Each provider takes its token from the one source it sets: `token`,
`token_env` or `token_command`. If that source yields nothing — an unset
variable, a failing command — discovery for that provider fails rather than
trying another source, which could send one account's token to another
account's API. A provider that sets none uses `GITHUB_TOKEN` / `GITLAB_TOKEN` /
`GITEA_TOKEN` / `FORGEJO_TOKEN`, and without those sees only public repos.

A repo's key and `{{.Host}}` are its host without a port --
`gitea.company.com/acme/app` for a provider at `https://gitea.company.com:3000`
-- the same key a clone of it gets when adopted, over SSH or HTTPS, and the
host an account's `hosts:` names.

## Discovery rules

Each rule selects repos from one provider. Use `org`/`user` for GitHub and
Gitea, `group`/`user` for GitLab. A rule with none of them lists the token's own
repos, private ones included; a named `user` lists only that user's public
repos. Repos matching **any** rule are included.

```yaml
discovery:
  - name: "personal"
    provider: "github.com"            # no user/org: the token's own repos
    affiliation: [owner]              # GitHub: owner | collaborator | organization_member
    tags: ["personal"]

  - name: "work-services"
    provider: "ghes.company.com"
    org: "platform"
    visibility: "all"                 # all | public | private | internal
    languages: ["Go", "Rust"]         # any of these, case-insensitive
    topics: ["service"]               # repo must have ALL listed topics
    names: [billing, "svc-*"]         # words in the name, or globs; any of them (see Matching names)
    # name_pattern: "^svc-"           # or a regular expression; with names, both must match
    skip: [billing-legacy]            # repos left out by exact name (what picking off the list writes)
    include_forks: false
    include_archived: false
    tags: ["work", "services"]
    ssh_key: "~/.ssh/id_work"
```

Filters are pushed to the server where the provider's list API supports it, and
applied locally otherwise (per-repo language/topic lookups happen only for repos
that pass the cheaper filters). When a provider genuinely can't honor a filter,
`discover` prints a warning rather than silently returning nothing.

`gitstead discover` on its own is a preview: it lists what each rule found and
what would change, then says so -- nothing was cloned or changed, how many
repos would be cloned, the commands that would make the changes (`discover
--apply`, `apply`) or change what's tracked (`setup`, `clone ... --save`),
and whether a background sync is installed that will make them on its next
run. With no background sync, nothing changes until you run one of those.

## Cloning a selection

To clone a set of repos without writing a rule first, give `clone` a
selection instead of a URL. It runs the same discovery a rule does, lists what
matches, and on a terminal asks: clone them all, pick from the list, or none.
It clones only what isn't tracked yet:

```bash
gitstead clone --org acme --dry-run                       # everything in acme: list only
gitstead clone --org acme --name billing,payments         # acme's repos with billing or payments as a word in the name
gitstead clone --org acme --language go --topic service  # acme's Go services
gitstead clone --user someone --name 'terraform-*'        # someone's public terraform-* repos
gitstead clone --mine --language rust                     # your own Rust repos, private ones included
gitstead clone --query "org:acme payments in:name,description,readme archived:false"
```

| Flag | Selects |
| --- | --- |
| `--org`, `--user`, `--group` | An organization's, a user's public, or a GitLab group's repos. |
| `--mine` | The repos your token's account owns, private ones included. |
| `--query` | What the provider's own search finds; no other filter applies. |
| `--language` | Only repos in one of these languages (repeat it, or comma-separate). |
| `--topic` | Only repos with all of these topics. |
| `--name` | Only repos whose name has one of these words, or matches one of these globs (`billing`, `'svc-*'`; see [Matching names](#matching-names)); repeat it, or comma-separate. |
| `--name-pattern` | Only repos whose name matches this regular expression. |
| `--visibility`, `--forks`, `--archived` | Public, private or internal only; include forks; include archived repos. |
| `--provider` | Which configured provider to search: the only one by default. With none configured, github.com, with gh's token when gh is logged in there and `GITHUB_TOKEN` isn't set. |
| `--tag` | Tags for what it clones. |

On a terminal it then asks whether to keep the selection as a discovery rule
-- named after what it selects, such as `acme-billing-payments`, or as
`--save <name>` says -- written into your config (with the provider, if
config didn't have it), the repos you picked off as its `skip`. Kept, every
`sync` clones what newly matches, follows renames and flags what stops
matching, as with any rule. Not kept (`--no-save`, and the default without a
terminal), its repos are tracked as cloned by hand: they keep their place and
are never judged stale. Without a terminal, or with `--json`, it clones the
whole selection without asking.

## Raw search queries

For power users, a rule can pass a raw `query:` straight to the provider's native
search API instead of the structured selectors/filters. It is mutually exclusive
with `user`/`org`/`group` and the structured filters:

```yaml
discovery:
  - name: "go-stars"
    provider: "github.com"
    query: "org:acme language:go stars:>50"
    tags: ["oss"]
```

GitHub's Search API caps results at 1000 per query; when a query matches more,
`discover` reports the rule as incomplete and none of its repos are judged
stale that run (list-based `org:`/`user:` rules have no such cap). GitLab and Gitea route `query:` to their own project/repo
search.
