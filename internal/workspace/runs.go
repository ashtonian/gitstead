package workspace

import (
	"time"

	"fmt"

	agents "github.com/ashtonian/git-mgr/internal/agent"
	"github.com/ashtonian/git-mgr/internal/gitops"
	"github.com/ashtonian/git-mgr/internal/state"
)

// Agent lifecycle states, as AgentState reports them.
const (
	AgentReady    = "ready"    // its copies are made; it hasn't run, or ran and ended
	AgentRunning  = "running"  // its run holds the lease
	AgentStopping = "stopping" // `ws stop` asked its run to end
	AgentStopped  = "stopped"  // its last run ended
	AgentMissing  = "missing"  // a copy of its is gone from disk
	AgentRetiring = "retiring" // retiring it began and didn't finish
)

// WaitingFor reports whether r's agent, running, is waiting for you: since
// when, and for what if it said.
func WaitingFor(r *state.ReplicaState) (since time.Time, what string, ok bool) {
	run := r.Run
	if run == nil || !run.Ended.IsZero() || run.Waiting.IsZero() || run.Waiting.Before(run.Started) {
		return time.Time{}, "", false
	}
	return run.Waiting, run.WaitingFor, true
}

// AgentState is where an agent is in its lifecycle, from its lease and its
// replica's record. stateDir is the state file's directory.
func AgentState(stateDir, workspace string, r *state.ReplicaState) string {
	if !r.Retiring.IsZero() {
		return AgentRetiring
	}
	if _, held := agents.Read(agents.LeasePath(stateDir, workspace, r.Agent)); held {
		if run := r.Run; run != nil && run.StopRequested.After(run.Started) {
			return AgentStopping
		}
		return AgentRunning
	}
	for _, c := range r.Copies {
		if !gitops.HasGitDir(c.Path) {
			return AgentMissing
		}
	}
	if r.Run != nil {
		return AgentStopped
	}
	return AgentReady
}

// ForgetRuns removes the lease files of agents that aren't running, once
// their replicas are gone.
func ForgetRuns(stateDir, workspace string, agentNames ...string) {
	if stateDir == "" {
		return
	}
	for _, a := range agentNames {
		agents.Forget(agents.LeasePath(stateDir, workspace, a))
	}
}

// CheckIdle fails when the agent's run holds its lease.
func CheckIdle(stateDir, workspace, agent string) error {
	if h, held := agents.Read(agents.LeasePath(stateDir, workspace, agent)); held {
		return fmt.Errorf("%s/%s is running (pid %d); stop it first: git-mgr ws stop %s %s", workspace, agent, h.PID, workspace, agent)
	}
	return nil
}
