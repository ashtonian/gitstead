// Package agent runs coding agents in workspace replicas: which command an
// agent profile runs, the lease that marks an agent running, and the run
// itself -- the agent's command in an interactive shell, recorded when it
// starts and ends, stoppable from another terminal.
package agent

import (
	"errors"
	"fmt"
	"os/exec"

	"github.com/ashtonian/gitstead/internal/config"
)

// Known are the agents gitstead offers when config declares none, if they
// are on PATH.
var Known = []config.AgentProfile{
	{Name: "claude", Command: "claude", Resume: "claude --continue"},
	{Name: "codex", Command: "codex", Resume: "codex resume --last"},
}

// ErrNoProfile reports that no agent profile is configured or detected.
var ErrNoProfile = errors.New("no agent profile: declare one under agents: in config, or install claude or codex")

// Profiles returns config's agent profiles, or with none declared, the
// detected agents found on PATH.
func Profiles(cfg *config.Config) []config.AgentProfile {
	if len(cfg.Agents) > 0 {
		return cfg.Agents
	}
	var out []config.AgentProfile
	for _, p := range Known {
		if _, err := exec.LookPath(p.Command); err == nil {
			out = append(out, p)
		}
	}
	return out
}

// Profile returns the named profile, or with name empty the first one.
func Profile(cfg *config.Config, name string) (config.AgentProfile, error) {
	profiles := Profiles(cfg)
	if name == "" {
		if len(profiles) == 0 {
			return config.AgentProfile{}, ErrNoProfile
		}
		return profiles[0], nil
	}
	for _, p := range profiles {
		if p.Name == name {
			return p, nil
		}
	}
	if len(cfg.Agents) == 0 {
		return config.AgentProfile{}, fmt.Errorf("no agent %q: declare it under agents: in config", name)
	}
	return config.AgentProfile{}, fmt.Errorf("no agent %q under agents: in config", name)
}

// CommandFor is what an agent with profile p types: its resume command when
// it ran in the replica before and fresh isn't asked for, else its command.
func CommandFor(p config.AgentProfile, ranBefore, fresh bool) string {
	if ranBefore && !fresh && p.Resume != "" {
		return p.Resume
	}
	return p.Command
}
