package config

import (
	"context"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"time"
)

// Machine is one machine's own settings, laid over the rest of the config on
// that machine: where its clones and workspaces go, how many agents it runs,
// and repos only it tracks. One config file then serves every machine.
//
// base_dir and each workspace_defaults key it sets replace the top-level
// value. overrides, repos and discovery are added after the top-level
// lists, so an override here wins over a top-level one for the same repo.
// A workspace or fleet with a top-level one's name changes the keys it sets
// of that one; with a new name it is declared on this machine only.
type Machine struct {
	BaseDir           string                    `koanf:"base_dir"           yaml:"base_dir,omitempty"`
	WorkspaceDefaults *MachineWorkspaceDefaults `koanf:"workspace_defaults" yaml:"workspace_defaults,omitempty"`
	Overrides         []Override                `koanf:"overrides"          yaml:"overrides,omitempty"  validate:"dive"`
	Repos             []ManualRepo              `koanf:"repos"              yaml:"repos,omitempty"      validate:"dive"`
	Discovery         []DiscoveryRule           `koanf:"discovery"          yaml:"discovery,omitempty"  validate:"dive"`
	Workspaces        []Workspace               `koanf:"workspaces"         yaml:"workspaces,omitempty" validate:"dive"`
	Fleets            []Fleet                   `koanf:"fleets"             yaml:"fleets,omitempty"     validate:"dive"`
}

// MachineWorkspaceDefaults sets workspace_defaults keys for one machine. A
// nil pointer leaves the top-level value; env adds to it, name by name.
type MachineWorkspaceDefaults struct {
	Root      *string           `koanf:"root"       yaml:"root,omitempty"`
	Isolation *string           `koanf:"isolation"  yaml:"isolation,omitempty"  validate:"omitempty,oneof=worktree clone reference-clone shared"`
	Agents    *int              `koanf:"agents"     yaml:"agents,omitempty"     validate:"omitempty,min=1,max=64"`
	AgentName *string           `koanf:"agent_name" yaml:"agent_name,omitempty"`
	Branch    *string           `koanf:"branch"     yaml:"branch,omitempty"`
	BaseRef   *string           `koanf:"base_ref"   yaml:"base_ref,omitempty"`
	Agent     *string           `koanf:"agent"      yaml:"agent,omitempty"`
	CacheRoot *string           `koanf:"cache_root" yaml:"cache_root,omitempty"`
	Env       map[string]string `koanf:"env"        yaml:"env,omitempty"`
}

// machineLookupTimeout bounds asking macOS for this machine's name.
const machineLookupTimeout = 2 * time.Second

// MachineSection reports the machine this config was loaded for and whether
// a machines: section by that name was laid over it.
func (c *Config) MachineSection() (name string, applied bool) {
	return c.Machine, c.machineApplied
}

// TopLevel is the config as its top level sets it, without this machine's
// section: what a command that writes top-level settings back from what it
// read, as setup does, starts from, so one machine's settings never become
// every machine's.
func (c *Config) TopLevel() *Config {
	if c.topLevel == nil {
		return c
	}
	return c.topLevel
}

// MachineNames lists the machines: sections, sorted.
func (c *Config) MachineNames() []string {
	return slices.Sorted(maps.Keys(c.Machines))
}

// applyMachines lays this machine's section over cfg, after checking that
// every section names a machine as a section must and leaves a valid config
// on its own machine, so a mistake in one machine's section shows on every
// machine that loads the file. It runs after normalize and normalizes what
// it adds.
func applyMachines(ctx context.Context, cfg *Config) error {
	if len(cfg.Machines) == 0 {
		return nil
	}
	if cfg.Machine == "" {
		cfg.Machine = detectMachine(ctx)
	}
	current := ""
	for _, name := range cfg.MachineNames() {
		if !validName(name) || strings.Contains(name, ".") {
			return &invalidError{fmt.Errorf("machines: %q can't name a machine: use letters, digits, '-' and '_', starting with a letter or digit", name)}
		}
		merged, err := cfg.forMachine(ctx, name)
		if err != nil {
			return err
		}
		if err := merged.validate(); err != nil {
			return &invalidError{fmt.Errorf("machines.%s: %w", name, err)}
		}
		if strings.EqualFold(name, cfg.Machine) && (current == "" || name == cfg.Machine) {
			current = name
		}
	}
	if current == "" {
		return nil
	}
	merged, err := cfg.forMachine(ctx, current)
	if err != nil {
		return err
	}
	top := *cfg
	merged.Machine, merged.machineApplied, merged.topLevel = current, true, &top
	*cfg = *merged
	return nil
}

// forMachine returns cfg with machine name's section laid over it,
// normalized. cfg itself is left as it was.
func (c *Config) forMachine(ctx context.Context, name string) (*Config, error) {
	m := c.Machines[name]
	out := *c
	// normalize writes into these lists' elements; each is the copy's own.
	out.Providers = slices.Clone(c.Providers)
	out.Discovery = append(slices.Clone(c.Discovery), m.Discovery...)
	out.Repos = append(slices.Clone(c.Repos), m.Repos...)
	out.Overrides = append(slices.Clone(c.Overrides), m.Overrides...)
	if m.BaseDir != "" {
		out.BaseDir = m.BaseDir
	}
	out.WorkspaceDefaults = m.WorkspaceDefaults.over(c.WorkspaceDefaults)
	out.Workspaces = slices.Clone(c.Workspaces)
	for _, w := range m.Workspaces {
		if i := slices.IndexFunc(out.Workspaces, func(t Workspace) bool { return t.Name == w.Name }); i >= 0 {
			out.Workspaces[i] = w.over(out.Workspaces[i])
		} else {
			out.Workspaces = append(out.Workspaces, w)
		}
	}
	out.Fleets = slices.Clone(c.Fleets)
	for _, f := range m.Fleets {
		if i := slices.IndexFunc(out.Fleets, func(t Fleet) bool { return t.Name == f.Name }); i >= 0 {
			out.Fleets[i] = f.over(out.Fleets[i])
		} else {
			out.Fleets = append(out.Fleets, f)
		}
	}
	if err := normalize(ctx, &out); err != nil {
		return nil, fmt.Errorf("machines.%s: %w", name, err)
	}
	return &out, nil
}

// over returns base with the keys d sets replaced, and d's env added to
// base's.
func (d *MachineWorkspaceDefaults) over(base WorkspaceDefaults) WorkspaceDefaults {
	if d == nil {
		return base
	}
	for _, f := range []struct {
		set *string
		to  *string
	}{
		{d.Root, &base.Root}, {d.Isolation, &base.Isolation}, {d.AgentName, &base.AgentName},
		{d.Branch, &base.Branch}, {d.BaseRef, &base.BaseRef}, {d.Agent, &base.Agent}, {d.CacheRoot, &base.CacheRoot},
	} {
		if f.set != nil {
			*f.to = *f.set
		}
	}
	if d.Agents != nil {
		base.Agents = *d.Agents
	}
	base.Env = mergeEnv(base.Env, d.Env)
	return base
}

// over returns base with every key w sets replaced; w's env adds to
// base's, name by name. A key w leaves empty keeps base's value.
func (w Workspace) over(base Workspace) Workspace {
	if len(w.Repos) > 0 || w.Set != "" || w.Path != "" {
		// A workspace's repos come from one of repos, set and path.
		base.Repos, base.Set, base.Path = w.Repos, w.Set, w.Path
	}
	for _, f := range []struct {
		set string
		to  *string
	}{
		{w.Root, &base.Root}, {w.Isolation, &base.Isolation}, {w.AgentName, &base.AgentName},
		{w.Branch, &base.Branch}, {w.BaseRef, &base.BaseRef}, {w.Agent, &base.Agent},
	} {
		if f.set != "" {
			*f.to = f.set
		}
	}
	if w.Agents != 0 {
		base.Agents = w.Agents
	}
	if w.RemoteSync != nil {
		base.RemoteSync = w.RemoteSync
	}
	if w.Hooks.PostCreate != nil {
		base.Hooks.PostCreate = w.Hooks.PostCreate
	}
	if w.Ports.Base != nil {
		base.Ports.Base = w.Ports.Base
	}
	if w.Ports.Count != nil {
		base.Ports.Count = w.Ports.Count
	}
	base.Env = mergeEnv(base.Env, w.Env)
	return base
}

// over returns base with every key f sets replaced.
func (f Fleet) over(base Fleet) Fleet {
	if len(f.Workspaces) > 0 {
		base.Workspaces = f.Workspaces
	}
	for _, v := range []struct {
		set string
		to  *string
	}{
		{f.Agent, &base.Agent}, {f.Launcher, &base.Launcher}, {f.Host, &base.Host}, {f.RemoteCommand, &base.RemoteCommand},
	} {
		if v.set != "" {
			*v.to = v.set
		}
	}
	return base
}

// mergeEnv is base with add's names added or replaced, in a map of its own.
func mergeEnv(base, add map[string]string) map[string]string {
	if len(add) == 0 {
		return base
	}
	out := maps.Clone(base)
	if out == nil {
		out = make(map[string]string, len(add))
	}
	maps.Copy(out, add)
	return out
}

// detectMachine is this machine's name for machines: on macOS its local
// host name (System Settings > Sharing), which DHCP doesn't change;
// elsewhere, or when that can't be read, the host name up to its first dot.
func detectMachine(ctx context.Context) string {
	if runtime.GOOS == "darwin" {
		lctx, cancel := context.WithTimeout(ctx, machineLookupTimeout)
		out, err := exec.CommandContext(lctx, "scutil", "--get", "LocalHostName").Output()
		cancel()
		if name := strings.TrimSpace(string(out)); err == nil && name != "" {
			return name
		}
	}
	host, err := os.Hostname()
	if err != nil {
		return ""
	}
	name, _, _ := strings.Cut(host, ".")
	return name
}
