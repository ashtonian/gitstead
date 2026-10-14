// Package discovery finds the repositories config's discovery rules name on
// GitHub (and GitHub Enterprise), GitLab and Gitea/Forgejo, and judges which
// tracked repos have gone stale.
//
// RunDiscovery runs every rule, or those a rule name or tag selects, in
// turn, each within sync.discovery_timeout; one that fails is reported and
// the rest still run. Each Provider lists an owner's, organization's,
// group's or search's repositories, applying what filters it can server-side
// and reporting which (ProviderResult.Applied); the rest, and config's
// excludes, are applied here, with per-repo language and topic lookups only
// when a rule filters on them. A repo several rules find keeps the first
// rule as its discoverer and the union of their tags; config's repos:
// entries are added last. A rule whose listing was capped or whose lookups
// failed is marked Incomplete, so none of its repos are judged stale.
//
// Tokens come from the one source a provider configures (ResolveToken), and
// API calls retry on rate limits and server errors within the provider's
// API timeout. Cleanup applies cleanup.policy to tracked repos discovery no
// longer finds, only within the rules that ran completely (StaleScopeOf):
// marking them stale first, then, past the grace period, untracking,
// archiving or deleting them through a RemoveFunc the caller supplies.
package discovery
