package config

import (
	"bytes"
	"fmt"
	"maps"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"text/template"

	"github.com/ashtonian/git-mgr/internal/repourl"
)

// Isolation modes: how each agent's copies are made.
const (
	IsolationWorktree       = "worktree"        // a linked worktree of the repo's clone
	IsolationClone          = "clone"           // a clone with its own objects, copied from the repo's clone
	IsolationReferenceClone = "reference-clone" // a clone borrowing the repo's clone's objects
	IsolationShared         = "shared"          // every agent works in the one folder
)

// AgentProfile is a command workspaces launch agents with.
type AgentProfile struct {
	Name string `koanf:"name" yaml:"name" validate:"required"`
	// Command is typed into an interactive shell in the agent's replica, so
	// shell functions and aliases work.
	Command string `koanf:"command" yaml:"command" validate:"required"`
	// Resume is typed instead of Command when the agent has run in the
	// replica before, such as `claude --continue`.
	Resume string            `koanf:"resume" yaml:"resume,omitempty"`
	Env    map[string]string `koanf:"env" yaml:"env,omitempty"`
}

// WorkspaceDefaults are the settings a workspace doesn't set itself.
type WorkspaceDefaults struct {
	// Root holds a directory per workspace.
	Root      string `koanf:"root"       yaml:"root"       default:"~/workspaces"`
	Isolation string `koanf:"isolation"  yaml:"isolation"  default:"worktree" validate:"oneof=worktree clone reference-clone shared"`
	// Agents is how many agents a workspace has, each with its own replica.
	Agents int `koanf:"agents" yaml:"agents" default:"1" validate:"min=1,max=64"`
	// AgentName names agent N ({{.N}}, {{.Workspace}}).
	AgentName string `koanf:"agent_name" yaml:"agent_name" default:"agent-{{.N}}"`
	// Branch names a new copy's branch ({{.Workspace}}, {{.Agent}}, {{.N}},
	// {{.Repo}}). Existing copies keep theirs.
	Branch string `koanf:"branch" yaml:"branch" default:"agents/{{.Workspace}}/{{.Agent}}"`
	// BaseRef is what new branches start from; empty for the upstream's
	// default branch.
	BaseRef string `koanf:"base_ref" yaml:"base_ref,omitempty"`
	// Agent is the agent profile workspaces' agents run; empty for the first
	// declared, or detected, one.
	Agent string `koanf:"agent" yaml:"agent,omitempty"`
	// Hooks are every workspace's own hooks, unless it sets its own.
	Hooks WorkspaceHooks `koanf:"hooks" yaml:"hooks,omitempty"`
	// Env is set for each agent's commands, after its profile's env; values
	// are templates of AgentVars ({{.Port}}, {{add .Port 1}}, {{.Cache}}).
	// A workspace's own env adds to it, name by name.
	Env map[string]string `koanf:"env" yaml:"env,omitempty"`
	// Ports gives each agent a block of ports of its own.
	Ports PortsConfig `koanf:"ports" yaml:"ports"`
	// CacheRoot holds a directory per workspace that its agents share for
	// build caches; empty for $XDG_CACHE_HOME/git-mgr/workspaces.
	CacheRoot string `koanf:"cache_root" yaml:"cache_root,omitempty"`
}

// Workspace is one repo, or several related ones, worked on by a number of
// agents, each in a replica of its own. Its repos are tracked repo keys,
// globs over them, local paths or URLs; a path or URL not yet tracked is
// adopted or cloned first. With Path instead, every agent works in that
// folder.
type Workspace struct {
	Name  string   `koanf:"name"  yaml:"name"            validate:"required"`
	Repos []string `koanf:"repos" yaml:"repos,omitempty"`
	// Set selects the repos with a named set instead.
	Set  string `koanf:"set"  yaml:"set,omitempty"`
	Path string `koanf:"path" yaml:"path,omitempty"`
	// Root holds the workspace's directory; default workspace_defaults.root.
	Root       string              `koanf:"root"        yaml:"root,omitempty"`
	Isolation  string              `koanf:"isolation"   yaml:"isolation,omitempty" validate:"omitempty,oneof=worktree clone reference-clone shared"`
	Agents     int                 `koanf:"agents"      yaml:"agents,omitempty"    validate:"min=0,max=64"`
	AgentName  string              `koanf:"agent_name"  yaml:"agent_name,omitempty"`
	Branch     string              `koanf:"branch"      yaml:"branch,omitempty"`
	BaseRef    string              `koanf:"base_ref"    yaml:"base_ref,omitempty"`
	Agent      string              `koanf:"agent"       yaml:"agent,omitempty"`
	RemoteSync *RemoteSyncOverride `koanf:"remote_sync" yaml:"remote_sync,omitempty"`
	Hooks      WorkspaceHooks      `koanf:"hooks"       yaml:"hooks,omitempty"`
	Env        map[string]string   `koanf:"env"         yaml:"env,omitempty"`
	Ports      PortsOverride       `koanf:"ports"       yaml:"ports,omitempty"`
}

// FindWorkspace returns the declared workspace with name, or nil.
func (c *Config) FindWorkspace(name string) *Workspace {
	for i := range c.Workspaces {
		if c.Workspaces[i].Name == name {
			return &c.Workspaces[i]
		}
	}
	return nil
}

// FindAgent returns the named agent profile, or nil.
func (c *Config) FindAgent(name string) *AgentProfile {
	for i := range c.Agents {
		if c.Agents[i].Name == name {
			return &c.Agents[i]
		}
	}
	return nil
}

// WithDefaults returns w with every setting it leaves empty taken from
// workspace_defaults. A workspace over a path is shared.
func (c *Config) WithDefaults(w Workspace) Workspace {
	d := c.WorkspaceDefaults
	if w.Root == "" {
		w.Root = d.Root
	}
	if w.Isolation == "" {
		w.Isolation = d.Isolation
		if w.Path != "" {
			w.Isolation = IsolationShared
		}
	}
	if w.Agents == 0 {
		w.Agents = d.Agents
	}
	if w.AgentName == "" {
		w.AgentName = d.AgentName
	}
	if w.Branch == "" {
		w.Branch = d.Branch
	}
	if w.BaseRef == "" {
		w.BaseRef = d.BaseRef
	}
	if w.Agent == "" {
		w.Agent = d.Agent
	}
	if w.Hooks.PostCreate == nil {
		w.Hooks.PostCreate = d.Hooks.PostCreate
	}
	if len(d.Env) > 0 {
		env := maps.Clone(d.Env)
		maps.Copy(env, w.Env)
		w.Env = env
	}
	if w.Ports.Base == nil {
		w.Ports.Base = &d.Ports.Base
	}
	if w.Ports.Count == nil {
		w.Ports.Count = &d.Ports.Count
	}
	w.Root, w.Path = ExpandHome(w.Root), ExpandHome(w.Path)
	return w
}

// PortBlock is where the agents' port blocks start and how big each is; a
// count of 0 hands out none. w must have its defaults.
func (w Workspace) PortBlock() (base, count int) {
	if w.Ports.Base == nil || w.Ports.Count == nil || *w.Ports.Base == 0 {
		return 0, 0
	}
	return *w.Ports.Base, *w.Ports.Count
}

// Dir is the workspace's directory under its root. w must have its defaults.
func (w Workspace) Dir() string {
	return filepath.Join(w.Root, w.Name)
}

// AgentNames names the workspace's agents, 1 to Agents. w must have its
// defaults.
func (w Workspace) AgentNames() ([]string, error) {
	tmpl, err := template.New("agent_name").Option("missingkey=error").Parse(w.AgentName)
	if err != nil {
		return nil, fmt.Errorf("agent_name %q: %w", w.AgentName, err)
	}
	names := make([]string, 0, w.Agents)
	for n := 1; n <= w.Agents; n++ {
		var b bytes.Buffer
		if err := tmpl.Execute(&b, struct {
			N         int
			Workspace string
		}{n, w.Name}); err != nil {
			return nil, fmt.Errorf("agent_name %q: %w", w.AgentName, err)
		}
		name := b.String()
		if !validName(name) {
			return nil, fmt.Errorf("agent_name %q gives %q for agent %d; a name is letters, digits, '-', '_' and '.', starting with a letter or digit", w.AgentName, name, n)
		}
		if slices.Contains(names, name) {
			return nil, fmt.Errorf("agent_name %q gives %q twice; include {{.N}}", w.AgentName, name)
		}
		names = append(names, name)
	}
	return names, nil
}

// BranchFor names the branch of agent n's copy of the repo named repo. w must
// have its defaults.
func (w Workspace) BranchFor(agent string, n int, repo string) (string, error) {
	tmpl, err := template.New("branch").Option("missingkey=error").Parse(w.Branch)
	if err != nil {
		return "", fmt.Errorf("branch %q: %w", w.Branch, err)
	}
	var b bytes.Buffer
	if err := tmpl.Execute(&b, struct {
		Workspace, Agent, Repo string
		N                      int
	}{w.Name, agent, repo, n}); err != nil {
		return "", fmt.Errorf("branch %q: %w", w.Branch, err)
	}
	branch := b.String()
	if !ValidBranch(branch) {
		return "", fmt.Errorf("branch %q gives %q, which git doesn't allow as a branch name", w.Branch, branch)
	}
	return branch, nil
}

var nameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// validName reports whether name is usable as a workspace or agent name: in
// paths, branch names, refs and terminal session names.
func validName(name string) bool {
	return len(name) <= 64 && nameRE.MatchString(name) && !strings.HasSuffix(name, ".lock") && !strings.Contains(name, "..")
}

// ValidBranch reports whether git accepts name as a branch name, following
// git check-ref-format's rules.
func ValidBranch(name string) bool {
	if name == "" || name == "@" || strings.HasPrefix(name, "-") || strings.HasPrefix(name, "/") ||
		strings.HasSuffix(name, "/") || strings.HasSuffix(name, ".") ||
		strings.Contains(name, "..") || strings.Contains(name, "//") || strings.Contains(name, "@{") {
		return false
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f || strings.ContainsRune(" ~^:?*[\\", r) {
			return false
		}
	}
	for _, part := range strings.Split(name, "/") {
		if strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return false
		}
	}
	return true
}

// validateWorkspaces checks the agents and workspaces config declares.
func validateWorkspaces(cfg *Config) error {
	agents := map[string]bool{}
	for _, a := range cfg.Agents {
		if !validName(a.Name) {
			return fmt.Errorf("agent %q: a name is letters, digits, '-', '_' and '.', starting with a letter or digit", a.Name)
		}
		if agents[a.Name] {
			return fmt.Errorf("duplicate agent name: %q", a.Name)
		}
		agents[a.Name] = true
	}

	if a := cfg.WorkspaceDefaults.Agent; a != "" && len(cfg.Agents) > 0 && cfg.FindAgent(a) == nil {
		return fmt.Errorf("workspace_defaults.agent: unknown agent %q (declare it under agents:)", a)
	}
	if err := checkEnv("workspace_defaults", cfg.WorkspaceDefaults.Env); err != nil {
		return err
	}
	names := map[string]bool{}
	for _, decl := range cfg.Workspaces {
		if names[decl.Name] {
			return fmt.Errorf("duplicate workspace name: %q", decl.Name)
		}
		names[decl.Name] = true
		if err := cfg.CheckWorkspace(decl); err != nil {
			return err
		}
	}
	return nil
}

// CheckWorkspace validates one workspace, declared or given on the command
// line.
func (c *Config) CheckWorkspace(decl Workspace) error {
	if !validName(decl.Name) || strings.Contains(decl.Name, ".") {
		return fmt.Errorf("workspace %q: a name is letters, digits, '-' and '_', starting with a letter or digit", decl.Name)
	}
	if err := validateWorkspace(c, decl); err != nil {
		return fmt.Errorf("workspace %q: %w", decl.Name, err)
	}
	return nil
}

func validateWorkspace(cfg *Config, decl Workspace) error {
	if err := checkEnv("env", decl.Env); err != nil {
		return err
	}
	sources := 0
	for _, set := range []bool{len(decl.Repos) > 0, decl.Set != "", decl.Path != ""} {
		if set {
			sources++
		}
	}
	if sources != 1 {
		return fmt.Errorf("set one of repos, set and path")
	}
	if decl.Set != "" {
		if _, ok := cfg.FindSet(decl.Set); !ok {
			return fmt.Errorf("unknown set %q", decl.Set)
		}
	}
	for _, r := range decl.Repos {
		if err := repourl.CheckNoCredentials(r); err != nil {
			return err
		}
	}
	w := cfg.WithDefaults(decl)
	if decl.Path != "" && w.Isolation != IsolationShared {
		return fmt.Errorf("a workspace over a path is shared; drop isolation: %s", w.Isolation)
	}
	if w.Isolation == IsolationShared && decl.Path == "" && (decl.Set != "" || len(decl.Repos) != 1) {
		return fmt.Errorf("shared isolation works in one folder: give path, or exactly one repo")
	}
	if _, err := w.AgentNames(); err != nil {
		return err
	}
	if w.Agent != "" && len(cfg.Agents) > 0 && cfg.FindAgent(w.Agent) == nil {
		return fmt.Errorf("unknown agent %q (declare it under agents:)", w.Agent)
	}
	_, err := w.BranchFor("agent", 1, "repo")
	return err
}

// Matches reports whether a repo belongs to the set: it must survive
// ExcludeArchived, then match any Repos glob or carry any of Tags (both
// empty means every repo).
func (s *RepoSet) Matches(key string, hasTag func(string) bool, archived bool) bool {
	if s.ExcludeArchived && archived {
		return false
	}
	if len(s.Repos) == 0 && len(s.Tags) == 0 {
		return true
	}
	for _, g := range s.Repos {
		if m, _ := filepath.Match(g, key); m {
			return true
		}
	}
	for _, t := range s.Tags {
		if hasTag(t) {
			return true
		}
	}
	return false
}
