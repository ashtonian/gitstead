package config

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestRenderEnv(t *testing.T) {
	v := AgentVars{Workspace: "app", Agent: "agent-2", N: 2, Replica: "/w/app/agent-2", Port: 41010, Ports: 10, Cache: "/c/app"}
	got, err := RenderEnv(map[string]string{
		"PORT":         "{{.Port}}",
		"API_PORT":     "{{add .Port 1}}",
		"DATABASE_URL": "postgres://localhost/app_{{.N}}",
		"TURBO_CACHE":  "{{.Cache}}/turbo",
	}, v)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"API_PORT=41011", "DATABASE_URL=postgres://localhost/app_2", "PORT=41010", "TURBO_CACHE=/c/app/turbo"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if env := strings.Join(v.Environ(), " "); !strings.Contains(env, "GITSTEAD_PORT=41010") || !strings.Contains(env, "GITSTEAD_AGENT_N=2") {
		t.Errorf("Environ = %s", env)
	}
	if env := strings.Join(AgentVars{Workspace: "a", Agent: "b"}.Environ(), " "); strings.Contains(env, "GITSTEAD_PORT") || strings.Contains(env, "GITSTEAD_CACHE") {
		t.Errorf("no ports and no cache are left out: %s", env)
	}
}

func TestCheckEnv(t *testing.T) {
	for name, env := range map[string]map[string]string{
		"bad name":      {"1PORT": "x"},
		"dotted":        {"A.B": "x"},
		"gitstead's own":  {"GITSTEAD_PORT": "1"},
		"unknown field": {"PORT": "{{.Prot}}"},
		"bad template":  {"PORT": "{{.Port"},
	} {
		if err := checkEnv("w", env); err == nil {
			t.Errorf("%s: accepted %v", name, env)
		}
	}
	if err := checkEnv("w", map[string]string{"PORT": "{{add .Port 2}}", "X": "plain"}); err != nil {
		t.Error(err)
	}
}

// A workspace's env adds to the defaults' name by name; its ports override
// theirs field by field.
func TestWithDefaults_EnvAndPorts(t *testing.T) {
	count := 20
	cfg := &Config{WorkspaceDefaults: WorkspaceDefaults{
		Env:   map[string]string{"PORT": "{{.Port}}", "NODE_ENV": "development"},
		Ports: PortsConfig{Base: 41000, Count: 10},
	}}
	w := cfg.WithDefaults(Workspace{Name: "a", Env: map[string]string{"NODE_ENV": "test"}, Ports: PortsOverride{Count: &count}})
	if !reflect.DeepEqual(w.Env, map[string]string{"PORT": "{{.Port}}", "NODE_ENV": "test"}) {
		t.Errorf("env = %v", w.Env)
	}
	if base, n := w.PortBlock(); base != 41000 || n != 20 {
		t.Errorf("ports = %d+%d", base, n)
	}
	if cfg.WorkspaceDefaults.Env["NODE_ENV"] != "development" {
		t.Error("merging changed the defaults")
	}
	off := 0
	if base, n := cfg.WithDefaults(Workspace{Name: "b", Ports: PortsOverride{Base: &off}}).PortBlock(); base != 0 || n != 0 {
		t.Errorf("base 0 hands out none, got %d+%d", base, n)
	}
}

// An agent's own context doesn't reach what it starts; gitstead's config
// variables do.
func TestEnviron(t *testing.T) {
	t.Setenv("GITSTEAD_PORT", "41000")
	t.Setenv("GITSTEAD_WORKSPACE", "caller")
	t.Setenv("GITSTEAD_CONFIG", "/c.yaml")
	env := strings.Join(Environ(), "\n")
	if strings.Contains(env, "GITSTEAD_PORT=") || strings.Contains(env, "GITSTEAD_WORKSPACE=") || !strings.Contains(env, "GITSTEAD_CONFIG=/c.yaml") {
		t.Errorf("Environ kept the caller's context or dropped config:\n%s", env)
	}
	// Every variable an agent is told is one Environ leaves out.
	full := AgentVars{Workspace: "w", Agent: "a", N: 1, Replica: "/r", Port: 1, Ports: 1, Cache: "/c"}
	for _, kv := range full.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if !slices.Contains(ContextVars, name) {
			t.Errorf("%s isn't in ContextVars", name)
		}
	}
}

// The variables gitstead sets for an agent name no setting, so gitstead run
// inside a workspace loads the config it loads outside one.
func TestLoad_ContextVarsAreNotSettings(t *testing.T) {
	want, err := Load(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range ContextVars {
		t.Setenv(name, "1")
	}
	got, err := Load(t.Context(), "")
	if err != nil {
		t.Fatalf("with the agent variables set: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("the agent variables changed the config:\n got %+v\nwant %+v", got, want)
	}
}
