package config

import (
	"errors"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
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

// A machine's clone_protocol replaces the top level's for every repo it
// would reach, a repo key entry's included, and each backup key it sets
// replaces the top level's -- an override's for a repo still wins. Another
// machine keeps the top level's.
func TestLoad_MachineCloneProtocolAndBackup(t *testing.T) {
	path := writeMachinesConfig(t, `
clone_protocol: ssh
backup:
  archive_dir: /archive
  keep_all: 12h
overrides:
  - repos: ["github.com/acme/*"]
    backup: {strategy: stash-ref}
repos:
  - repo: github.com/me/notes
machines:
  laptop:
    clone_protocol: https
    backup:
      archive_dir: ~/archive
      patch_dir: /Volumes/Bulk/patches
      strategy: patch
      archive_retention: 720h
`)
	t.Setenv("GITSTEAD_MACHINE", "laptop")
	cfg, err := Load(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CloneProtocol != "https" || cfg.CloneProtocolFor("github.com/me/notes", "_manual") != "https" {
		t.Errorf("clone_protocol = %q, for the repo entry %q; want the laptop's https", cfg.CloneProtocol, cfg.CloneProtocolFor("github.com/me/notes", "_manual"))
	}
	b := cfg.Backup
	if b.ArchiveDir != ExpandHome("~/archive") || b.PatchDir != "/Volumes/Bulk/patches" || b.Strategy != "patch" ||
		b.ArchiveRetention != 720*time.Hour || b.KeepAll != 12*time.Hour || !b.Enabled {
		t.Errorf("backup = %+v, want the laptop's keys over the top level's", b)
	}
	if got := cfg.ResolveForRepo("github.com/acme/api").Backup; got.Strategy != "stash-ref" || got.PatchDir != "/Volumes/Bulk/patches" {
		t.Errorf("an acme repo's backup = %+v, want its override's strategy over the laptop's", got)
	}
	if top := cfg.TopLevel(); top.CloneProtocol != "ssh" || top.Backup.ArchiveDir != "/archive" || top.Backup.Strategy != "both" {
		t.Errorf("TopLevel = clone_protocol %q, backup %+v", top.CloneProtocol, top.Backup)
	}

	t.Setenv("GITSTEAD_MACHINE", "desktop")
	if cfg, err = Load(t.Context(), path); err != nil {
		t.Fatal(err)
	}
	if cfg.CloneProtocol != "ssh" || cfg.Backup.ArchiveDir != "/archive" || cfg.Backup.Strategy != "both" {
		t.Errorf("desktop: clone_protocol %q, backup %+v; want the top level's", cfg.CloneProtocol, cfg.Backup)
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
		{
			name: "a transport there is none of",
			yaml: "machines:\n  laptop:\n    clone_protocol: ftp\n",
			want: "machines[laptop].clone_protocol must be one of [ssh https]",
		},
		{
			name: "a backup strategy there is none of",
			yaml: "machines:\n  desktop:\n    backup:\n      strategy: copy\n",
			want: "machines[desktop].backup.strategy must be one of",
		},
		{
			name: "a negative archive retention",
			yaml: "machines:\n  desktop:\n    backup:\n      archive_retention: -1h\n",
			want: "machines[desktop].backup.archive_retention must be >= 0",
		},
		{
			name: "a key backup doesn't take",
			yaml: "machines:\n  desktop:\n    backup:\n      archive: /x\n",
			want: "archive",
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
	t.Run("backup", func(t *testing.T) {
		b := nonZero(reflect.TypeFor[MachineBackup]())
		mb := b.Interface().(MachineBackup)
		got := reflect.ValueOf(mb.over(BackupCfg{}))
		for i := range got.NumField() {
			name := got.Type().Field(i).Name
			set := b.FieldByName(name)
			if !set.IsValid() || set.Kind() != reflect.Pointer {
				t.Errorf("a machine's backup can't set %s", name)
				continue
			}
			if want := set.Elem().Interface(); !reflect.DeepEqual(got.Field(i).Interface(), want) {
				t.Errorf("backup.%s = %v, want %v", name, got.Field(i), want)
			}
			// Checked as the top-level key is, when set.
			field, _ := b.Type().FieldByName(name)
			top := got.Type().Field(i)
			wantRule := strings.TrimPrefix(strings.TrimPrefix(top.Tag.Get("validate"), "required"), ",")
			if wantRule != "" {
				wantRule = "omitempty," + wantRule
			}
			if field.Tag.Get("validate") != wantRule || field.Tag.Get("koanf") != top.Tag.Get("koanf") {
				t.Errorf("backup.%s: koanf %q, validate %q; want %q, %q", name, field.Tag.Get("koanf"), field.Tag.Get("validate"), top.Tag.Get("koanf"), wantRule)
			}
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

// aliasesConfig is a base, a fragment that keys sections by aliases alone,
// read first, and one that names the machines and gives them their aliases.
var aliasesConfig = map[string]string{
	"config.yaml": `
base_dir: /repos
workspaces:
  - name: app
    repos: [github.com/me/app]
`,
	"config.d/a-client.yaml": `
repos:
  - url: git@github.com:acme/api.git
machines:
  Desktop:
    workspaces:
      - name: api
        repos: [github.com/acme/api]
        agents: 3
  laptop:
    clone_protocol: https
`,
	"config.d/home.yaml": `
machines:
  my-studio:
    aliases: [desktop, big]
    base_dir: /fast
    workspaces:
      - name: app
        agents: 6
  my-laptop:
    aliases: [laptop]
`,
}

// A section keyed by an alias is its machine's, whichever file keys it and
// in whichever case: on that machine, chosen by its name or by an alias, the
// sections of both names apply, and only the machine's name is a section.
func TestLoad_MachineAliases(t *testing.T) {
	path := writeConfigDir(t, aliasesConfig)
	tests := []struct {
		machine, section string
		api              bool // client's desktop workspace
		protocol         string
	}{
		{machine: "my-studio", section: "my-studio", api: true},
		{machine: "MY-STUDIO", section: "my-studio", api: true},
		{machine: "desktop", section: "my-studio", api: true},
		{machine: "Big", section: "my-studio", api: true},
		{machine: "laptop", section: "my-laptop", protocol: "https"},
		{machine: "my-laptop", section: "my-laptop", protocol: "https"},
	}
	for _, tt := range tests {
		t.Run(tt.machine, func(t *testing.T) {
			t.Setenv("GITSTEAD_MACHINE", tt.machine)
			cfg, err := Load(t.Context(), path)
			if err != nil {
				t.Fatal(err)
			}
			if name, applied := cfg.MachineSection(); name != tt.section || !applied {
				t.Errorf("MachineSection = %q, %v; want %s applied", name, applied, tt.section)
			}
			if got := cfg.MachineNames(); !slices.Equal(got, []string{"my-laptop", "my-studio"}) {
				t.Errorf("MachineNames = %v, want the machines' names alone", got)
			}
			if api := cfg.FindWorkspace("api"); (api != nil) != tt.api || (api != nil && api.Agents != 3) {
				t.Errorf("api = %+v, want it (with 3 agents): %v", api, tt.api)
			}
			if tt.section == "my-studio" {
				if app := cfg.FindWorkspace("app"); cfg.BaseDir != "/fast" || app == nil || app.Agents != 6 {
					t.Errorf("base_dir %q, app %+v: want my-studio's own section too", cfg.BaseDir, app)
				}
				if got := cfg.Machines["my-studio"].Aliases; !slices.Equal(got, []string{"desktop", "big"}) {
					t.Errorf("aliases = %v", got)
				}
			}
			if cfg.CloneProtocol != tt.protocol && tt.protocol != "" {
				t.Errorf("clone_protocol = %q, want %q from the alias's section", cfg.CloneProtocol, tt.protocol)
			}
		})
	}

	t.Run("a machine with neither name", func(t *testing.T) {
		t.Setenv("GITSTEAD_MACHINE", "build-1")
		cfg, err := Load(t.Context(), path)
		if err != nil {
			t.Fatal(err)
		}
		if name, applied := cfg.MachineSection(); name != "build-1" || applied || cfg.FindWorkspace("api") != nil {
			t.Errorf("MachineSection = %q, %v; want none applied", name, applied)
		}
	})
}

// machine: in the main file may be an alias too.
func TestLoad_MachineAliasInMainFile(t *testing.T) {
	files := maps.Clone(aliasesConfig)
	files["config.yaml"] = "machine: laptop\n" + files["config.yaml"]
	cfg, err := Load(t.Context(), writeConfigDir(t, files))
	if err != nil {
		t.Fatal(err)
	}
	if name, applied := cfg.MachineSection(); name != "my-laptop" || !applied || cfg.CloneProtocol != "https" {
		t.Errorf("MachineSection = %q, %v, clone_protocol %q; want my-laptop's", name, applied, cfg.CloneProtocol)
	}
}

// One file may key a section by an alias it declares: the two sections are
// one, and what both set is set twice.
func TestLoad_MachineAliasesInOneFile(t *testing.T) {
	path := writeMachinesConfig(t, `
base_dir: /repos
machines:
  laptop:
    base_dir: /laptop
  my-laptop:
    aliases: [laptop]
    clone_protocol: https
`)
	t.Setenv("GITSTEAD_MACHINE", "my-laptop")
	cfg, err := Load(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BaseDir != "/laptop" || cfg.CloneProtocol != "https" || !slices.Equal(cfg.MachineNames(), []string{"my-laptop"}) {
		t.Errorf("base_dir %q, clone_protocol %q, sections %v: want one section with both", cfg.BaseDir, cfg.CloneProtocol, cfg.MachineNames())
	}

	path = writeMachinesConfig(t, "machines:\n  laptop:\n    base_dir: /a\n  my-laptop:\n    aliases: [laptop]\n    base_dir: /b\n")
	_, err = Load(t.Context(), path)
	var conflict *ConflictError
	if !errors.As(err, &conflict) || conflict.Key != "machines.my-laptop.base_dir" || conflict.Files != [2]string{path, path} {
		t.Fatalf("Load = %v, want my-laptop's base_dir set twice in %s", err, path)
	}
	if want := "machines.my-laptop.base_dir is set twice in " + path + ", in the machine's section and as machines.laptop, its alias"; !strings.Contains(err.Error(), want) {
		t.Errorf("error %q doesn't say %q", err, want)
	}
}

// A conflict through an alias's section names the alias beside its file.
func TestLoad_MachineAliasConflictNamesTheAlias(t *testing.T) {
	path := writeConfigDir(t, map[string]string{
		"config.d/a.yaml": "machines:\n  box:\n    base_dir: /a\n",
		"config.d/b.yaml": "machines:\n  desktop:\n    aliases: [box]\n    base_dir: /b\n",
	})
	_, err := Load(t.Context(), path, WithOptionalFile())
	dir := filepath.Join(filepath.Dir(path), "config.d")
	want := "machines.desktop.base_dir is set in both " + filepath.Join(dir, "a.yaml") + " (as machines.box) and " + filepath.Join(dir, "b.yaml") + ": each setting may be set in only one file"
	if err == nil || err.Error() != want {
		t.Errorf("Load = %v\nwant %s", err, want)
	}
}

// An alias is a name a section could have, of one machine, not its own
// section's, and not of a section that is an alias itself; the error names
// the file that declares it.
func TestLoad_MachineAliasErrors(t *testing.T) {
	tests := []struct {
		name, main, fragment string
		inFragment           bool
		want                 string
	}{
		{
			name: "a name that isn't one",
			main: "machines:\n  desktop:\n    aliases: [\"my mac\"]\n",
			want: `machines.desktop.aliases: "my mac" can't name a machine`,
		},
		{
			name: "a name with a dot",
			main: "machines:\n  desktop:\n    aliases: [box.local]\n",
			want: `machines.desktop.aliases: "box.local" can't name a machine`,
		},
		{
			name: "the section's own name",
			main: "machines:\n  desktop:\n    aliases: [Desktop]\n",
			want: `machines.desktop.aliases: "Desktop" is the section's own name`,
		},
		{
			name: "two machines, one file",
			main: "machines:\n  a:\n    aliases: [box]\n  b:\n    aliases: [box]\n",
			want: `machines.b.aliases: "box" is already machines.a's alias, in `,
		},
		{
			name:       "two machines, two files",
			main:       "machines:\n  a:\n    aliases: [box]\n",
			fragment:   "machines:\n  b:\n    aliases: [BOX]\n",
			inFragment: true,
			want:       `machines.b.aliases: "BOX" is already machines.a's alias, in `,
		},
		{
			name: "an alias's own aliases",
			main: "machines:\n  a:\n    aliases: [b]\n  b:\n    aliases: [c]\n",
			want: "machines.b.aliases: b is an alias of machines.a, in ",
		},
		{
			name:       "an alias's own aliases, in another file",
			main:       "machines:\n  a:\n    aliases: [b]\n",
			fragment:   "machines:\n  B:\n    aliases: [c]\n",
			inFragment: true,
			want:       "machines.B.aliases: B is an alias of machines.a, in ",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			files := map[string]string{"config.yaml": tt.main}
			if tt.fragment != "" {
				files["config.d/b.yaml"] = tt.fragment
			}
			path := writeConfigDir(t, files)
			in := path
			if tt.inFragment {
				in = filepath.Join(filepath.Dir(path), "config.d", "b.yaml")
			}
			_, err := Load(t.Context(), path)
			if err == nil || !strings.HasPrefix(err.Error(), "config "+in+" is invalid: ") || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Load = %v, want %s's error saying %q", err, in, tt.want)
			}
		})
	}
}

// A write to the config file keeps its machine aliases, and is checked with
// the sections other files key by them.
func TestDocument_KeepsMachineAliases(t *testing.T) {
	path := writeConfigDir(t, map[string]string{
		"config.yaml":          "base_dir: /repos\nmachines:\n  my-studio:\n    aliases: [desktop] # what the client's file calls it\n    base_dir: /fast\n",
		"config.d/client.yaml": "machines:\n  desktop:\n    workspace_defaults:\n      agents: 4\n",
	})
	doc, err := OpenDocument(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := doc.Set("sync.retries", 4); err != nil {
		t.Fatal(err)
	}
	if err := doc.Save(t.Context()); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "aliases: [desktop] # what the client's file calls it") {
		t.Errorf("config.yaml lost its aliases:\n%s", data)
	}
	t.Setenv("GITSTEAD_MACHINE", "desktop")
	cfg, err := Load(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Sync.Retries != 4 || cfg.BaseDir != "/fast" || cfg.WorkspaceDefaults.Agents != 4 {
		t.Errorf("retries %d, base_dir %q, agents %d: want the write and both sections", cfg.Sync.Retries, cfg.BaseDir, cfg.WorkspaceDefaults.Agents)
	}

	// A write that would set what the alias's section sets is refused before
	// anything is written.
	if err := doc.Set("machines.my-studio.workspace_defaults.agents", 2); err != nil {
		t.Fatal(err)
	}
	var conflict *ConflictError
	if err := doc.Save(t.Context()); !errors.As(err, &conflict) || conflict.Key != "machines.my-studio.workspace_defaults.agents" || conflict.Aliases[1] != "desktop" {
		t.Errorf("Save = %v, want the conflict with the alias's section", err)
	}
	if after, _ := os.ReadFile(path); string(after) != string(data) {
		t.Errorf("a refused Save wrote:\n%s", after)
	}
}
