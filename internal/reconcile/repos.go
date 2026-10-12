package reconcile

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"github.com/ashtonian/git-mgr/internal/ctxutil"
	"github.com/ashtonian/git-mgr/internal/gitops"
	"github.com/ashtonian/git-mgr/internal/repourl"
	"github.com/ashtonian/git-mgr/internal/state"
)

// PlanRepos compares what config wants for each repo with what state
// records and what seen found on disk. overwrite puts git-mgr's values back
// over values changed by hand instead of reporting them as drift.
func PlanRepos(desired map[string]Repo, st *state.State, seen Seen, overwrite bool) *Plan {
	p := &Plan{}
	renamedOld := map[string]bool{}
	renames := map[string]string{}
	for key, d := range desired {
		if old := renamedFrom(d, desired, st); old != "" {
			renames[key], renamedOld[old] = old, true
		}
	}

	var first, rest []Action
	for _, key := range slices.Sorted(maps.Keys(desired)) {
		if renamedOld[key] {
			continue
		}
		d := desired[key]
		applied := st.Repos[key]
		if old, ok := renames[key]; ok {
			applied = st.Repos[old]
			if applied.ManuallyAdded {
				d.Pinned, d.Path, d.URL, d.UserTags = true, applied.LocalPath, applied.CloneURL, applied.UserTags
				d.Tags = tagUnion(d.Tags, applied.UserTags)
				d.Config = managedConfig(d.URL, d.SSHKey, d.accountConfig)
			}
			first = append(first, Action{
				Kind: KindRename, Target: key, From: old, To: key,
				Note: "same repository (provider ID " + d.ProviderID + ")",
				run:  renameRepo(old, d),
			})
		}
		if applied == nil {
			rest = append(rest, planNewRepo(p, d, seen)...)
			continue
		}
		rest = append(rest, planTrackedRepo(p, d, applied, renames[key] == "", seen, overwrite)...)
	}
	p.Actions = append(first, rest...)
	return p
}

// renamedFrom returns the tracked key that desired repo d was renamed or
// transferred from: the one entry with d's provider ID on d's host that this
// run's discovery did not find under its own key.
func renamedFrom(d Repo, desired map[string]Repo, st *state.State) string {
	if d.ProviderID == "" || st.Repos[d.Key] != nil {
		return ""
	}
	match := ""
	for key, a := range st.Repos {
		if key == d.Key || a.ProviderID != d.ProviderID || a.Host != d.Host {
			continue
		}
		if other, ok := desired[key]; ok && other.Found != nil {
			continue
		}
		if match != "" {
			return "" // ambiguous; clone rather than guess
		}
		match = key
	}
	return match
}

func planNewRepo(p *Plan, d Repo, seen Seen) []Action {
	c := seen.at(d.Path)
	switch {
	case !c.Exists:
		return []Action{{Kind: KindClone, Target: d.Key, From: d.URL, To: d.Path, run: cloneRepo(d, false)}}
	case c.is(d.Key, ""):
		return []Action{{Kind: KindAdopt, Target: d.Key, To: d.Path, Note: "already cloned there", run: cloneRepo(d, true)}}
	default:
		p.Blocked = append(p.Blocked, Blocked{Target: d.Key, Reason: occupied(d.Path, c)})
		return nil
	}
}

func planTrackedRepo(p *Plan, d Repo, a *state.RepoState, sameKey bool, seen Seen, overwrite bool) []Action {
	here := seen.at(a.LocalPath)
	if !here.Exists {
		there := seen.at(d.Path)
		switch {
		case d.Path != a.LocalPath && there.is(d.Key, a.CopyID):
			return []Action{{Kind: KindMove, Target: d.Key, From: a.LocalPath, To: d.Path, Note: "already moved; recording it",
				run: recordMove(d.Key, a.LocalPath, d.Path)}}
		case d.Path != a.LocalPath && there.Exists:
			p.Blocked = append(p.Blocked, Blocked{Target: d.Key, Reason: "gone from " + a.LocalPath + ", and " + occupied(d.Path, there)})
			return nil
		default:
			return []Action{{Kind: KindClone, Target: d.Key, From: d.URL, To: d.Path, Note: "gone from disk; cloning it again",
				run: cloneRepo(d, false)}}
		}
	}
	if here.Err != nil {
		p.Blocked = append(p.Blocked, Blocked{Target: d.Key, Reason: fmt.Sprintf("reading %s: %v", a.LocalPath, here.Err)})
		return nil
	}
	if !here.IsRepo {
		p.Blocked = append(p.Blocked, Blocked{Target: d.Key, Reason: a.LocalPath + " is not a git repository"})
		return nil
	}
	if sameKey {
		p.record = append(p.record, recordRepo(d, here))
	}

	var actions []Action
	if tags := tagUnion(a.Tags); !slices.Equal(tags, d.Tags) || a.DiscoveredBy != d.Rule {
		actions = append(actions, Action{Kind: KindTags, Target: d.Key,
			From: describeTags(tags, a.DiscoveredBy), To: describeTags(d.Tags, d.Rule), run: retag(d)})
	}
	actions = append(actions, planURL(p, d, a, here, overwrite)...)
	actions = append(actions, planConfig(p, KindConfig, d.Key, d.Config, a.ManagedConfig, here.Config, overwrite,
		func(k, v string, unset, recordOnly bool) func(context.Context, *Env) (outcome, error) {
			return setConfig(d.Key, a.LocalPath, k, v, unset, recordOnly)
		})...)
	if !d.Pinned && d.Path != a.LocalPath {
		if there := seen.at(d.Path); there.Exists {
			p.Blocked = append(p.Blocked, Blocked{Target: d.Key, Reason: "cannot move from " + a.LocalPath + ": " + occupied(d.Path, there)})
		} else {
			actions = append(actions, Action{Kind: KindMove, Target: d.Key, From: a.LocalPath, To: d.Path, Disruptive: true,
				run: moveRepo(d.Key, a.LocalPath, d.Path)})
		}
	}
	return actions
}

func planURL(p *Plan, d Repo, a *state.RepoState, here Copy, overwrite bool) []Action {
	want, recorded, have := d.URL, a.CloneURL, here.Origin
	set := func(from, note string) []Action {
		return []Action{{Kind: KindURL, Target: d.Key, From: from, To: want, Note: note, run: setURL(d.Key, a.LocalPath, want)}}
	}
	switch {
	case have == want && recorded == want:
		return nil
	case have == want:
		return set(recorded, "already set; recording it")
	case have == recorded:
		return set(recorded, "")
	case overwrite:
		return set(have, "changed by hand; restoring")
	default:
		p.Drift = append(p.Drift, Drift{Target: d.Key, What: "origin URL", Recorded: recorded, Found: orUnset(have), Wanted: want})
		return nil
	}
}

// configSetter builds the action that applies one managed config change to a
// copy and records it.
type configSetter func(key, value string, unset, recordOnly bool) func(context.Context, *Env) (outcome, error)

// planConfig plans one copy's managed git config: want is what config asks
// for, recorded what git-mgr set, have what is set now. A key set by hand is
// taken over when it already holds the wanted value, reported as drift when
// it doesn't, and dropped from management when config stops wanting it.
func planConfig(p *Plan, kind Kind, target string, want, recorded, have map[string]string, overwrite bool, set configSetter) []Action {
	keys := map[string]bool{}
	for k := range want {
		keys[k] = true
	}
	for k := range recorded {
		keys[k] = true
	}
	var actions []Action
	for _, k := range slices.Sorted(maps.Keys(keys)) {
		w, wantOK := want[k]
		rec, recOK := recorded[k]
		h, haveOK := have[k]
		act := func(from, to string, unset, recordOnly bool, note string) {
			actions = append(actions, Action{Kind: kind, Target: target, Field: k, From: from, To: to, Note: note,
				run: set(k, to, unset, recordOnly)})
		}
		switch {
		case wantOK && haveOK && h == w:
			if !recOK || rec != w {
				act(h, w, false, true, "already set; now managed")
			}
		case wantOK && ((!recOK && !haveOK) || (recOK && haveOK && h == rec)):
			act(orUnset(h), w, false, false, "")
		case wantOK && overwrite:
			act(orUnset(h), w, false, false, "changed by hand; restoring")
		case wantOK:
			p.Drift = append(p.Drift, Drift{Target: target, What: k, Recorded: orUnset(rec), Found: orUnset(h), Wanted: w})
		case haveOK && h == rec:
			act(rec, "", true, false, "no longer configured")
		default:
			act(orUnset(h), "", true, true, "changed by hand; no longer managed")
		}
	}
	return actions
}

func describeTags(tags []string, rule string) string {
	s := "[" + strings.Join(tags, " ") + "]"
	if rule != "" {
		s += " rule " + rule
	}
	return s
}

func orUnset(v string) string {
	if v == "" {
		return "(unset)"
	}
	return v
}

func occupied(path string, c Copy) string {
	switch {
	case c.Err != nil:
		return fmt.Sprintf("%s can't be read: %v", path, c.Err)
	case !c.IsRepo:
		return path + " is already taken by something that isn't a git repository"
	case c.Origin != "":
		return path + " already holds a clone of " + c.Origin
	default:
		return path + " already holds another repository"
	}
}

// recordRepo stores what this run observed of a tracked repo: that it is on
// disk, its copy ID (created if missing), its provider ID and whether the
// provider has archived it.
func recordRepo(d Repo, here Copy) func(context.Context, *state.State) {
	return func(ctx context.Context, st *state.State) {
		a := st.Repos[d.Key]
		if a == nil {
			return
		}
		a.Cloned = true
		if d.ProviderID != "" {
			a.ProviderID = d.ProviderID
		}
		switch {
		case here.CopyID != "":
			a.CopyID = here.CopyID
		default:
			if id, err := gitops.CopyID(ctx, a.LocalPath); err == nil {
				a.CopyID = id
			}
		}
		if d.Found != nil {
			a.IsArchived = d.Found.IsArchived
		}
	}
}

// cloneRepo clones d to its path, or with adopt takes the clone already
// there, then writes its managed git config and records it. The clone runs
// outside the state lock, within env.CloneTimeout; one that fails removes
// what it left behind, so a half-made clone is never adopted later.
func cloneRepo(d Repo, adopt bool) func(context.Context, *Env) (outcome, error) {
	return func(ctx context.Context, env *Env) (outcome, error) {
		if !adopt {
			if err := cloneInto(ctx, env, d); err != nil {
				return outcome{record: func(st *state.State) error {
					if a := st.Repos[d.Key]; a != nil {
						a.Cloned, a.Status = false, "missing"
					}
					return nil
				}}, err
			}
		}
		c := observeCopy(ctx, d.Path)
		if !c.IsRepo || c.Err != nil {
			return outcome{}, fmt.Errorf("%s is not a readable git repository", d.Path)
		}
		if c.Origin != d.URL {
			if err := gitops.SetOriginURL(ctx, d.Path, d.URL); err != nil {
				return outcome{}, err
			}
		}
		managed, err := writeManagedConfig(ctx, d.Path, d.Config, c.Config)
		if err != nil {
			return outcome{}, err
		}
		copyID, err := gitops.CopyID(ctx, d.Path)
		if err != nil {
			return outcome{}, err
		}

		return outcome{record: func(st *state.State) error {
			a := st.Repos[d.Key]
			if a == nil {
				a = &state.RepoState{ManuallyAdded: d.Pinned}
				st.Repos[d.Key] = a
			}
			a.Host, a.Owner, a.Name, a.FullName = d.Host, d.Owner, d.Name, d.FullName
			a.CloneURL, a.LocalPath, a.CopyID, a.ManagedConfig = d.URL, d.Path, copyID, managed
			a.Tags, a.UserTags, a.DiscoveredBy = d.Tags, d.UserTags, d.Rule
			a.Cloned, a.Status = true, "clean"
			if d.ProviderID != "" {
				a.ProviderID = d.ProviderID
			}
			if d.Found != nil {
				a.IsArchived = d.Found.IsArchived
			}
			return nil
		}}, nil
	}
}

func cloneInto(ctx context.Context, env *Env, d Repo) error {
	sshKey := d.SSHKey
	if repourl.IsHTTP(d.URL) {
		sshKey = ""
	}
	ctx, cancel := ctxutil.WithTimeout(ctx, env.CloneTimeout)
	defer cancel()
	return ctxutil.TimedOut(ctx, gitops.Clone(ctx, d.URL, d.Path, sshKey), "clone", env.CloneTimeout, "sync.clone_timeout")
}

// writeManagedConfig sets each wanted key that isn't set, and records it.
// A key already set to the wanted value is recorded too; one set to something
// else was set by hand and is left for the next plan to report.
func writeManagedConfig(ctx context.Context, path string, want, have map[string]string) (map[string]string, error) {
	managed := map[string]string{}
	for _, k := range slices.Sorted(maps.Keys(want)) {
		v := want[k]
		cur, set := have[k]
		switch {
		case set && cur == v:
		case set:
			continue
		default:
			if err := gitops.SetLocalConfig(ctx, path, k, v); err != nil {
				return managed, err
			}
		}
		managed[k] = v
	}
	if len(managed) == 0 {
		return nil, nil
	}
	return managed, nil
}

func renameRepo(old string, d Repo) func(context.Context, *Env) (outcome, error) {
	return func(context.Context, *Env) (outcome, error) {
		return outcome{record: func(st *state.State) error {
			a := st.Repos[old]
			if a == nil {
				return fmt.Errorf("%s is no longer tracked", old)
			}
			if st.Repos[d.Key] != nil {
				return fmt.Errorf("%s is already tracked", d.Key)
			}
			a.Host, a.Owner, a.Name, a.FullName, a.ProviderID = d.Host, d.Owner, d.Name, d.FullName, d.ProviderID
			st.Repos[d.Key] = a
			delete(st.Repos, old)
			for _, ws := range st.Workgroups {
				for i := range ws.Members {
					if ws.Members[i].RepoKey == old {
						ws.Members[i].RepoKey = d.Key
					}
				}
			}
			return nil
		}}, nil
	}
}

func retag(d Repo) func(context.Context, *Env) (outcome, error) {
	return func(context.Context, *Env) (outcome, error) {
		return outcome{record: func(st *state.State) error {
			a, err := trackedRepo(st, d.Key)
			if err != nil {
				return err
			}
			a.Tags, a.DiscoveredBy = d.Tags, d.Rule
			return nil
		}}, nil
	}
}

// setURL sets the origin URL of the copy at path, where the repo key was
// when the plan was made.
func setURL(key, path, url string) func(context.Context, *Env) (outcome, error) {
	return func(ctx context.Context, _ *Env) (outcome, error) {
		if have, err := gitops.Git(ctx, path, "remote", "get-url", "origin"); err != nil || have != url {
			if err := gitops.SetOriginURL(ctx, path, url); err != nil {
				return outcome{}, err
			}
		}
		return outcome{record: func(st *state.State) error {
			a, err := trackedRepo(st, key)
			if err == nil {
				a.CloneURL = url
			}
			return err
		}}, nil
	}
}

// setConfig sets or unsets one managed git config key of the copy at path,
// where the repo key was when the plan was made.
func setConfig(key, path, name, value string, unset, recordOnly bool) func(context.Context, *Env) (outcome, error) {
	return func(ctx context.Context, _ *Env) (outcome, error) {
		if !recordOnly {
			if err := writeConfig(ctx, path, name, value, unset); err != nil {
				return outcome{}, err
			}
		}
		return outcome{record: func(st *state.State) error {
			a, err := trackedRepo(st, key)
			if err == nil {
				recordConfig(&a.ManagedConfig, name, value, unset)
			}
			return err
		}}, nil
	}
}

func trackedRepo(st *state.State, key string) (*state.RepoState, error) {
	if a := st.Repos[key]; a != nil {
		return a, nil
	}
	return nil, fmt.Errorf("%s is no longer tracked", key)
}

// writeConfig sets or unsets one git config key in the copy at path.
func writeConfig(ctx context.Context, path, name, value string, unset bool) error {
	if unset {
		return gitops.UnsetLocalConfig(ctx, path, name)
	}
	return gitops.SetLocalConfig(ctx, path, name, value)
}

// recordConfig records a managed key's new value, or that it is no longer
// managed.
func recordConfig(managed *map[string]string, name, value string, unset bool) {
	if unset {
		delete(*managed, name)
		if len(*managed) == 0 {
			*managed = nil
		}
		return
	}
	if *managed == nil {
		*managed = map[string]string{}
	}
	(*managed)[name] = value
}

// moveRepo renames the repo's directory to its desired path, reconnects its
// linked worktrees and points workgroups that use it as their source at the
// new path. It refuses a move across filesystems, which would be a copy. The
// rename is made as the move is recorded, under the state lock, so no other
// run sees the repo missing from the path state records; so is the tidying of
// the parent directories it emptied, so no other move or clone of this run
// has a directory removed from under it.
func moveRepo(key, from, to string) func(context.Context, *Env) (outcome, error) {
	return func(_ context.Context, env *Env) (outcome, error) {
		return outcome{
			record: func(st *state.State) error {
				if _, err := os.Lstat(to); err == nil {
					return fmt.Errorf("cannot move to %s: it already exists", to)
				}
				if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
					return err
				}
				if err := os.Rename(from, to); err != nil {
					if errors.Is(err, syscall.EXDEV) {
						return fmt.Errorf("%s and %s are on different filesystems; move it yourself and run git-mgr apply to record it", from, to)
					}
					return err
				}
				recordMoved(st, key, from, to)
				env.noteMove(from, to)
				removeEmptyParents(env, from)
				return nil
			},
			after: func(ctx context.Context) (string, error) {
				if err := gitops.WorktreeRepair(ctx, to); err != nil {
					return "", fmt.Errorf("moved, but reconnecting its worktrees failed (run `git worktree repair` in %s): %w", to, err)
				}
				return "", nil
			},
		}, nil
	}
}

func recordMove(key, from, to string) func(context.Context, *Env) (outcome, error) {
	return func(_ context.Context, env *Env) (outcome, error) {
		env.noteMove(from, to)
		return outcome{record: func(st *state.State) error {
			recordMoved(st, key, from, to)
			return nil
		}}, nil
	}
}

func recordMoved(st *state.State, key, from, to string) {
	if a := st.Repos[key]; a != nil {
		a.LocalPath = to
	}
	for _, ws := range st.Workgroups {
		if ws.Source == from {
			ws.Source = to
		}
	}
}
