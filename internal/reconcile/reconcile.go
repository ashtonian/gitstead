// Package reconcile brings tracked repos on disk in line with config, and
// runs plans: the workspace subsystem plans its own actions and applies them
// with the same engine.
//
// Every run compares three views of each object:
//
//   - desired: derived from config and this run's discovery (see Repos);
//   - applied: what gitstead last set, kept in state (path, origin URL, tags,
//     managed git config);
//   - actual: what is on disk now (see Observe).
//
// Desired against applied gives the actions of a Plan. Actual against applied
// gives drift: a value gitstead set was changed by hand, which is reported and
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

	"github.com/ashtonian/gitstead/internal/hook"
	"github.com/ashtonian/gitstead/internal/state"
)

// Kind names what an action does.
type Kind string

const (
	KindClone  Kind = "clone"  // clone a repo, or re-clone one gone from disk
	KindAdopt  Kind = "adopt"  // track a clone already at the desired path
	KindRename Kind = "rename" // the provider renamed or transferred the repo
	KindURL    Kind = "url"    // change the origin URL
	KindConfig Kind = "config" // set or unset a managed git config key
	KindTags   Kind = "tags"   // change tags or discovering rule (state only)
	KindMove   Kind = "move"   // move a repo to its desired path
)

// Action is one change a plan makes.
type Action struct {
	Kind   Kind   `json:"kind"`
	Target string `json:"target"`          // repo key, or what the planner names: workspace/agent
	Field  string `json:"field,omitempty"` // the git config key, for KindConfig
	From   string `json:"from,omitempty"`
	To     string `json:"to,omitempty"`
	Note   string `json:"note,omitempty"`
	// Disruptive actions move or delete directories that a shell, editor or
	// agent may be using; they wait for an explicit `gitstead apply`.
	Disruptive bool `json:"disruptive"`
	// Serial actions run one at a time, in plan order, after all the others.
	// Workspace actions are serial: their copies share records, and the
	// repos they are made from must be in place first.
	Serial bool `json:"-"`

	// Run makes the change on disk and returns what to record of it.
	Run RunFunc `json:"-"`
}

// RunFunc makes an action's change on disk, outside the state lock, and
// returns what to record of it.
type RunFunc func(ctx context.Context, env *Env) (Outcome, error)

// Outcome is what an action did, and what of it to record.
type Outcome struct {
	// Info reports anything worth showing, such as where removed work was
	// archived.
	Info string
	// Record saves the change in state; nil when there is nothing to save. It
	// runs even when the action failed, for the part that was done. A record
	// that fails must leave the state as it found it, since it shares its
	// write with other actions' records.
	Record func(st *state.State) error
	// After, if set, runs once the record is saved; its info is added to the
	// action's.
	After func(ctx context.Context) (string, error)
	// Release, if set, runs last, whatever happened: it lets go of what Run
	// took for the action's whole course, such as a repo's lock.
	Release func()
}

// Drift is a value gitstead set that was changed by hand.
type Drift struct {
	Target   string `json:"target"`
	What     string `json:"what"`
	Recorded string `json:"recorded"` // what gitstead set
	Found    string `json:"found"`    // what is there now
	Wanted   string `json:"wanted"`   // what config wants
}

// Blocked is a change config asks for that gitstead will not make.
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
	record []Observation
}

// Observation is what a run observed of something tracked: it does what disk
// work recording it needs -- creating a missing copy ID -- before the state
// lock is taken, and returns the change that records it, applied under the
// lock; nil records nothing.
type Observation func(ctx context.Context) func(*state.State)

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

// Split separates the plan's serial actions from the others. The parallel
// part, which applies first, keeps what this run observed.
func (p *Plan) Split() (parallel, serial *Plan) {
	parallel, serial = &Plan{record: p.record}, &Plan{}
	for _, a := range p.Actions {
		if a.Serial {
			serial.Actions = append(serial.Actions, a)
		} else {
			parallel.Actions = append(parallel.Actions, a)
		}
	}
	return parallel, serial
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
	// State is the state Update keeps current. Serial actions read it; repo
	// actions run at once and read only what their plan gives them.
	State *state.State
	// Update applies a change to the latest state and saves it, under the
	// state lock, so other gitstead runs can work between actions: an action
	// does its slow disk work (a clone, an archive) first and only then
	// records the result. Required.
	Update func(change func(*state.State) error) error
	// Concurrency is how many repos' actions do disk work at once; below 1
	// means one.
	Concurrency int
	// CloneTimeout bounds each clone; 0 means none.
	CloneTimeout time.Duration
	// LockWait is how long an action waits for a repo another gitstead
	// command is using (see gitops.LockRepo).
	LockWait time.Duration
	// InspectTimeout bounds each look at a working copy; 0 means none.
	InspectTimeout time.Duration
	// Hooks runs the hooks of checkouts the plan makes.
	Hooks hook.Runner
	// ArchiveDir receives the unique work of anything removed.
	ArchiveDir string
	// StateDir is the state file's directory, where agents' runs keep their
	// leases.
	StateDir string
	// KeepDirs are directories never removed when a move leaves its old
	// parents empty, such as base_dir and the workspace root.
	KeepDirs []string
	Now      func() time.Time
}

// Time is the current time, from Now when set.
func (env *Env) Time() time.Time {
	if env.Now != nil {
		return env.Now()
	}
	return time.Now()
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

// RemoveEmptyParents removes the now-empty directories above path, stopping
// at the first one that isn't empty or that env keeps.
func RemoveEmptyParents(env *Env, path string) {
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
