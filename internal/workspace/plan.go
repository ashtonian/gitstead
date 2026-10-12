package workspace

import (
	"context"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ashtonian/git-mgr/internal/config"
	"github.com/ashtonian/git-mgr/internal/gitops"
	"github.com/ashtonian/git-mgr/internal/reconcile"
	"github.com/ashtonian/git-mgr/internal/repourl"
	"github.com/ashtonian/git-mgr/internal/state"
)

// Kinds of workspace actions.
const (
	KindCreateWorkspace reconcile.Kind = "create-workspace" // record a declared workspace
	KindWorkspace       reconcile.Kind = "workspace"        // update a workspace's recorded settings
	KindCreateCopy      reconcile.Kind = "create-copy"      // make, or make again, an agent's copy of a repo
	KindCopy            reconcile.Kind = "copy"             // record which repo a copy is of
	KindMoveCopy        reconcile.Kind = "move-copy"        // move a copy to its desired path
	KindRemoveCopy      reconcile.Kind = "remove-copy"      // archive and remove a copy no longer wanted
	KindForgetCopy      reconcile.Kind = "forget-copy"      // drop a copy that is already gone from disk
	KindRemoveWorkspace reconcile.Kind = "remove-workspace" // archive and remove a workspace no longer declared
	KindCopyURL         reconcile.Kind = "copy-url"         // change a clone's origin URL
	KindCopyConfig      reconcile.Kind = "copy-config"      // set or unset a clone's managed git config key
	KindCopySource      reconcile.Kind = "copy-source"      // point a clone at its repo's moved clone
)

// Scope selects what Compute plans.
type Scope struct {
	// Repos plans every tracked repo too, from Discovery.
	Repos     bool
	Discovery reconcile.Scope
	// Only limits the workspaces planned; nil plans all of them.
	Only func(name string) bool
	// Extra are workspaces given on the command line, planned as declared
	// ones but recorded as made from flags.
	Extra []config.Workspace
}

func (s Scope) covers(name string) bool { return s.Only == nil || s.Only(name) }

// Compute plans what config asks of workspaces and, with scope.Repos, of
// tracked repos. Repos a workspace names by path or URL that aren't tracked
// yet are tracked first, adopted where they are or cloned. Workspace actions
// are serial, after the repo actions; see Apply.
func Compute(ctx context.Context, cfg *config.Config, st *state.State, scope Scope) *reconcile.Plan {
	desired := map[string]reconcile.Repo{}
	var blocked []reconcile.Blocked
	if scope.Repos {
		desired, blocked = reconcile.Repos(cfg, st, scope.Discovery.Discovered, scope.Discovery.Rules)
	}
	decls := declarations(cfg, scope)
	blocked = append(blocked, trackReferenced(ctx, cfg, st, desired, decls)...)
	projected := reconcile.Projected(st, desired)
	specs, b := Desired(cfg, projected, decls, scope.covers)
	blocked = append(blocked, b...)

	paths := append(reconcile.RepoPaths(desired, st), Paths(specs, projected)...)
	seen := reconcile.Observe(ctx, paths, cfg.Concurrency)
	overwrite := cfg.Reconcile.Drift == "overwrite"
	p := reconcile.PlanRepos(desired, st, seen, overwrite)
	p.Merge(Plan(specs, projected, seen, scope.covers, overwrite))
	p.Blocked = append(p.Blocked, blocked...)
	return p
}

// Apply applies p, a plan Compute made for scope: its repo actions first,
// then, because workspaces are made from repos, a workspace plan made afresh
// against the updated state. A disruptive workspace action of the fresh plan
// runs only if p had it too, so nothing disruptive runs that wasn't shown.
func Apply(ctx context.Context, cfg *config.Config, env *reconcile.Env, p *reconcile.Plan, scope Scope, include func(reconcile.Action) bool) []reconcile.Result {
	repos, serial := p.Split()
	shown := map[string]bool{}
	for _, a := range serial.Actions {
		if a.Disruptive {
			shown[a.Identity()] = true
		}
	}
	results := repos.Apply(ctx, env, include)
	_, fresh := Compute(ctx, cfg, env.State, Scope{Only: scope.Only, Extra: scope.Extra}).Split()
	return append(results, fresh.Apply(ctx, env, func(a reconcile.Action) bool {
		return include(a) && (!a.Disruptive || shown[a.Identity()])
	})...)
}

// declaration is a workspace config or the command line declares.
type declaration struct {
	config.Workspace
	declared bool
}

func declarations(cfg *config.Config, scope Scope) []declaration {
	var out []declaration
	for _, w := range cfg.Workspaces {
		if scope.covers(w.Name) {
			out = append(out, declaration{w, true})
		}
	}
	for _, w := range scope.Extra {
		out = append(out, declaration{w, false})
	}
	return out
}

// trackReferenced adds to desired each repo the declarations name by path or
// URL that isn't tracked: adopted at its path, or cloned where config puts
// it.
func trackReferenced(ctx context.Context, cfg *config.Config, st *state.State, desired map[string]reconcile.Repo, decls []declaration) []reconcile.Blocked {
	var blocked []reconcile.Blocked
	paths := map[string]string{} // untracked path -> the workspace naming it
	for _, d := range decls {
		for _, entry := range d.Repos {
			switch ref := ParseRef(entry); ref.Kind {
			case RefPath:
				if trackedAt(st, ref.Path) == "" {
					paths[ref.Path] = d.Name
				}
			case RefURL:
				if st.Repos[ref.Key] != nil {
					continue
				}
				if _, ok := desired[ref.Key]; ok {
					continue
				}
				path, err := reconcile.TemplatePath(cfg, ref.Key, ref.ID.Host, ref.ID.Owner, ref.ID.Name, ref.ID.FullName)
				if err != nil {
					blocked = append(blocked, reconcile.Blocked{Target: d.Name, Reason: err.Error()})
					continue
				}
				desired[ref.Key] = reconcile.HandAdded(cfg, ref.ID, path, nil)
			}
		}
	}
	seen := reconcile.Observe(ctx, slices.Collect(maps.Keys(paths)), cfg.Concurrency)
	for _, path := range slices.Sorted(maps.Keys(paths)) {
		c := seen.At(path)
		if !c.IsRepo || c.Err != nil {
			blocked = append(blocked, reconcile.Blocked{Target: paths[path], Reason: path + " is not a readable git repository"})
			continue
		}
		id, err := repourl.Parse(c.Origin)
		if err != nil {
			blocked = append(blocked, reconcile.Blocked{Target: paths[path], Reason: path + " has no origin remote git-mgr can track it by"})
			continue
		}
		key := id.Key()
		if r := st.Repos[key]; r != nil {
			blocked = append(blocked, reconcile.Blocked{Target: paths[path], Reason: fmt.Sprintf("%s is a clone of %s, which git-mgr tracks at %s", path, key, r.LocalPath)})
			continue
		}
		if d, ok := desired[key]; ok && d.Path != path {
			blocked = append(blocked, reconcile.Blocked{Target: paths[path], Reason: fmt.Sprintf("%s is a clone of %s, which belongs at %s", path, key, d.Path)})
			continue
		}
		desired[key] = reconcile.HandAdded(cfg, id, path, nil)
	}
	return blocked
}

// Desired returns what is wanted of each workspace covers accepts: those
// decls declare, and those recorded without a declaration, kept as they
// are. A workspace whose declaration can't be resolved maps to nil, which
// the plan leaves alone; a recorded workspace that was declared and no
// longer is is absent, which the plan removes.
func Desired(cfg *config.Config, st *state.State, decls []declaration, covers func(string) bool) (map[string]*Spec, []reconcile.Blocked) {
	out := map[string]*Spec{}
	var blocked []reconcile.Blocked
	for _, d := range decls {
		spec, err := Resolve(cfg, st, d.Workspace)
		if err != nil {
			blocked = append(blocked, reconcile.Blocked{Target: d.Name, Reason: err.Error()})
			out[d.Name] = nil
			continue
		}
		spec.Declared = d.declared
		out[d.Name] = spec
	}
	for name, ws := range st.Workspaces {
		if _, ok := out[name]; ok || ws.Declared || !covers(name) {
			continue
		}
		out[name] = FromState(cfg, st, ws)
	}
	return out, blocked
}

// Paths lists what a workspace plan needs observed: copies wanted and
// recorded, and the clones they are made from.
func Paths(specs map[string]*Spec, st *state.State) []string {
	var paths []string
	for _, spec := range specs {
		if spec == nil {
			continue
		}
		for _, r := range spec.Replicas {
			for _, c := range r.Copies {
				paths = append(paths, c.Path, c.Source)
			}
		}
	}
	for _, ws := range st.Workspaces {
		for _, r := range ws.Replicas {
			for _, c := range r.Copies {
				paths = append(paths, c.Path)
			}
		}
	}
	return paths
}

// Plan compares what specs want of each workspace covers accepts with what
// st records and seen found on disk.
func Plan(specs map[string]*Spec, st *state.State, seen reconcile.Seen, covers func(string) bool, overwrite bool) *reconcile.Plan {
	p := &reconcile.Plan{}
	names := map[string]bool{}
	for name := range specs {
		names[name] = true
	}
	for name := range st.Workspaces {
		if covers(name) {
			names[name] = true
		}
	}
	for _, name := range slices.Sorted(maps.Keys(names)) {
		spec, wanted := specs[name]
		ws := st.Workspaces[name]
		pl := &planner{p: p, seen: seen, overwrite: overwrite}
		switch {
		case wanted && spec == nil:
			// Its declaration is blocked; leave what exists alone.
		case !wanted:
			n := 0
			for _, r := range ws.Replicas {
				n += len(r.Copies)
			}
			pl.add(reconcile.Action{Kind: KindRemoveWorkspace, Target: name, From: ws.Dir, Disruptive: true,
				Note: fmt.Sprintf("no longer declared; %d copies archived and removed", n), Run: removeWorkspace(name)})
		case ws == nil:
			pl.add(reconcile.Action{Kind: KindCreateWorkspace, Target: name, To: spec.Dir,
				Note: fmt.Sprintf("%s, %d agent(s)", spec.Isolation, len(spec.Replicas)), Run: createWorkspace(spec)})
			for _, r := range spec.Replicas {
				for _, c := range r.Copies {
					pl.copy(spec, r, c, nil)
				}
			}
		default:
			pl.workspace(spec, ws)
		}
	}
	return p
}

type planner struct {
	p         *reconcile.Plan
	seen      reconcile.Seen
	overwrite bool
}

func (pl *planner) add(a reconcile.Action) {
	a.Serial = true
	pl.p.Actions = append(pl.p.Actions, a)
}

func (pl *planner) block(target, reason string) {
	pl.p.Blocked = append(pl.p.Blocked, reconcile.Blocked{Target: target, Reason: reason})
}

func (pl *planner) workspace(spec *Spec, ws *state.WorkspaceState) {
	if ws.Isolation != spec.Isolation {
		pl.block(spec.Name, fmt.Sprintf("isolation changed from %s to %s; remove it (git-mgr ws rm %s --delete) to make it again", ws.Isolation, spec.Isolation, spec.Name))
		return
	}
	if changed := settingsChanged(ws, spec); len(changed) > 0 {
		a := reconcile.Action{Kind: KindWorkspace, Target: spec.Name, Note: strings.Join(changed, ", "), Run: updateWorkspace(spec)}
		if ws.Dir != spec.Dir {
			a.From, a.To = ws.Dir, spec.Dir
		}
		pl.add(a)
	}
	if spec.Shared() {
		return
	}

	matched := map[string]bool{}
	for _, r := range spec.Replicas {
		rr := ws.Replica(r.Agent)
		for _, c := range r.Copies {
			var rec *state.CopyState
			if rr != nil {
				if rec = rr.CopyOf(c.Repo); rec == nil {
					if at := rr.CopyAt(c.Path); at != nil && at.Repo == "" {
						rec = at
					}
				}
			}
			if rec != nil {
				matched[rr.Agent+"\x00"+rec.Path] = true
			}
			pl.copy(spec, r, c, rec)
		}
	}
	for _, rr := range ws.Replicas {
		for _, rc := range rr.Copies {
			if matched[rr.Agent+"\x00"+rc.Path] {
				continue
			}
			target := copyTarget(spec.Name, rr.Agent, rr.Path, rc.Path)
			if !pl.seen.At(rc.Path).Exists {
				pl.add(reconcile.Action{Kind: KindForgetCopy, Target: target, From: rc.Path,
					Note: "no longer wanted and gone from disk", Run: forgetCopy(spec.Name, rr.Agent, rc.Path)})
				continue
			}
			pl.add(reconcile.Action{Kind: KindRemoveCopy, Target: target, From: rc.Path, Disruptive: true,
				Note: "no longer wanted; unique work archived first", Run: removeCopy(spec.Name, rr.Agent, rc.Path)})
		}
	}
}

// copy plans copy c of replica r, recorded as rec (nil when it isn't).
func (pl *planner) copy(spec *Spec, r Replica, c Copy, rec *state.CopyState) {
	target := copyTarget(spec.Name, r.Agent, r.Path, c.Path)
	here := pl.seen.At(c.Path)
	create := func(note, replaces string) {
		pl.add(reconcile.Action{Kind: KindCreateCopy, Target: target, To: c.Path, Note: note, Run: createCopy(spec, r, c, replaces)})
	}
	switch {
	case rec == nil && !here.Exists:
		create("", "")
	case rec == nil && pl.isCopy(spec, here, c, ""):
		pl.add(reconcile.Action{Kind: KindCreateCopy, Target: target, To: c.Path, Note: "already there; recording it", Run: recordCopy(spec, r, c)})
	case rec == nil:
		pl.block(target, reconcile.Occupied(c.Path, here))
	case rec.Path != c.Path:
		old := pl.seen.At(rec.Path)
		switch {
		case within(rec.Path, c.Path) || within(c.Path, rec.Path):
			pl.block(target, fmt.Sprintf("its copy would move from %s into %s; remove the copy (git-mgr ws rm) to make it again", rec.Path, c.Path))
		case !old.Exists && pl.isCopy(spec, here, c, rec.CopyID):
			pl.add(reconcile.Action{Kind: KindMoveCopy, Target: target, From: rec.Path, To: c.Path, Note: "already moved; recording it",
				Run: recordCopyMove(spec.Name, r, rec.Path, c.Path)})
		case here.Exists:
			pl.block(target, "cannot move from "+rec.Path+": "+reconcile.Occupied(c.Path, here))
		case !old.Exists:
			create("gone from disk; creating it again", rec.Path)
		default:
			pl.add(reconcile.Action{Kind: KindMoveCopy, Target: target, From: rec.Path, To: c.Path, Disruptive: true,
				Run: moveCopy(spec, r, *rec, c)})
		}
	case !here.Exists:
		create("gone from disk; creating it again", "")
	case !here.IsRepo || here.Err != nil:
		pl.block(target, c.Path+" is not a readable git repository")
	default:
		pl.existing(spec, r, c, rec, here, target)
	}
}

// existing plans a recorded copy that is where it should be.
func (pl *planner) existing(spec *Spec, r Replica, c Copy, rec *state.CopyState, here reconcile.Copy, target string) {
	if rec.Repo == "" && c.Repo != "" {
		if !pl.isCopy(spec, here, c, "") {
			pl.block(target, fmt.Sprintf("%s isn't a copy of %s", c.Path, c.Repo))
			return
		}
		pl.add(reconcile.Action{Kind: KindCopy, Target: target, To: c.Repo, Note: "recording its repo",
			Run: recordCopyRepo(spec.Name, r.Agent, c.Path, c.Repo)})
	}
	if spec.Isolation == config.IsolationWorktree {
		return
	}
	pl.copyURL(spec.Name, r.Agent, target, rec, c, here)
	if src := here.Config["remote."+gitops.SourceRemote+".url"]; c.Source != "" && src != "" && src != c.Source {
		pl.add(reconcile.Action{Kind: KindCopySource, Target: target, From: src, To: c.Source, Run: repointCopy(c.Path, src, c.Source)})
	}
	name, agent, path := spec.Name, r.Agent, c.Path
	pl.p.Actions = append(pl.p.Actions, reconcile.PlanConfig(pl.p, KindCopyConfig, target, c.Config, rec.ManagedConfig, here.Config, pl.overwrite, true,
		func(k, v string, unset, recordOnly bool) reconcile.RunFunc {
			return setCopyConfig(name, agent, path, k, v, unset, recordOnly)
		})...)
}

func (pl *planner) copyURL(name, agent, target string, rec *state.CopyState, c Copy, here reconcile.Copy) {
	want, recorded, have := c.URL, rec.URL, here.Origin
	set := func(from, note string) {
		pl.add(reconcile.Action{Kind: KindCopyURL, Target: target, From: from, To: want, Note: note, Run: setCopyURL(name, agent, c.Path, want)})
	}
	switch {
	case want == "" || have == want && recorded == want:
	case have == want:
		set(recorded, "already set; recording it")
	case have == recorded || recorded == "":
		set(have, "")
	case pl.overwrite:
		set(have, "changed by hand; restoring")
	default:
		pl.p.Drift = append(pl.p.Drift, reconcile.Drift{Target: target, What: "origin URL", Recorded: recorded, Found: reconcile.OrUnset(have), Wanted: want})
	}
}

// isCopy reports whether what is on disk at c's path is that copy: by copy
// ID when both have one; else a worktree must be one of its repo's clone,
// and a clone must have been made from that clone or be a clone of the repo.
func (pl *planner) isCopy(spec *Spec, here reconcile.Copy, c Copy, copyID string) bool {
	if !here.IsRepo || here.Err != nil {
		return false
	}
	if copyID != "" && here.CopyID != "" {
		return here.CopyID == copyID
	}
	if c.Source == "" {
		return false
	}
	src := pl.seen.At(c.Source)
	linked := src.IsRepo && here.CommonDir != "" && realPath(here.CommonDir) == realPath(src.CommonDir)
	if spec.Isolation == config.IsolationWorktree {
		return linked
	}
	if linked {
		return false
	}
	if here.Config["remote."+gitops.SourceRemote+".url"] == c.Source {
		return true
	}
	return here.Is(c.Repo, "")
}

// settingsChanged describes how the recorded workspace's own settings differ
// from spec's.
func settingsChanged(ws *state.WorkspaceState, spec *Spec) []string {
	var changed []string
	if ws.Dir != spec.Dir {
		changed = append(changed, "new directory")
	}
	if ws.BaseRef != spec.BaseRef {
		changed = append(changed, fmt.Sprintf("base ref %s -> %s", reconcile.OrUnset(ws.BaseRef), reconcile.OrUnset(spec.BaseRef)))
	}
	switch {
	case !ws.Declared && spec.Declared:
		changed = append(changed, "now declared in config")
	case ws.Declared && !spec.Declared:
		changed = append(changed, "no longer declared in config")
	}
	if spec.Shared() && !sameReplicas(ws, spec) {
		changed = append(changed, fmt.Sprintf("%d agent(s)", len(spec.Replicas)))
	}
	return changed
}

func sameReplicas(ws *state.WorkspaceState, spec *Spec) bool {
	if len(ws.Replicas) != len(spec.Replicas) {
		return false
	}
	for i, r := range spec.Replicas {
		if ws.Replicas[i].Agent != r.Agent || ws.Replicas[i].Path != r.Path {
			return false
		}
	}
	return true
}

// copyTarget names a copy in a plan: workspace/agent, plus the copy's
// folder when its replica holds several.
func copyTarget(workspace, agent, replicaPath, copyPath string) string {
	target := workspace + "/" + agent
	if copyPath != replicaPath {
		if rel, err := filepath.Rel(replicaPath, copyPath); err == nil && !strings.HasPrefix(rel, "..") {
			return target + "/" + filepath.ToSlash(rel)
		}
	}
	return target
}

// within reports whether path is inside dir.
func within(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != "." && !strings.HasPrefix(rel, "..")
}
