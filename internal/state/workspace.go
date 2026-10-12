package state

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// WorkspaceState is a workspace git-mgr created: one replica per agent, each
// holding the agent's copies of the workspace's repos. Like RepoState, each
// field's json tag mirrors its yaml tag.
type WorkspaceState struct {
	Name string `yaml:"name" json:"name"`
	// Dir is the workspace's directory, which replicas are made in; with
	// shared isolation, the folder every agent works in.
	Dir string `yaml:"dir" json:"dir"`
	// Isolation is how replicas were made: worktree, clone, reference-clone
	// or shared.
	Isolation string `yaml:"isolation" json:"isolation"`
	// BaseRef is what new agent branches start from; "" for the upstream's
	// default branch.
	BaseRef string `yaml:"base_ref,omitempty" json:"base_ref,omitempty"`
	// Declared marks a workspace created from a config declaration; removing
	// the declaration then means removing the workspace. Workspaces created
	// from flags or adopted are kept as recorded.
	Declared  bool           `yaml:"declared,omitempty" json:"declared,omitempty"`
	CreatedAt time.Time      `yaml:"created_at" json:"created_at"`
	Replicas  []ReplicaState `yaml:"replicas" json:"replicas"`
}

// ReplicaState is one agent's folder in a workspace.
type ReplicaState struct {
	Agent string `yaml:"agent" json:"agent"`
	// Path is the folder the agent works in: its only copy when the
	// workspace has one repo, the folder holding its copies when it has
	// several, the shared folder with shared isolation.
	Path   string      `yaml:"path" json:"path"`
	Copies []CopyState `yaml:"copies,omitempty" json:"copies,omitempty"`
	// Run is the agent's latest run here; nil before its first. Whether it
	// is running now is its lease's to say, not this record's.
	Run *RunState `yaml:"run,omitempty" json:"run,omitempty"`
}

// RunState is an agent's latest run in its replica.
type RunState struct {
	Profile  string    `yaml:"profile,omitempty"  json:"profile,omitempty"`
	Command  string    `yaml:"command"            json:"command"`
	PID      int       `yaml:"pid"                json:"pid"`
	Launcher string    `yaml:"launcher,omitempty" json:"launcher,omitempty"`
	Session  string    `yaml:"session,omitempty"  json:"session,omitempty"`
	Started  time.Time `yaml:"started"            json:"started"`
	// StopRequested is when `ws stop` asked it to end.
	StopRequested time.Time `yaml:"stop_requested,omitempty" json:"stop_requested,omitzero"`
	Ended         time.Time `yaml:"ended,omitempty"          json:"ended,omitzero"`
	ExitCode      int       `yaml:"exit_code"                json:"exit_code"`
}

// CopyState is one checkout of a repo in a replica.
type CopyState struct {
	// Repo is the tracked repo this is a copy of; "" for a copy of a repo
	// git-mgr doesn't track, such as one adopted with its directory.
	Repo   string `yaml:"repo,omitempty" json:"repo,omitempty"`
	Path   string `yaml:"path" json:"path"`
	Branch string `yaml:"branch,omitempty" json:"branch,omitempty"`
	// URL is the origin URL git-mgr set in a clone; worktrees share their
	// repo's.
	URL    string `yaml:"url,omitempty" json:"url,omitempty"`
	CopyID string `yaml:"copy_id,omitempty" json:"copy_id,omitempty"`
	// ManagedConfig holds the git config keys git-mgr set in a clone, as
	// RepoState.ManagedConfig does for a repo. Worktrees share their repo's.
	ManagedConfig map[string]string `yaml:"managed_config,omitempty" json:"managed_config,omitempty"`
	BackupStatus  `yaml:",inline"`
	OffsiteStatus `yaml:",inline"`
}

// Replica returns the agent's replica, or nil.
func (ws *WorkspaceState) Replica(agent string) *ReplicaState {
	for i := range ws.Replicas {
		if ws.Replicas[i].Agent == agent {
			return &ws.Replicas[i]
		}
	}
	return nil
}

// UpsertReplica returns the agent's replica, adding it at path if missing.
func (ws *WorkspaceState) UpsertReplica(agent, path string) *ReplicaState {
	if r := ws.Replica(agent); r != nil {
		return r
	}
	ws.Replicas = append(ws.Replicas, ReplicaState{Agent: agent, Path: path})
	return &ws.Replicas[len(ws.Replicas)-1]
}

// DropReplica removes the agent's replica.
func (ws *WorkspaceState) DropReplica(agent string) {
	ws.Replicas = slices.DeleteFunc(ws.Replicas, func(r ReplicaState) bool { return r.Agent == agent })
}

// CopyAt returns the replica's copy at path, or nil.
func (r *ReplicaState) CopyAt(path string) *CopyState {
	for i := range r.Copies {
		if r.Copies[i].Path == path {
			return &r.Copies[i]
		}
	}
	return nil
}

// CopyOf returns the replica's copy of the tracked repo key, or nil.
func (r *ReplicaState) CopyOf(key string) *CopyState {
	for i := range r.Copies {
		if key != "" && r.Copies[i].Repo == key {
			return &r.Copies[i]
		}
	}
	return nil
}

// UpsertCopy records c, replacing the replica's copy at the same path.
func (r *ReplicaState) UpsertCopy(c CopyState) {
	if old := r.CopyAt(c.Path); old != nil {
		*old = c
		return
	}
	r.Copies = append(r.Copies, c)
}

// DropCopy removes the replica's copy at path.
func (r *ReplicaState) DropCopy(path string) {
	r.Copies = slices.DeleteFunc(r.Copies, func(c CopyState) bool { return c.Path == path })
}

// CopyAt returns the copy at path in a workspace's replica, or nil if it is
// no longer recorded there.
func (s *State) CopyAt(workspace, agent, path string) *CopyState {
	ws := s.Workspaces[workspace]
	if ws == nil {
		return nil
	}
	r := ws.Replica(agent)
	if r == nil {
		return nil
	}
	return r.CopyAt(path)
}

// ErrHasCopies reports a tracked repo that workspace copies are made from:
// untracking or deleting it would leave them without their repo.
var ErrHasCopies = errors.New("workspace copies are made from it")

// CopiesOf names the workspace copies made from the tracked repo key, as
// workspace/agent, sorted.
func (s *State) CopiesOf(key string) []string {
	var out []string
	for name, ws := range s.Workspaces {
		for _, r := range ws.Replicas {
			for _, c := range r.Copies {
				if c.Repo == key {
					out = append(out, name+"/"+r.Agent)
				}
			}
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// CheckRemovable fails with ErrHasCopies, naming them, when workspace copies
// are made from the tracked repo key.
func (s *State) CheckRemovable(key string) error {
	if copies := s.CopiesOf(key); len(copies) > 0 {
		return fmt.Errorf("%w (%s); remove them first with git-mgr ws rm --delete, or drop the repo from their workspaces", ErrHasCopies, strings.Join(copies, ", "))
	}
	return nil
}

// RenameRepo moves the tracked repo from to key to, along with every
// workspace copy of it.
func (s *State) RenameRepo(from, to string) {
	r, ok := s.Repos[from]
	if !ok {
		return
	}
	delete(s.Repos, from)
	s.Repos[to] = r
	for _, ws := range s.Workspaces {
		for i := range ws.Replicas {
			for j := range ws.Replicas[i].Copies {
				if c := &ws.Replicas[i].Copies[j]; c.Repo == from {
					c.Repo = to
				}
			}
		}
	}
}

// FleetState is what opening a fleet left that the next open needs.
type FleetState struct {
	Name     string    `yaml:"name"     json:"name"`
	Launcher string    `yaml:"launcher" json:"launcher"`
	Opened   time.Time `yaml:"opened"   json:"opened"`
	// MuxyProjects maps each workspace to the Muxy project made for it.
	MuxyProjects map[string]string `yaml:"muxy_projects,omitempty" json:"muxy_projects,omitempty"`
}

// workgroupV2 is a workgroup as state version 2 and earlier recorded it.
type workgroupV2 struct {
	Kind      string     `yaml:"kind"`
	Root      string     `yaml:"root"`
	Source    string     `yaml:"source"`
	BaseRef   string     `yaml:"base_ref"`
	Members   []memberV2 `yaml:"members"`
	Declared  bool       `yaml:"declared"`
	CreatedAt time.Time  `yaml:"created_at"`
}

type memberV2 struct {
	Name          string            `yaml:"name"`
	Path          string            `yaml:"path"`
	Branch        string            `yaml:"branch"`
	RepoURL       string            `yaml:"repo_url"`
	RepoKey       string            `yaml:"repo_key"`
	CopyID        string            `yaml:"copy_id"`
	ManagedConfig map[string]string `yaml:"managed_config"`
	BackupStatus  `yaml:",inline"`
	OffsiteStatus `yaml:",inline"`
}

// workspaceFromV2 turns a version 2 workgroup into a workspace: each member
// becomes an agent's replica holding one copy, of the tracked repo the member
// names or, for a worktree group, the tracked repo at its source.
func (s *State) workspaceFromV2(name string, wg workgroupV2) *WorkspaceState {
	ws := &WorkspaceState{
		Name: name, Dir: wg.Root, Isolation: "worktree", BaseRef: wg.BaseRef,
		Declared: wg.Declared, CreatedAt: wg.CreatedAt,
	}
	source := ""
	if wg.Kind == "clone" {
		ws.Isolation = "clone"
	} else {
		for key, r := range s.Repos {
			if wg.Source != "" && r.LocalPath == wg.Source {
				source = key
			}
		}
	}
	for _, m := range wg.Members {
		c := CopyState{
			Repo: m.RepoKey, Path: m.Path, Branch: m.Branch, CopyID: m.CopyID,
			ManagedConfig: m.ManagedConfig, BackupStatus: m.BackupStatus, OffsiteStatus: m.OffsiteStatus,
		}
		if wg.Kind == "clone" {
			c.URL = m.RepoURL
		} else if c.Repo == "" {
			c.Repo = source
		}
		ws.Replicas = append(ws.Replicas, ReplicaState{Agent: m.Name, Path: m.Path, Copies: []CopyState{c}})
	}
	return ws
}
