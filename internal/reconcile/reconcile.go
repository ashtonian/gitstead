// Package reconcile brings repos and workgroups on disk in line with config.
//
// Every run compares three views of each object:
//
//   - desired: derived from config and this run's discovery (see Repos,
//     Workgroups);
//   - applied: what git-mgr last set, kept in state (path, origin URL, tags,
//     managed git config, workgroup members);
//   - actual: what is on disk now (see Observe).
//
// Desired against applied gives the actions of a Plan. Actual against applied
// gives drift: a value git-mgr set was changed by hand, which is reported and
// left alone unless drift is set to overwrite.
//
// Every action checks the disk before acting, so an interrupted apply is
// resumed by planning again: a clone whose directory already holds the repo is
// adopted, a move whose destination already holds the copy is recorded.
package reconcile

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/ashtonian/git-mgr/internal/state"
)

// Kind names what an action does.
type Kind string

const (
	KindClone           Kind = "clone"            // clone a repo, or re-clone one gone from disk
	KindAdopt           Kind = "adopt"            // track a clone already at the desired path
	KindRename          Kind = "rename"           // the provider renamed or transferred the repo
	KindURL             Kind = "url"              // change the origin URL
	KindConfig          Kind = "config"           // set or unset a managed git config key
	KindTags            Kind = "tags"             // change tags or discovering rule (state only)
	KindMove            Kind = "move"             // move a repo to its desired path
	KindCreateWorkgroup Kind = "create-workgroup" // materialise a declared workgroup
	KindWorkgroup       Kind = "workgroup"        // update a workgroup's recorded settings
	KindCreateMember    Kind = "create-member"    // create, or re-create, a workgroup member
	KindMoveMember      Kind = "move-member"      // move a member to its desired path
	KindRemoveMember    Kind = "remove-member"    // archive and remove a member no longer declared
	KindForgetMember    Kind = "forget-member"    // drop a member that is already gone from disk
	KindRemoveWorkgroup Kind = "remove-workgroup" // archive and remove a workgroup no longer declared
	KindMemberURL       Kind = "member-url"       // change a clone member's origin URL
	KindMemberConfig    Kind = "member-config"    // set or unset a clone member's managed git config key
)

// Action is one change a plan makes.
type Action struct {
	Kind   Kind   `json:"kind"`
	Target string `json:"target"`          // repo key, workgroup, or workgroup/member
	Field  string `json:"field,omitempty"` // the git config key, for KindConfig
	From   string `json:"from,omitempty"`
	To     string `json:"to,omitempty"`
	Note   string `json:"note,omitempty"`
	// Disruptive actions move or delete directories that a shell, editor or
	// agent may be using; they wait for an explicit `git-mgr apply`.
	Disruptive bool `json:"disruptive"`

	// run makes the change and records it in env.State. Its string reports
	// anything worth showing, such as where removed work was archived.
	run func(ctx context.Context, env *Env) (string, error)
}

// Workgroup reports whether the action changes a workgroup rather than a
// tracked repo. Only `git-mgr apply` and `wg ensure` make workgroup changes.
func (a Action) Workgroup() bool {
	switch a.Kind {
	case KindCreateWorkgroup, KindWorkgroup, KindCreateMember, KindMoveMember,
		KindRemoveMember, KindForgetMember, KindRemoveWorkgroup, KindMemberURL, KindMemberConfig:
		return true
	}
	return false
}

// Drift is a value git-mgr set that was changed by hand.
type Drift struct {
	Target   string `json:"target"`
	What     string `json:"what"`
	Recorded string `json:"recorded"` // what git-mgr set
	Found    string `json:"found"`    // what is there now
	Wanted   string `json:"wanted"`   // what config wants
}

// Blocked is a change config asks for that git-mgr will not make.
type Blocked struct {
	Target string `json:"target"`
	Reason string `json:"reason"`
}

// Plan is the set of changes that bring disk and state in line with config.
type Plan struct {
	Actions []Action  `json:"actions"`
	Drift   []Drift   `json:"drift"`
	Blocked []Blocked `json:"blocked"`

	// record updates state with what this run observed (copy IDs, provider
	// IDs, discovery metadata). It changes nothing on disk beyond creating
	// missing copy IDs, so it runs whenever a plan is applied.
	record []func(ctx context.Context, env *Env)
}

// MarshalJSON writes empty lists as [] rather than null.
func (p *Plan) MarshalJSON() ([]byte, error) {
	type plan Plan
	out := plan(*p)
	if out.Actions == nil {
		out.Actions = []Action{}
	}
	if out.Drift == nil {
		out.Drift = []Drift{}
	}
	if out.Blocked == nil {
		out.Blocked = []Blocked{}
	}
	return json.Marshal(out)
}

// Merge appends other's contents to p.
func (p *Plan) Merge(other *Plan) {
	p.Actions = append(p.Actions, other.Actions...)
	p.Drift = append(p.Drift, other.Drift...)
	p.Blocked = append(p.Blocked, other.Blocked...)
	p.record = append(p.record, other.record...)
}

// Empty reports whether the plan changes nothing.
func (p *Plan) Empty() bool {
	return len(p.Actions) == 0 && len(p.Drift) == 0 && len(p.Blocked) == 0
}

// Disruptive counts the plan's disruptive actions.
func (p *Plan) Disruptive() int {
	n := 0
	for _, a := range p.Actions {
		if a.Disruptive {
			n++
		}
	}
	return n
}

// Env is what applying a plan acts on.
type Env struct {
	State *state.State
	// Save persists State. Apply calls it after every action, so an
	// interruption loses at most the action in progress.
	Save func() error
	// ArchiveDir receives the unique work of anything removed.
	ArchiveDir string
	// KeepDirs are directories never removed when a move leaves its old
	// parents empty, such as base_dir and the home directory.
	KeepDirs []string
	Now      func() time.Time

	// moved maps paths moved during this run to their new location, so later
	// actions find a workgroup's source after its repo moved.
	moved map[string]string
}

func (env *Env) now() time.Time {
	if env.Now != nil {
		return env.Now()
	}
	return time.Now()
}

// resolve returns where path is now, following moves made during this run.
func (env *Env) resolve(path string) string {
	if to, ok := env.moved[path]; ok {
		return to
	}
	return path
}

func (env *Env) noteMove(from, to string) {
	if env.moved == nil {
		env.moved = map[string]string{}
	}
	env.moved[from] = to
}

// ErrSkipped marks an action not run because an earlier action on the same
// target failed.
var ErrSkipped = errors.New("skipped: an earlier change to the same target failed")

// Result is the outcome of one action.
type Result struct {
	Action Action
	Info   string
	Err    error
}

// Safe selects the actions that move and delete nothing.
func Safe(a Action) bool { return !a.Disruptive }

// All selects every action.
func All(Action) bool { return true }

// None selects no action.
func None(Action) bool { return false }

// Apply records this run's observations, then runs the actions include
// selects, in order, saving state after each. An action whose target already
// failed in this run is skipped, so a failed rename never leads to a move.
func (p *Plan) Apply(ctx context.Context, env *Env, include func(Action) bool) []Result {
	for _, rec := range p.record {
		rec(ctx, env)
	}
	if len(p.record) > 0 && env.Save != nil {
		if err := env.Save(); err != nil {
			return []Result{{Err: err}}
		}
	}

	var results []Result
	failed := map[string]bool{}
	for _, a := range p.Actions {
		if !include(a) {
			continue
		}
		if failed[a.Target] {
			results = append(results, Result{Action: a, Err: ErrSkipped})
			continue
		}
		info, err := a.run(ctx, env)
		// Save even after a failure: an action records in state only what it
		// actually changed, and a move that succeeded before a later step
		// failed must not be forgotten.
		if env.Save != nil {
			if serr := env.Save(); serr != nil && err == nil {
				err = serr
			}
		}
		if err != nil {
			failed[a.Target] = true
		}
		results = append(results, Result{Action: a, Info: info, Err: err})
	}
	return results
}

// removeEmptyParents removes the now-empty directories above path, stopping
// at the first one that isn't empty or that env keeps.
func removeEmptyParents(env *Env, path string) {
	keep := map[string]bool{"/": true}
	for _, d := range env.KeepDirs {
		keep[filepath.Clean(d)] = true
	}
	if home, err := os.UserHomeDir(); err == nil {
		keep[filepath.Clean(home)] = true
	}
	for dir := filepath.Dir(path); !keep[dir] && dir != filepath.Dir(dir); dir = filepath.Dir(dir) {
		if os.Remove(dir) != nil {
			return
		}
	}
}
