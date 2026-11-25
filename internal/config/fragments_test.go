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

	"github.com/knadh/koanf/parsers/yaml"
)

// writeConfigDir writes files, by path relative to a new config directory,
// and returns the main config file's path in it.
func writeConfigDir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(dir, "config.yaml")
}

// The fragments in config.d are read with the main file: a repo a fragment
// lists is tracked, and a setting only a fragment sets applies.
func TestLoad_ReadsFragments(t *testing.T) {
	path := writeConfigDir(t, map[string]string{
		"config.yaml":          "repos:\n  - url: git@github.com:me/base.git\n",
		"config.d/client.yaml": "base_dir: /client\nrepos:\n  - url: git@github.com:client/app.git\n",
	})
	cfg, err := Load(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BaseDir != "/client" {
		t.Errorf("base_dir = %q, want the fragment's /client", cfg.BaseDir)
	}
	if len(cfg.Repos) != 2 {
		t.Errorf("repos = %+v, want the main file's and the fragment's", cfg.Repos)
	}
}

const (
	fragmentsBase = `
base_dir: /repos
sync:
  update: ff-only-clean
accounts:
  - name: personal
    hosts: [github.com]
    owners: [me]
repos:
  - url: git@github.com:me/base.git
workspace_defaults:
  env: {SHARED: "1"}
`
	// work names the personal account the main file defines.
	fragmentWork = `
accounts:
  - name: work
    hosts: [github.com]
    owners: [acme]
repos:
  - url: git@github.com:acme/api.git
  - url: git@github.com:me/notes.git
    account: personal
sync:
  retries: 5
workspace_defaults:
  env: {WORK: "1"}
workspaces:
  - name: api
    repos: [github.com/acme/api]
machines:
  desktop:
    base_dir: /Volumes/Fast/repos
    repos:
      - url: git@github.com:acme/infra.git
`
	fragmentHome = `
repos:
  - url: git@github.com:me/blog.git
machines:
  Desktop:
    workspace_defaults:
      agents: 4
    workspaces:
      - name: blog
        repos: [github.com/me/blog]
  laptop:
    base_dir: ~/code
`
)

// The main file and two fragments merge: lists in file order, the main
// file's entries first; named entries by name; a section's keys from
// whichever file sets each; one machine's sections from several files, its
// name matched ignoring case and spelled as the first file spells it; and a
// fragment may name an account another file defines.
func TestLoad_MergesFragments(t *testing.T) {
	path := writeConfigDir(t, map[string]string{
		"config.yaml":        fragmentsBase,
		"config.d/work.yaml": fragmentWork,
		"config.d/home.yml":  fragmentHome,
	})
	t.Setenv("GITSTEAD_MACHINE", "desktop")
	cfg, err := Load(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(path)
	wantFiles := []string{path, filepath.Join(dir, "config.d", "home.yml"), filepath.Join(dir, "config.d", "work.yaml")}
	if !slices.Equal(cfg.Files(), wantFiles) {
		t.Errorf("Files = %v, want %v", cfg.Files(), wantFiles)
	}
	var urls []string
	for _, r := range cfg.Repos {
		urls = append(urls, r.URL)
	}
	wantURLs := []string{
		"git@github.com:me/base.git", "git@github.com:me/blog.git",
		"git@github.com:acme/api.git", "git@github.com:me/notes.git",
		"git@github.com:acme/infra.git", // the machine's, after the top level's
	}
	if !slices.Equal(urls, wantURLs) {
		t.Errorf("repos = %v, want %v", urls, wantURLs)
	}
	if len(cfg.Accounts) != 2 || cfg.Accounts[0].Name != "personal" || cfg.Accounts[1].Name != "work" {
		t.Errorf("accounts = %+v, want personal then work", cfg.Accounts)
	}
	if cfg.Sync.Update != "ff-only-clean" || cfg.Sync.Retries != 5 {
		t.Errorf("sync = %+v, want update from config.yaml and retries from work.yaml", cfg.Sync)
	}
	if env := cfg.WorkspaceDefaults.Env; env["SHARED"] != "1" || env["WORK"] != "1" {
		t.Errorf("workspace_defaults.env = %v, want both files' names", env)
	}
	// home.yml, read first, spells the section Desktop.
	if name, applied := cfg.MachineSection(); name != "Desktop" || !applied {
		t.Errorf("MachineSection = %q, %v; want Desktop applied", name, applied)
	}
	if cfg.BaseDir != "/Volumes/Fast/repos" || cfg.WorkspaceDefaults.Agents != 4 {
		t.Errorf("base_dir %q, agents %d: want desktop's section from both fragments", cfg.BaseDir, cfg.WorkspaceDefaults.Agents)
	}
	if cfg.FindWorkspace("api") == nil || cfg.FindWorkspace("blog") == nil {
		t.Errorf("workspaces = %+v, want api and desktop's blog", cfg.Workspaces)
	}
	if got := cfg.MachineNames(); !slices.Equal(got, []string{"Desktop", "laptop"}) {
		t.Errorf("MachineNames = %v, want one desktop section", got)
	}
}

// A setting or named entry two files set fails the load, naming the
// setting and both files, whatever the two files are.
func TestLoad_FragmentConflicts(t *testing.T) {
	tests := []struct {
		name       string
		main, a, b string
		key        string
		first      string // the file named first: config.yaml, a.yaml or b.yaml
	}{
		{
			name: "a setting in the main file and a fragment",
			main: "base_dir: /one\n", b: "base_dir: /two\n",
			key: "base_dir", first: "config.yaml",
		},
		{
			name: "a section's key in two fragments",
			a:    "sync:\n  update: off\n", b: "sync:\n  enabled: false\n  update: ff-only-clean\n",
			key: "sync.update", first: "a.yaml",
		},
		{
			name: "a duration, set the same in both",
			a:    "backup:\n  archive_retention: 720h\n", b: "backup:\n  archive_retention: 720h\n",
			key: "backup.archive_retention", first: "a.yaml",
		},
		{
			name: "a list that is one setting's value",
			main: "hooks:\n  post_clone: [direnv allow]\n", a: "hooks:\n  post_clone: [npm ci]\n",
			key: "hooks.post_clone", first: "config.yaml",
		},
		{
			name: "a named entry",
			a:    "accounts:\n  - {name: work, hosts: [github.com]}\n",
			b:    "accounts:\n  - {name: work, hosts: [gitlab.com]}\n",
			key:  `accounts entry "work"`, first: "a.yaml",
		},
		{
			name: "a discovery rule's name",
			main: "providers:\n  - {name: gh, type: github}\ndiscovery:\n  - {name: mine, provider: gh}\n",
			a:    "discovery:\n  - {name: mine, provider: gh, org: acme}\n",
			key:  `discovery entry "mine"`, first: "config.yaml",
		},
		{
			name: "a map's entry",
			main: "workspace_defaults:\n  env: {EDITOR: vi}\n", b: "workspace_defaults:\n  env: {EDITOR: nano}\n",
			key: "workspace_defaults.env.EDITOR", first: "config.yaml",
		},
		{
			name: "a launcher plugin",
			a:    "launchers:\n  plugins:\n    wezterm: {cmd: a}\n", b: "launchers:\n  plugins:\n    wezterm: {cmd: b}\n",
			key: "launchers.plugins.wezterm", first: "a.yaml",
		},
		{
			name: "a machine section's setting, its name in another case",
			a:    "machines:\n  desktop:\n    base_dir: /a\n", b: "machines:\n  Desktop:\n    base_dir: /b\n",
			key: "machines.desktop.base_dir", first: "a.yaml",
		},
		{
			name: "a machine section's named entry",
			main: "machines:\n  desktop:\n    workspaces:\n      - {name: app, agents: 2}\n",
			b:    "machines:\n  desktop:\n    workspaces:\n      - {name: app, agents: 6}\n",
			key:  `machines.desktop.workspaces entry "app"`, first: "config.yaml",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			files := map[string]string{}
			for name, content := range map[string]string{"config.yaml": tt.main, "config.d/a.yaml": tt.a, "config.d/b.yaml": tt.b} {
				if content != "" {
					files[name] = content
				}
			}
			path := writeConfigDir(t, files)
			_, err := Load(t.Context(), path, WithOptionalFile())
			var conflict *ConflictError
			if !errors.As(err, &conflict) {
				t.Fatalf("Load = %v, want a ConflictError", err)
			}
			if conflict.Key != tt.key {
				t.Errorf("Key = %q, want %q", conflict.Key, tt.key)
			}
			if got := filepath.Base(conflict.Files[0]); got != tt.first {
				t.Errorf("Files = %v, want %s first", conflict.Files, tt.first)
			}
			if conflict.Files[0] == conflict.Files[1] {
				t.Errorf("Files = %v, want two files", conflict.Files)
			}
			msg := err.Error()
			for _, want := range []string{tt.key, conflict.Files[0], conflict.Files[1], "each setting may be set in only one file"} {
				if !strings.Contains(msg, want) {
					t.Errorf("error %q doesn't say %q", msg, want)
				}
			}
		})
	}
}

// A problem one file has on its own is reported in that file: a key no
// setting takes, with the setting it likely misspells; a value of the wrong
// type; YAML that doesn't parse; and machine:, which a fragment every
// machine reads can't set.
func TestLoad_FragmentErrorsNameTheFragment(t *testing.T) {
	tests := []struct {
		name, fragment string
		want           []string
	}{
		{"an unknown key", "sync:\n  timout_per_repo: 1m\n", []string{`"sync.timout_per_repo" is not a setting (did you mean "sync.timeout_per_repo"?)`}},
		{"a value of the wrong type", "concurrency: lots\n", []string{"concurrency"}},
		{"YAML that doesn't parse", "repos: [\n", []string{"does not parse: yaml: line "}},
		{"the machine selector", "machine: desktop\n", []string{`"machine" can't be set in a fragment`, "GITSTEAD_MACHINE", "--machine"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeConfigDir(t, map[string]string{
				"config.yaml":          "base_dir: /repos\n",
				"config.d/a.yaml":      "repos: []\n",
				"config.d/broken.yaml": tt.fragment,
			})
			_, err := Load(t.Context(), path)
			fragment := filepath.Join(filepath.Dir(path), "config.d", "broken.yaml")
			if err == nil || !strings.HasPrefix(err.Error(), "config "+fragment) {
				t.Fatalf("Load = %v, want an error naming %s alone", err, fragment)
			}
			for _, want := range tt.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q doesn't say %q", err, want)
				}
			}
		})
	}
}

// machine: stays a setting the main file may hold.
func TestLoad_MachineInMainFile(t *testing.T) {
	path := writeConfigDir(t, map[string]string{
		"config.yaml":     "machine: laptop\n",
		"config.d/a.yaml": "machines:\n  laptop:\n    base_dir: /laptop\n",
	})
	cfg, err := Load(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if name, applied := cfg.MachineSection(); name != "laptop" || !applied || cfg.BaseDir != "/laptop" {
		t.Errorf("MachineSection = %q, %v, base_dir %q; want laptop's section", name, applied, cfg.BaseDir)
	}
}

// Semantic validation runs on the merged config, naming every file read.
func TestLoad_InvalidMergeNamesEveryFile(t *testing.T) {
	path := writeConfigDir(t, map[string]string{
		"config.yaml":     "base_dir: /repos\n",
		"config.d/a.yaml": "repos:\n  - url: git@github.com:me/app.git\n    account: nobody\n",
	})
	_, err := Load(t.Context(), path)
	fragment := filepath.Join(filepath.Dir(path), "config.d", "a.yaml")
	if err == nil || !strings.HasPrefix(err.Error(), "config "+path+", "+fragment+" is invalid: ") || !strings.Contains(err.Error(), `unknown account "nobody"`) {
		t.Fatalf("Load = %v, want the unknown account, naming both files", err)
	}
}

// With no main file, the fragments are the config, when the main file may
// be missing; a config file named explicitly must still exist.
func TestLoad_FragmentsOnly(t *testing.T) {
	path := writeConfigDir(t, map[string]string{
		"config.d/a.yaml": "base_dir: /a\n",
	})
	cfg, err := Load(t.Context(), path, WithOptionalFile())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BaseDir != "/a" || len(cfg.Files()) != 1 || filepath.Base(cfg.Files()[0]) != "a.yaml" {
		t.Errorf("base_dir %q, files %v: want the fragment alone", cfg.BaseDir, cfg.Files())
	}
	if _, err := Load(t.Context(), path); !errors.Is(err, ErrNotFound) {
		t.Errorf("Load of a named, missing file = %v, want ErrNotFound", err)
	}
	files, err := FilesFor(path)
	if err != nil || len(files) != 1 {
		t.Errorf("FilesFor = %v, %v; want the fragment", files, err)
	}
}

// A config file anywhere reads the config.d beside it, not the default
// one's.
func TestLoad_FragmentsBesideTheConfigInUse(t *testing.T) {
	defaultPath := writeConfigDir(t, map[string]string{
		"config.yaml":     "base_dir: /default\n",
		"config.d/a.yaml": "concurrency: 2\n",
	})
	t.Setenv("XDG_CONFIG_HOME", filepath.Dir(filepath.Dir(defaultPath)))
	other := writeConfigDir(t, map[string]string{
		"config.yaml":     "base_dir: /other\n",
		"config.d/b.yaml": "concurrency: 3\n",
	})
	cfg, err := Load(t.Context(), other)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BaseDir != "/other" || cfg.Concurrency != 3 {
		t.Errorf("base_dir %q, concurrency %d: want the other config's own fragment", cfg.BaseDir, cfg.Concurrency)
	}
}

// Fragments are *.yaml and *.yml files, read in byte order of their names;
// hidden files and anything else in config.d are left alone, and links are
// followed, to a fragment and to config.d itself.
func TestLoad_WhichFragments(t *testing.T) {
	elsewhere := t.TempDir()
	if err := os.WriteFile(filepath.Join(elsewhere, "linked.yaml"), []byte("repos:\n  - url: git@github.com:me/linked.git\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := writeConfigDir(t, map[string]string{
		"config.yaml":           "base_dir: /repos\n",
		"config.d/B.yaml":       "repos:\n  - url: git@github.com:me/upper.git\n",
		"config.d/a.yml":        "repos:\n  - url: git@github.com:me/lower.git\n",
		"config.d/.hidden.yaml": "base_dir: /hidden\n",
		"config.d/notes.txt":    "base_dir: /notes\n",
		"config.d/a.yaml.bak":   "base_dir: /backup\n",
		"config.d/sub/c.yaml":   "base_dir: /nested\n",
	})
	dir := filepath.Join(filepath.Dir(path), "config.d")
	if err := os.Symlink(filepath.Join(elsewhere, "linked.yaml"), filepath.Join(dir, "c.yaml")); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range cfg.Files()[1:] {
		got = append(got, filepath.Base(f))
	}
	if want := []string{"B.yaml", "a.yml", "c.yaml"}; !slices.Equal(got, want) {
		t.Errorf("fragments = %v, want %v", got, want)
	}
	if cfg.BaseDir != "/repos" || len(cfg.Repos) != 3 || cfg.Repos[2].URL != "git@github.com:me/linked.git" {
		t.Errorf("base_dir %q, repos %+v", cfg.BaseDir, cfg.Repos)
	}

	// config.d itself a link, as an overlay may make it.
	linkedDir := writeConfigDir(t, map[string]string{"config.yaml": "base_dir: /repos\n"})
	if err := os.Symlink(dir, filepath.Join(filepath.Dir(linkedDir), "config.d")); err != nil {
		t.Fatal(err)
	}
	if cfg, err := Load(t.Context(), linkedDir); err != nil || len(cfg.Files()) != 4 {
		t.Errorf("through a linked config.d: %v, %v", cfg.Files(), err)
	}
}

// What can't be read is an error, never a config without it: a broken link
// to a fragment or to config.d, a config.d that isn't a directory or can't be
// listed, and a fragment that isn't a file.
func TestLoad_UnreadableFragments(t *testing.T) {
	tests := []struct {
		name string
		make func(t *testing.T, dir string)
		// listed: FilesFor still lists config.d, which it reads only by name.
		listed bool
	}{
		{"a broken link to a fragment", func(t *testing.T, dir string) {
			mkdir(t, filepath.Join(dir, "config.d"))
			symlink(t, filepath.Join(dir, "gone.yaml"), filepath.Join(dir, "config.d", "a.yaml"))
		}, true},
		{"a fragment that is a directory", func(t *testing.T, dir string) {
			mkdir(t, filepath.Join(dir, "config.d", "a.yaml"))
		}, true},
		{"a broken link to config.d", func(t *testing.T, dir string) {
			symlink(t, filepath.Join(dir, "gone"), filepath.Join(dir, "config.d"))
		}, false},
		{"config.d a file", func(t *testing.T, dir string) {
			if err := os.WriteFile(filepath.Join(dir, "config.d"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}, false},
		{"config.d unlistable", func(t *testing.T, dir string) {
			if os.Geteuid() == 0 {
				t.Skip("root lists any directory")
			}
			d := filepath.Join(dir, "config.d")
			mkdir(t, d)
			if err := os.Chmod(d, 0o000); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.Chmod(d, 0o700) })
		}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeConfigDir(t, map[string]string{"config.yaml": "base_dir: /repos\n"})
			tt.make(t, filepath.Dir(path))
			if _, err := Load(t.Context(), path); err == nil || !strings.Contains(err.Error(), "config.d") {
				t.Errorf("Load = %v, want an error naming config.d", err)
			}
			if _, err := OpenDocument(path); err == nil {
				t.Error("OpenDocument read a config whose fragments it can't")
			}
			if _, err := FilesFor(path); (err == nil) != tt.listed {
				t.Errorf("FilesFor = %v, want an error: %v", err, !tt.listed)
			}
		})
	}
}

func mkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
}

func symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

// A Document writes the main file alone: a change to a setting or entry a
// fragment sets is refused, naming the fragment, unless the fragment has it
// so already; what no fragment sets is written as ever, an entry appended to
// a list a fragment adds to included. What only the merge can see is
// refused before anything is written.
func TestDocument_FragmentWrites(t *testing.T) {
	path := writeConfigDir(t, map[string]string{
		"config.yaml": "base_dir: /repos # where they go\n",
		"config.d/work.yaml": `
sync:
  update: ff-only-clean
workspaces:
  - name: api
    repos: [github.com/acme/api]
repos:
  - url: git@github.com:acme/api.git
machines:
  Desktop:
    base_dir: /fast
`,
	})
	fragment := filepath.Join(filepath.Dir(path), "config.d", "work.yaml")
	ok := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	refused := func(what string, err error) {
		t.Helper()
		var fe *FragmentError
		if !errors.As(err, &fe) || fe.What != what || fe.Fragment != fragment || fe.Config != path {
			t.Errorf("got %v, want %s refused as %s's", err, what, fragment)
		}
	}
	doc, err := OpenDocument(path)
	ok(err)
	refused("sync.update", doc.Set("sync.update", "off"))
	ok(doc.Set("sync.update", "ff-only-clean")) // the fragment's value: nothing to write
	refused("sync.update", doc.Delete("sync.update"))
	refused(`workspaces entry "api"`, doc.Upsert("workspaces", Named("api"), Workspace{Name: "api", Repos: []string{"github.com/acme/web"}}))
	refused(`repos entry "git@github.com:acme/api.git"`, doc.Remove("repos", MatchesRepo("github.com/acme/api")))
	ok(doc.Set("sync.retries", 4))
	ok(doc.Upsert("workspaces", Named("web"), Workspace{Name: "web", Repos: []string{"github.com/acme/web"}}))
	ok(doc.Upsert("repos", MatchesRepo("github.com/acme/web"), ManualRepo{URL: "git@github.com:acme/web.git"}))
	ok(doc.Save(t.Context()))

	data, err := os.ReadFile(path)
	ok(err)
	if got := string(data); strings.Contains(got, "update") || strings.Contains(got, "acme/api") || !strings.Contains(got, "# where they go") {
		t.Errorf("config.yaml holds what the fragment sets, or lost its comment:\n%s", got)
	}
	cfg, err := Load(t.Context(), path)
	ok(err)
	if cfg.Sync.Retries != 4 || cfg.Sync.Update != "ff-only-clean" || len(cfg.Workspaces) != 2 || len(cfg.Repos) != 2 {
		t.Errorf("sync %+v, workspaces %+v, repos %+v", cfg.Sync, cfg.Workspaces, cfg.Repos)
	}

	refused(`workspaces entry "api"`, SaveWorkspace(t.Context(), path, Workspace{Name: "api", Repos: []string{"/x"}}, true))

	// The fragment spells the machine Desktop; the section here would set its
	// base_dir again.
	ok(doc.Set("machines.desktop.base_dir", "/slow"))
	var conflict *ConflictError
	if err := doc.Save(t.Context()); !errors.As(err, &conflict) || conflict.Files != [2]string{path, fragment} {
		t.Errorf("Save = %v, want the conflict with the fragment", err)
	}
	if after, _ := os.ReadFile(path); string(after) != string(data) {
		t.Errorf("a refused Save wrote:\n%s", after)
	}
}

// One file merges into the tree it is: a config without fragments loads as
// it always has.
func TestMergeSources_OneFileIsItself(t *testing.T) {
	for _, content := range []string{machinesYAML, fragmentWork, fragmentHome, "sync:\nrepos:\nmachines:\n  a:\n"} {
		tree, err := yaml.Parser().Unmarshal([]byte(content))
		if err != nil {
			t.Fatal(err)
		}
		merged, err := mergeSources([]source{{path: "config.yaml", tree: tree}})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(merged, tree) {
			t.Errorf("merged = %v\nwant %v", merged, tree)
		}
	}
}

// Every list and map a config file can hold merges as the docs say: a key
// added to Config or Machine whose merge isn't listed here fails, so its
// rule is chosen, and documented, on purpose. Every other key is a section,
// merged key by key, or a setting one file sets.
func TestMergeKinds(t *testing.T) {
	want := map[string]mergeKind{
		"accounts": mergeByName, "providers": mergeByName, "discovery": mergeByName,
		"sets": mergeByName, "agents": mergeByName, "workspaces": mergeByName, "fleets": mergeByName,
		"repos": mergeAppend, "exclude": mergeAppend, "overrides": mergeAppend,
		"workspace_defaults.env": mergeEntries, "launchers.plugins": mergeEntries,
		"machines":             mergeMachines,
		"machines.*.overrides": mergeAppend, "machines.*.repos": mergeAppend,
		"machines.*.discovery": mergeByName, "machines.*.workspaces": mergeByName, "machines.*.fleets": mergeByName,
		"machines.*.workspace_defaults.env": mergeEntries,
	}
	got := map[string]mergeKind{}
	var walk func(t reflect.Type, at string)
	walk = func(t reflect.Type, at string) {
		for i := range t.NumField() {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			key, _, _ := strings.Cut(f.Tag.Get("koanf"), ",")
			path := joinKey(at, key)
			switch kind := kindOf(f.Type); kind {
			case mergeFields:
				walk(derefType(f.Type), path)
			case mergeMachines:
				got[path] = kind
				walk(reflect.TypeFor[Machine](), path+".*")
			case mergeOnce:
			default:
				got[path] = kind
			}
		}
	}
	walk(reflect.TypeFor[Config](), "")
	for _, k := range slices.Sorted(maps.Keys(got)) {
		if w, ok := want[k]; !ok || w != got[k] {
			t.Errorf("%s merges as %d, want %d (listed: %v)", k, got[k], w, ok)
		}
	}
	for k := range want {
		if _, ok := got[k]; !ok {
			t.Errorf("%s is listed but Config has no such list or map", k)
		}
	}
}
