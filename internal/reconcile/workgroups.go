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

	"github.com/ashtonian/git-mgr/internal/gitops"
	"github.com/ashtonian/git-mgr/internal/repourl"
	"github.com/ashtonian/git-mgr/internal/state"
	"github.com/ashtonian/git-mgr/internal/workgroup"
)

// PlanWorkgroups compares what config wants for each workgroup with what
// state records and what seen found on disk. only, when not nil, limits the
// plan to the workgroups it accepts.
func PlanWorkgroups(desired map[string]Workgroup, st *state.State, seen Seen, only func(name string) bool, overwrite bool) *Plan {
	p := &Plan{}
	names := map[string]bool{}
	for name := range desired {
		names[name] = true
	}
	for name := range st.Workgroups {
		names[name] = true
	}
	for _, name := range slices.Sorted(maps.Keys(names)) {
		if only != nil && !only(name) {
			continue
		}
		w, wanted := desired[name]
		ws := st.Workgroups[name]
		switch {
		case !wanted && !ws.Declared:
			// Created from flags or adopted: there is no declaration to follow.
		case !wanted:
			p.Actions = append(p.Actions, Action{
				Kind: KindRemoveWorkgroup, Target: name, From: ws.Root, Disruptive: true,
				Note: fmt.Sprintf("no longer declared; %d member(s) archived and removed", len(ws.Members)),
				run:  removeWorkgroup(name),
			})
		case ws == nil:
			p.Actions = append(p.Actions, Action{
				Kind: KindCreateWorkgroup, Target: name, To: w.Spec.Root,
				Note: fmt.Sprintf("%s, %d member(s)", w.Spec.Kind, len(w.Spec.Members)),
				run:  createWorkgroup(w),
			})
		default:
			planMembers(p, w, ws, seen, overwrite)
		}
	}
	return p
}

func planMembers(p *Plan, w Workgroup, ws *state.WorkgroupState, seen Seen, overwrite bool) {
	spec, name := w.Spec, w.Spec.Name
	block := func(target, reason string) { p.Blocked = append(p.Blocked, Blocked{Target: target, Reason: reason}) }
	if ws.Kind != spec.Kind {
		block(name, fmt.Sprintf("kind changed from %s to %s; remove it (git-mgr wg rm %s --delete) to recreate it", ws.Kind, spec.Kind, name))
		return
	}
	if spec.Kind == "worktree" && ws.Source != spec.Source {
		block(name, fmt.Sprintf("source changed from %s to %s; remove it (git-mgr wg rm %s --delete) to recreate it", ws.Source, spec.Source, name))
		return
	}
	if ws.Root != spec.Root || ws.BaseRef != spec.BaseRef || ws.Declared != w.Declared {
		p.Actions = append(p.Actions, Action{Kind: KindWorkgroup, Target: name, From: ws.Root, To: spec.Root,
			run: updateWorkgroup(name, spec.Root, spec.BaseRef, w.Declared)})
	}

	applied := map[string]state.WorkgroupMemberState{}
	for _, m := range ws.Members {
		applied[m.Name] = m
	}
	for _, m := range spec.Members {
		target := name + "/" + m.Name
		am, ok := applied[m.Name]
		here := seen.at(m.Path)
		create := func(note string) {
			p.Actions = append(p.Actions, Action{Kind: KindCreateMember, Target: target, To: m.Path, Note: note, run: createMember(w, m)})
		}
		switch {
		case !ok && !here.Exists:
			create("")
		case !ok && isMember(here, spec, m, seen, ""):
			p.Actions = append(p.Actions, Action{Kind: KindCreateMember, Target: target, To: m.Path,
				Note: "already there; recording it", run: recordMember(w, m)})
		case !ok:
			block(target, occupied(m.Path, here))
		case am.Path != m.Path:
			old := seen.at(am.Path)
			switch {
			case !old.Exists && isMember(here, spec, m, seen, am.CopyID):
				p.Actions = append(p.Actions, Action{Kind: KindMoveMember, Target: target, From: am.Path, To: m.Path,
					Note: "already moved; recording it", run: recordMemberMove(name, m)})
			case here.Exists:
				block(target, "cannot move from "+am.Path+": "+occupied(m.Path, here))
			case !old.Exists:
				create("gone from disk; creating it again")
			default:
				p.Actions = append(p.Actions, Action{Kind: KindMoveMember, Target: target, From: am.Path, To: m.Path,
					Disruptive: true, run: moveMember(w, am, m)})
			}
		case !here.Exists:
			create("gone from disk; creating it again")
		case !here.IsRepo || here.Err != nil:
			block(target, m.Path+" is not a readable git repository")
		case spec.Kind == "clone" && am.RepoURL != m.RepoURL:
			planMemberURL(p, target, name, am, m, here)
		case spec.Kind == "worktree" && am.Branch != m.Branch:
			block(target, fmt.Sprintf("branch changed in config from %s to %s; an existing member keeps its branch", am.Branch, m.Branch))
		}
		if ok && am.Path == m.Path && here.IsRepo && here.Err == nil && spec.Kind == "clone" {
			p.Actions = append(p.Actions, planConfig(p, KindMemberConfig, target, w.Configs[m.Name], am.ManagedConfig, here.Config, overwrite,
				func(k, v string, unset, recordOnly bool) func(context.Context, *Env) (outcome, error) {
					return setMemberConfig(name, m.Name, k, v, unset, recordOnly)
				})...)
		}
	}

	wanted := map[string]bool{}
	for _, m := range spec.Members {
		wanted[m.Name] = true
	}
	for _, am := range ws.Members {
		if wanted[am.Name] {
			continue
		}
		target := name + "/" + am.Name
		if !seen.at(am.Path).Exists {
			p.Actions = append(p.Actions, Action{Kind: KindForgetMember, Target: target, From: am.Path,
				Note: "no longer declared and gone from disk", run: forgetMember(name, am.Name)})
			continue
		}
		p.Actions = append(p.Actions, Action{Kind: KindRemoveMember, Target: target, From: am.Path, Disruptive: true,
			Note: "no longer declared; unique work archived first", run: removeMember(name, am.Name)})
	}
}

func planMemberURL(p *Plan, target, name string, am state.WorkgroupMemberState, m workgroup.Member, here Copy) {
	switch here.Origin {
	case m.RepoURL, am.RepoURL:
		note := ""
		if here.Origin == m.RepoURL {
			note = "already set; recording it"
		}
		p.Actions = append(p.Actions, Action{Kind: KindMemberURL, Target: target, From: am.RepoURL, To: m.RepoURL, Note: note,
			run: setMemberURL(name, m.Name, m.RepoURL)})
	default:
		p.Drift = append(p.Drift, Drift{Target: target, What: "origin URL", Recorded: am.RepoURL, Found: orUnset(here.Origin), Wanted: m.RepoURL})
	}
}

// isMember reports whether the copy c is member m: by copy ID when both have
// one; otherwise a worktree member must belong to the group's source repo and
// a clone member must be a clone of its repo.
func isMember(c Copy, spec *workgroup.Spec, m workgroup.Member, seen Seen, copyID string) bool {
	if !c.IsRepo || c.Err != nil {
		return false
	}
	if copyID != "" && c.CopyID != "" {
		return c.CopyID == copyID
	}
	if spec.Kind == "worktree" {
		src := seen.at(spec.Source)
		return src.IsRepo && src.CommonDir != "" && c.CommonDir == src.CommonDir
	}
	have, err1 := repourl.Parse(c.Origin)
	want, err2 := repourl.Parse(m.RepoURL)
	return err1 == nil && err2 == nil && have.Key() == want.Key()
}

// resolvedSpec copies spec with its source moved to wherever this run moved it.
func resolvedSpec(env *Env, spec *workgroup.Spec, members ...workgroup.Member) *workgroup.Spec {
	s := *spec
	s.Source = env.resolve(spec.Source)
	if members != nil {
		s.Members = members
	}
	return &s
}

func sshKeyFor(w Workgroup) func(workgroup.Member) string {
	return func(m workgroup.Member) string { return w.SSHKeys[m.Name] }
}

// memberState records member m, writing the managed git config it wants
// that isn't set yet.
func memberState(ctx context.Context, m workgroup.Member, want map[string]string) (state.WorkgroupMemberState, error) {
	ms := state.WorkgroupMemberState{Name: m.Name, Path: m.Path, Branch: m.Branch, RepoURL: m.RepoURL, RepoKey: m.RepoKey}
	id, err := gitops.CopyID(ctx, m.Path)
	if err != nil {
		return ms, err
	}
	ms.CopyID = id
	if len(want) > 0 {
		have, err := gitops.LocalConfig(ctx, m.Path)
		if err != nil {
			return ms, err
		}
		if ms.ManagedConfig, err = writeManagedConfig(ctx, m.Path, want, have); err != nil {
			return ms, err
		}
	}
	return ms, nil
}

func workgroupState(st *state.State, name string) (*state.WorkgroupState, error) {
	ws := st.Workgroups[name]
	if ws == nil {
		return nil, fmt.Errorf("workgroup %s is no longer recorded", name)
	}
	return ws, nil
}

// member finds a recorded member of a workgroup.
func member(st *state.State, name, memberName string) (*state.WorkgroupMemberState, error) {
	ws, err := workgroupState(st, name)
	if err != nil {
		return nil, err
	}
	for i := range ws.Members {
		if ws.Members[i].Name == memberName {
			return &ws.Members[i], nil
		}
	}
	return nil, fmt.Errorf("%s/%s is no longer recorded", name, memberName)
}

func upsertMember(ws *state.WorkgroupState, ms state.WorkgroupMemberState) {
	for i := range ws.Members {
		if ws.Members[i].Name == ms.Name {
			ws.Members[i] = ms
			return
		}
	}
	ws.Members = append(ws.Members, ms)
}

func dropMember(ws *state.WorkgroupState, member string) {
	ws.Members = slices.DeleteFunc(ws.Members, func(m state.WorkgroupMemberState) bool { return m.Name == member })
}

// createWorkgroup creates every member, outside the state lock, then records
// the workgroup with the members that were made.
func createWorkgroup(w Workgroup) func(context.Context, *Env) (outcome, error) {
	return func(ctx context.Context, env *Env) (outcome, error) {
		spec := resolvedSpec(env, w.Spec)
		statuses, err := workgroup.Ensure(ctx, spec, sshKeyFor(w))
		if err != nil {
			return outcome{}, err
		}
		ws := &state.WorkgroupState{
			Name: spec.Name, Kind: spec.Kind, Root: spec.Root, Source: spec.Source, BaseRef: spec.BaseRef,
			CreatedAt: env.now(), Declared: w.Declared,
		}
		var errs []error
		for _, s := range statuses {
			if s.Err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", s.Name, s.Err))
				continue
			}
			ms, err := memberState(ctx, s.Member, w.Configs[s.Name])
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", s.Name, err))
			}
			ws.Members = append(ws.Members, ms)
		}
		tracking := ""
		return outcome{
			record: func(st *state.State) error {
				st.Workgroups[spec.Name] = ws
				if w.Track {
					added, skipped := workgroup.TrackRepos(ctx, st, spec)
					tracking = fmt.Sprintf("tracking %d repo(s) for sync", added)
					if len(skipped) > 0 {
						tracking += "; not tracking " + strings.Join(skipped, ", ") + " (no usable origin)"
					}
				}
				return nil
			},
			after: func(context.Context) (string, error) { return tracking, nil },
		}, errors.Join(errs...)
	}
}

func createMember(w Workgroup, m workgroup.Member) func(context.Context, *Env) (outcome, error) {
	return func(ctx context.Context, env *Env) (outcome, error) {
		if _, err := workgroupState(env.State, w.Spec.Name); err != nil {
			return outcome{}, err
		}
		statuses, err := workgroup.Ensure(ctx, resolvedSpec(env, w.Spec, m), sshKeyFor(w))
		if err != nil {
			return outcome{}, err
		}
		if statuses[0].Err != nil {
			return outcome{}, statuses[0].Err
		}
		ms, err := memberState(ctx, m, w.Configs[m.Name])
		return outcome{record: recordMemberState(w.Spec.Name, ms)}, err
	}
}

func recordMember(w Workgroup, m workgroup.Member) func(context.Context, *Env) (outcome, error) {
	return func(ctx context.Context, _ *Env) (outcome, error) {
		ms, err := memberState(ctx, m, w.Configs[m.Name])
		return outcome{record: recordMemberState(w.Spec.Name, ms)}, err
	}
}

func recordMemberState(name string, ms state.WorkgroupMemberState) func(*state.State) error {
	return func(st *state.State) error {
		ws, err := workgroupState(st, name)
		if err == nil {
			upsertMember(ws, ms)
		}
		return err
	}
}

func setMemberConfig(name, memberName, key, value string, unset, recordOnly bool) func(context.Context, *Env) (outcome, error) {
	return func(ctx context.Context, env *Env) (outcome, error) {
		m, err := member(env.State, name, memberName)
		if err != nil {
			return outcome{}, err
		}
		if !recordOnly {
			if err := writeConfig(ctx, m.Path, key, value, unset); err != nil {
				return outcome{}, err
			}
		}
		return outcome{record: func(st *state.State) error {
			m, err := member(st, name, memberName)
			if err == nil {
				recordConfig(&m.ManagedConfig, key, value, unset)
			}
			return err
		}}, nil
	}
}

// moveMember moves a member as it is recorded, under the state lock, as
// moveRepo moves a repo.
func moveMember(w Workgroup, am state.WorkgroupMemberState, m workgroup.Member) func(context.Context, *Env) (outcome, error) {
	return func(ctx context.Context, env *Env) (outcome, error) {
		from := workgroup.Member{Name: am.Name, Path: am.Path, Branch: am.Branch, RepoURL: am.RepoURL, RepoKey: am.RepoKey}
		return outcome{
			record: func(st *state.State) error {
				rec, err := member(st, w.Spec.Name, am.Name)
				if err != nil {
					return err
				}
				if err := workgroup.MoveMember(ctx, resolvedSpec(env, w.Spec), from, m.Path); err != nil {
					return err
				}
				rec.Path = m.Path
				return nil
			},
			after: func(context.Context) (string, error) {
				_ = os.Remove(filepath.Dir(am.Path)) // the old root, once its last member has moved
				return "", nil
			},
		}, nil
	}
}

func recordMemberMove(name string, m workgroup.Member) func(context.Context, *Env) (outcome, error) {
	return func(context.Context, *Env) (outcome, error) {
		return outcome{record: func(st *state.State) error {
			rec, err := member(st, name, m.Name)
			if err == nil {
				rec.Path = m.Path
			}
			return err
		}}, nil
	}
}

func setMemberURL(name, memberName, url string) func(context.Context, *Env) (outcome, error) {
	return func(ctx context.Context, env *Env) (outcome, error) {
		m, err := member(env.State, name, memberName)
		if err != nil {
			return outcome{}, err
		}
		if err := gitops.SetOriginURL(ctx, m.Path, url); err != nil {
			return outcome{}, err
		}
		return outcome{record: func(st *state.State) error {
			m, err := member(st, name, memberName)
			if err == nil {
				m.RepoURL = url
			}
			return err
		}}, nil
	}
}

func updateWorkgroup(name, root, baseRef string, declared bool) func(context.Context, *Env) (outcome, error) {
	return func(context.Context, *Env) (outcome, error) {
		return outcome{record: func(st *state.State) error {
			ws, err := workgroupState(st, name)
			if err == nil {
				ws.Root, ws.BaseRef, ws.Declared = root, baseRef, declared
			}
			return err
		}}, nil
	}
}

func forgetMember(name, memberName string) func(context.Context, *Env) (outcome, error) {
	return func(context.Context, *Env) (outcome, error) {
		return outcome{record: dropMemberState(name, memberName)}, nil
	}
}

func dropMemberState(name, memberName string) func(*state.State) error {
	return func(st *state.State) error {
		ws, err := workgroupState(st, name)
		if err == nil {
			dropMember(ws, memberName)
		}
		return err
	}
}

// removeMember archives a member's unique work and removes it, outside the
// state lock; on any failure the member stays on disk and in state.
func removeMember(name, memberName string) func(context.Context, *Env) (outcome, error) {
	return func(ctx context.Context, env *Env) (outcome, error) {
		ws, err := workgroupState(env.State, name)
		if err != nil {
			return outcome{}, err
		}
		spec := resolvedSpec(env, workgroup.FromState(ws))
		for _, m := range spec.Members {
			if m.Name != memberName {
				continue
			}
			archived, err := workgroup.RemoveMember(ctx, spec, m, env.ArchiveDir, false)
			if err != nil {
				return outcome{}, err
			}
			return outcome{info: archivedInfo(archived), record: dropMemberState(name, memberName)}, nil
		}
		return outcome{}, nil
	}
}

func removeWorkgroup(name string) func(context.Context, *Env) (outcome, error) {
	return func(ctx context.Context, env *Env) (outcome, error) {
		ws, err := workgroupState(env.State, name)
		if err != nil {
			return outcome{}, err
		}
		spec := resolvedSpec(env, workgroup.FromState(ws))
		var infos []string
		var errs []error
		var removed []string
		for _, m := range spec.Members {
			archived, err := workgroup.RemoveMember(ctx, spec, m, env.ArchiveDir, false)
			if err != nil {
				errs = append(errs, fmt.Errorf("%s kept: %w", m.Name, err))
				continue
			}
			removed = append(removed, m.Name)
			if archived != "" {
				infos = append(infos, m.Name+": "+archivedInfo(archived))
			}
		}
		return outcome{
			info: strings.Join(infos, "; "),
			record: func(st *state.State) error {
				ws, err := workgroupState(st, name)
				if err != nil {
					return err
				}
				for _, m := range removed {
					dropMember(ws, m)
				}
				if len(ws.Members) == 0 {
					workgroup.RemoveEmptyRoot(spec)
					delete(st.Workgroups, name)
				}
				return nil
			},
		}, errors.Join(errs...)
	}
}

func archivedInfo(path string) string {
	if path == "" {
		return ""
	}
	return "unique work archived to " + path
}
