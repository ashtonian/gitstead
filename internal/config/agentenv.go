package config

import (
	"bytes"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"text/template"
)

// PortsConfig hands each agent a block of ports of its own, so agents' dev
// servers and test databases don't collide.
type PortsConfig struct {
	// Base is the lowest port handed out; 0 hands out none.
	Base int `koanf:"base" yaml:"base" default:"41000" validate:"min=0,max=65535"`
	// Count is the size of each agent's block: GITSTEAD_PORT up to
	// GITSTEAD_PORT+Count-1.
	Count int `koanf:"count" yaml:"count" default:"10" validate:"min=0,max=1000"`
}

// PortsOverride sets a workspace's ports. A nil pointer means "not set".
type PortsOverride struct {
	Base  *int `koanf:"base"  yaml:"base,omitempty"  validate:"omitempty,min=0,max=65535"`
	Count *int `koanf:"count" yaml:"count,omitempty" validate:"omitempty,min=0,max=1000"`
}

// AgentVars are what an agent's commands -- the agent itself, `ws exec`,
// post_create hooks -- are told of it: as GITSTEAD_* variables, and to the
// templates of its workspace's env.
type AgentVars struct {
	Workspace, Agent string
	// N is the agent's number in its workspace, from 1.
	N int
	// Replica is the agent's folder.
	Replica string
	// Port is the first of the agent's Ports ports; 0 when it has none.
	Port, Ports int
	// Cache is the directory the workspace's agents share for build caches.
	Cache string
}

// Environ is v as GITSTEAD_* variables; those without a value are left out.
func (v AgentVars) Environ() []string {
	env := []string{
		"GITSTEAD_WORKSPACE=" + v.Workspace,
		"GITSTEAD_AGENT=" + v.Agent,
		"GITSTEAD_AGENT_N=" + strconv.Itoa(v.N),
		"GITSTEAD_REPLICA=" + v.Replica,
	}
	if v.Port > 0 {
		env = append(env, "GITSTEAD_PORT="+strconv.Itoa(v.Port), "GITSTEAD_PORTS="+strconv.Itoa(v.Ports))
	}
	if v.Cache != "" {
		env = append(env, "GITSTEAD_CACHE="+v.Cache)
	}
	return env
}

// ContextVars are the variables gitstead tells an agent's commands and hooks
// where they are. A command started from inside one -- an agent running
// gitstead -- must not pass its own on to another, so Environ leaves them out.
var ContextVars = []string{
	"GITSTEAD_WORKSPACE", "GITSTEAD_AGENT", "GITSTEAD_AGENT_N", "GITSTEAD_REPLICA",
	"GITSTEAD_PORT", "GITSTEAD_PORTS", "GITSTEAD_CACHE",
	"GITSTEAD_EVENT", "GITSTEAD_PATH", "GITSTEAD_REPO", "GITSTEAD_SOURCE", "GITSTEAD_BRANCH",
}

// Environ is this process's environment without its own ContextVars: what
// gitstead starts an agent, a hook or a command in a copy with, before adding
// theirs.
func Environ() []string {
	return slices.DeleteFunc(os.Environ(), func(kv string) bool {
		name, _, _ := strings.Cut(kv, "=")
		return slices.Contains(ContextVars, name)
	})
}

// envFuncs are the functions env templates can call: {{add .Port 1}}.
var envFuncs = template.FuncMap{"add": func(a, b int) int { return a + b }}

var envNameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// RenderEnv renders env's templates for v, as NAME=value, sorted by name.
func RenderEnv(env map[string]string, v AgentVars) ([]string, error) {
	out := make([]string, 0, len(env))
	for _, name := range slices.Sorted(maps.Keys(env)) {
		tmpl, err := template.New(name).Funcs(envFuncs).Option("missingkey=error").Parse(env[name])
		if err != nil {
			return nil, fmt.Errorf("env %s: %w", name, err)
		}
		var b bytes.Buffer
		if err := tmpl.Execute(&b, v); err != nil {
			return nil, fmt.Errorf("env %s: %w", name, err)
		}
		out = append(out, name+"="+b.String())
	}
	return out, nil
}

// checkEnv checks env's names and templates, rendering them for a sample
// agent.
func checkEnv(where string, env map[string]string) error {
	for name := range env {
		switch {
		case !envNameRE.MatchString(name):
			return fmt.Errorf("%s: env %q is not a variable name", where, name)
		case strings.HasPrefix(name, "GITSTEAD_"):
			return fmt.Errorf("%s: env %s: GITSTEAD_* variables are gitstead's own", where, name)
		}
	}
	sample := AgentVars{Workspace: "w", Agent: "agent-1", N: 1, Replica: "/w/agent-1", Port: 41000, Ports: 10, Cache: "/c/w"}
	if _, err := RenderEnv(env, sample); err != nil {
		return fmt.Errorf("%s: %w", where, err)
	}
	return nil
}

// WorkspaceCacheDir is the cache directory workspace name's agents share.
func (c *Config) WorkspaceCacheDir(name string) string {
	return filepath.Join(c.WorkspaceDefaults.CacheRoot, name)
}

// defaultCacheRoot is where workspaces' shared caches go unless
// workspace_defaults.cache_root says otherwise.
func defaultCacheRoot() string {
	return filepath.Join(CacheDir(), "workspaces")
}
