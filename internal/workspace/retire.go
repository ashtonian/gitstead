package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"time"

	agents "github.com/ashtonian/gitstead/internal/agent"
	"github.com/ashtonian/gitstead/internal/ctxutil"
	"github.com/ashtonian/gitstead/internal/gitops"
	"github.com/ashtonian/gitstead/internal/state"
)

// RetireOptions shape a retirement.
type RetireOptions struct {
	StateDir   string // where agents' leases are
	ArchiveDir string
	// Stop ends a running agent's run first, waiting up to StopTimeout;
	// without it, a running agent's copies are kept.
	Stop        bool
	StopTimeout time.Duration
	// Discard deletes unique work instead of archiving it.
	Discard bool
	// Timeout bounds archiving and removing each copy; 0 means none.
	Timeout time.Duration
}

// Retired is what retiring copies did, and what to record of it.
type Retired struct {
	Removed    []Located
	Kept       []KeptCopy
	Tombstones []state.Tombstone
	Archives   []state.ArchiveEntry
}

// KeptCopy is a copy a retirement left, and why.
type KeptCopy struct {
	Located
	Err error
}

// Paths lists the paths of the removed copies.
func (r Retired) Paths() []string {
	var out []string
	for _, l := range r.Removed {
		out = append(out, l.Path)
	}
	return out
}

// Retire retires the agent's copies in a workspace: those at paths, or all
// of them. It holds the agent's lease throughout, so no run starts meanwhile
// -- a running agent is stopped first with opts.Stop, else its copies are
// kept -- and then, for each copy, inspects it afresh, archives and verifies
// its unique work (submodules' and LFS objects included), and deletes it.
// It returns a tombstone for each copy removed and the catalog entry of each
// archive, for the caller to record with the copies dropped. A copy that
// can't be inspected, archived or deleted is kept.
func Retire(ctx context.Context, spec *Spec, ws *state.WorkspaceState, agent string, paths []string, opts RetireOptions) Retired {
	var out Retired
	var targets []Located
	for _, l := range spec.Copies() {
		if l.Agent == agent && (paths == nil || slices.Contains(paths, l.Path)) {
			targets = append(targets, l)
		}
	}
	if len(targets) == 0 {
		return out
	}
	lease, err := takeLease(opts, spec.Name, agent)
	if err != nil {
		for _, l := range targets {
			out.Kept = append(out.Kept, KeptCopy{l, err})
		}
		return out
	}
	defer lease.Release()

	now := time.Now()
	for _, l := range targets {
		rctx, cancel := ctxutil.WithTimeout(ctx, opts.Timeout)
		archived, err := RemoveCopy(rctx, spec.Isolation, l.Copy, opts.ArchiveDir, ArchiveName(l), opts.Discard)
		err = ctxutil.TimedOut(rctx, err, "archiving and removing "+l.Path, opts.Timeout, "sync.clone_timeout")
		cancel()
		if err != nil {
			out.Kept = append(out.Kept, KeptCopy{l, err})
			continue
		}
		out.Removed = append(out.Removed, l)
		tomb := state.Tombstone{Workspace: spec.Name, Agent: agent, Repo: l.Repo, Path: l.Path, Branch: l.Branch, Retired: now}
		if r := ws.Replica(agent); r != nil {
			if c := r.CopyAt(l.Path); c != nil {
				tomb.CopyID, tomb.PR = c.CopyID, c.PR
			}
		}
		if archived != "" {
			entry := state.ArchiveEntry{Path: archived, Repo: l.Repo, Workspace: spec.Name, Agent: agent,
				Source: l.Path, Branch: l.Branch, CopyID: tomb.CopyID, Created: now}
			if meta, id, err := gitops.ReadArchive(archived); err == nil {
				entry.ID, entry.Work, entry.Created = id, meta.Work, meta.Created
				if meta.Branch != "" {
					entry.Branch = meta.Branch
				}
			}
			tomb.Archive = entry.ID
			out.Archives = append(out.Archives, entry)
		}
		out.Tombstones = append(out.Tombstones, tomb)
	}
	return out
}

// takeLease claims the agent's lease for a retirement, stopping its run
// first when opts allow.
func takeLease(opts RetireOptions, workspace, agent string) (*agents.Lease, error) {
	if opts.StateDir == "" {
		return nil, nil
	}
	path := agents.LeasePath(opts.StateDir, workspace, agent)
	holder := agents.Holder{PID: os.Getpid(), Command: "gitstead retire", Started: time.Now()}
	lease, err := agents.Acquire(path, holder)
	if !errors.Is(err, agents.ErrRunning) {
		return lease, err
	}
	if !opts.Stop {
		return nil, fmt.Errorf("%s/%s is running; stop it first (gitstead ws stop %s %s), or retire with --stop", workspace, agent, workspace, agent)
	}
	ended, err := agents.Stop(path, opts.StopTimeout, false)
	if err != nil {
		return nil, err
	}
	if !ended {
		return nil, fmt.Errorf("%s/%s is still running after %s; gitstead ws stop --kill ends it", workspace, agent, opts.StopTimeout)
	}
	return agents.Acquire(path, holder)
}
