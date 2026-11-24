package config

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

const machinesYAML = `
base_dir: /repos
workspace_defaults:
  root: /ws
  agents: 2
  env: {SHARED: "1"}
repos:
  - url: git@github.com:me/shared.git
workspaces:
  - name: app
    repos: [github.com/me/app]
    agents: 3
  - name: notes
    path: /notes
fleets:
  - name: all
    workspaces: [app, notes]
    launcher: tmux
machines:
  desktop:
    base_dir: ~/fast/repos
    workspace_defaults:
      root: /Volumes/A/ws
      env: {CACHE: /Volumes/A/cache}
    overrides:
      - repos: ["github.com/acme/*"]
        base_dir: /Volumes/B/repos
    repos:
      - url: git@github.com:acme/tool.git
    workspaces:
      - name: app
        agents: 6
        root: /Volumes/B/ws
      - name: tools
        repos: [github.com/acme/tool]
    fleets:
      - name: all
        workspaces: [app, notes, tools]
  laptop:
    workspace_defaults:
      agents: 1
`

func writeMachinesConfig(t *testing.T, yaml string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// This machine's section is laid over the rest: its base_dir and
// workspace_defaults keys replace the top level's, its overrides and repos
// come after the top level's, and its workspaces and fleets change the
// top-level ones of their name or add their own.
func TestLoad_MachineSectionApplies(t *testing.T) {
	path := writeMachinesConfig(t, machinesYAML)
	t.Setenv("GITSTEAD_MACHINE", "desktop")
	cfg, err := Load(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if name, applied := cfg.MachineSection(); name != "desktop" || !applied {
		t.Errorf("MachineSection = %q, %v; want desktop applied", name, applied)
	}
	if want := ExpandHome("~/fast/repos"); cfg.BaseDir != want {
		t.Errorf("base_dir = %q, want %q (expanded)", cfg.BaseDir, want)
	}
	d := cfg.WorkspaceDefaults
	if d.Root != "/Volumes/A/ws" || d.Agents != 2 || d.Env["SHARED"] != "1" || d.Env["CACHE"] != "/Volumes/A/cache" {
		t.Errorf("workspace_defaults = %+v", d)
	}
	if got := cfg.ResolveForRepo("github.com/acme/api").BaseDir; got != "/Volumes/B/repos" {
		t.Errorf("an acme repo's base_dir = %q, want the machine's override's", got)
	}
	if got := cfg.ResolveForRepo("github.com/me/app").BaseDir; got != cfg.BaseDir {
		t.Errorf("another repo's base_dir = %q, want %q", got, cfg.BaseDir)
	}
	if len(cfg.Repos) != 2 || cfg.Repos[1].URL != "git@github.com:acme/tool.git" {
		t.Errorf("repos = %+v, want the top level's then the machine's", cfg.Repos)
	}
	app := cfg.FindWorkspace("app")
	if app == nil || app.Agents != 6 || app.Root != "/Volumes/B/ws" || !slices.Equal(app.Repos, []string{"github.com/me/app"}) {
		t.Errorf("app = %+v, want 6 agents under /Volumes/B/ws, its repos kept", app)
	}
	if cfg.FindWorkspace("tools") == nil || cfg.FindWorkspace("notes") == nil {
		t.Errorf("workspaces = %+v, want notes kept and tools added", cfg.Workspaces)
	}
	if len(cfg.Fleets) != 1 || len(cfg.Fleets[0].Workspaces) != 3 || cfg.Fleets[0].Launcher != "tmux" {
		t.Errorf("fleets = %+v, want all with three workspaces and its launcher kept", cfg.Fleets)
	}

	// What setup writes back starts from the top level, untouched.
	top := cfg.TopLevel()
	if top.BaseDir != "/repos" || top.WorkspaceDefaults.Root != "/ws" || len(top.Repos) != 1 || len(top.Workspaces) != 2 || top.Workspaces[0].Agents != 3 {
		t.Errorf("TopLevel = base_dir %q, root %q, %d repos, workspaces %+v", top.BaseDir, top.WorkspaceDefaults.Root, len(top.Repos), top.Workspaces)
	}
	if _, applied := top.MachineSection(); applied {
		t.Error("the top level has no section applied")
	}
}

// A machine's name matches its section whatever its letter case, as macOS
// local host names are matched.
func TestLoad_MachineNameIgnoresCase(t *testing.T) {
	path := writeMachinesConfig(t, machinesYAML)
	t.Setenv("GITSTEAD_MACHINE", "Desktop")
	cfg, err := Load(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if _, applied := cfg.MachineSection(); !applied || cfg.FindWorkspace("tools") == nil {
		t.Errorf("Desktop's section should apply: %+v", cfg.Workspaces)
	}
}

// A machine with no section of its own gets the top-level settings, and
// the other machines' sections are still checked.
func TestLoad_MachineWithoutSection(t *testing.T) {
	path := writeMachinesConfig(t, machinesYAML)
	t.Setenv("GITSTEAD_MACHINE", "build-1")
	cfg, err := Load(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if name, applied := cfg.MachineSection(); name != "build-1" || applied {
		t.Errorf("MachineSection = %q, %v; want build-1 not applied", name, applied)
	}
	if cfg.BaseDir != "/repos" || cfg.WorkspaceDefaults.Agents != 2 || len(cfg.Workspaces) != 2 {
		t.Errorf("want the top-level settings, got base_dir %q, %+v", cfg.BaseDir, cfg.Workspaces)
	}
	if !slices.Equal(cfg.MachineNames(), []string{"desktop", "laptop"}) {
		t.Errorf("MachineNames = %v", cfg.MachineNames())
	}
}

func TestLoad_MachineSectionErrors(t *testing.T) {
	tests := []struct {
		name, yaml, want string
	}{
		{
			name: "a key no machine setting takes",
			yaml: "machines:\n  desktop:\n    base_dirr: /x\n",
			want: "base_dirr",
		},
		{
			name: "a top-level key a machine can't set",
			yaml: "machines:\n  desktop:\n    concurrency: 4\n",
			want: "concurrency",
		},
		{
			name: "another machine's section is invalid",
			yaml: "machines:\n  laptop:\n    workspaces:\n      - name: lonely\n",
			want: `machines.laptop: workspace "lonely": set one of repos, set and path`,
		},
		{
			name: "a name that isn't one",
			yaml: "machines:\n  \"my mac\":\n    base_dir: /x\n",
			want: "can't name a machine",
		},
		{
			name: "a value a tag refuses",
			yaml: "machines:\n  desktop:\n    workspace_defaults:\n      isolation: copy\n",
			want: "isolation",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeMachinesConfig(t, tt.yaml)
			t.Setenv("GITSTEAD_MACHINE", "desktop")
			if _, err := Load(t.Context(), path); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Load = %v, want an error naming %q", err, tt.want)
			}
		})
	}
}

// nonZero is a value of type t with every field, element and pointee set.
func nonZero(t reflect.Type) reflect.Value {
	v := reflect.New(t).Elem()
	switch t.Kind() {
	case reflect.String:
		v.SetString("x")
	case reflect.Int:
		v.SetInt(7)
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Pointer:
		v.Set(reflect.New(t.Elem()))
		v.Elem().Set(nonZero(t.Elem()))
	case reflect.Slice:
		v.Set(reflect.Append(reflect.MakeSlice(t, 0, 1), nonZero(t.Elem())))
	case reflect.Map:
		v.Set(reflect.MakeMap(t))
		v.SetMapIndex(nonZero(t.Key()), nonZero(t.Elem()))
	case reflect.Struct:
		for i := range t.NumField() {
			if t.Field(i).IsExported() {
				v.Field(i).Set(nonZero(t.Field(i).Type))
			}
		}
	}
	return v
}

// Every key a machine's workspace, fleet or workspace_defaults can set
// reaches the merged config: a key added to one of those types and not to
// its merge fails here.
func TestMachineMergesEveryKey(t *testing.T) {
	t.Run("workspace", func(t *testing.T) {
		w := nonZero(reflect.TypeFor[Workspace]()).Interface().(Workspace)
		w.Set, w.Path = "", "" // repos, set and path exclude each other
		if got := w.over(Workspace{Name: w.Name}); !reflect.DeepEqual(got, w) {
			t.Errorf("over = %+v, want every key of %+v", got, w)
		}
	})
	t.Run("fleet", func(t *testing.T) {
		f := nonZero(reflect.TypeFor[Fleet]()).Interface().(Fleet)
		if got := f.over(Fleet{Name: f.Name}); !reflect.DeepEqual(got, f) {
			t.Errorf("over = %+v, want every key of %+v", got, f)
		}
	})
	t.Run("workspace_defaults", func(t *testing.T) {
		d := nonZero(reflect.TypeFor[MachineWorkspaceDefaults]())
		md := d.Interface().(MachineWorkspaceDefaults)
		got := reflect.ValueOf(md.over(WorkspaceDefaults{}))
		for i := range d.NumField() {
			name := d.Type().Field(i).Name
			want := reflect.Indirect(d.Field(i)).Interface()
			field := got.FieldByName(name)
			if !field.IsValid() || !reflect.DeepEqual(field.Interface(), want) {
				t.Errorf("workspace_defaults.%s = %v, want %v", name, field, want)
			}
		}
	})
}
