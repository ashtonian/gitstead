package launch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ashtonian/gitten/internal/config"
	"github.com/ashtonian/gitten/internal/state"
)

func launchers() config.LaunchersConfig {
	return config.LaunchersConfig{
		Default: "auto", Title: "{{.Workspace}}/{{.Agent}}", Check: true, KeepShell: true,
		Tmux: config.TmuxConfig{Session: "gitten-{{.Fleet}}", WindowPer: "workspace", Layout: "tiled", MaxPanes: 2,
			PaneTitles: true, ControlMode: "auto", OnExists: "add-missing", RemainOnExit: true},
		ITerm: config.ITermConfig{Window: "new", TabPer: "workspace", Split: "grid", MaxPanes: 4, Profile: "Default",
			Badge: "{{.Agent}}", Focus: true, TypeDelay: 150 * time.Millisecond},
		Muxy: config.MuxyConfig{ProjectName: "{{.Workspace}}", Open: "layout", Restart: "ask", Color: "auto"},
	}
}

func testPlan() Plan {
	agents := func(ws string, n int) []Agent {
		var out []Agent
		for i := 1; i <= n; i++ {
			name := fmt.Sprintf("agent-%d", i)
			out = append(out, Agent{Name: name, Dir: "/ws/" + ws + "/" + name, Title: ws + "/" + name, Profile: "claude",
				Command: "gitten ws run " + ws + " " + name, State: "ready"})
		}
		return out
	}
	return Plan{Fleet: "daily", Workspaces: []Workspace{
		{Name: "app", Dir: "/ws/app", Agents: agents("app", 3)},
		{Name: "lib", Dir: "/ws/lib", Agents: agents("lib", 1)},
	}}
}

func TestBuild(t *testing.T) {
	cfg := &config.Config{
		Agents:            []config.AgentProfile{{Name: "claude", Command: "claude"}, {Name: "codex", Command: "codex"}},
		WorkspaceDefaults: config.WorkspaceDefaults{Agent: "codex"},
		Workspaces:        []config.Workspace{{Name: "app", Repos: []string{"x"}, Agent: "claude"}},
		Launchers:         launchers(),
	}
	cfg.Launchers.Check = false
	st, err := state.Load(filepath.Join(t.TempDir(), "state.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	st.Workspaces["app"] = &state.WorkspaceState{Name: "app", Dir: "/ws/app", Replicas: []state.ReplicaState{{Agent: "agent-1", Path: "/ws/app/agent-1"}}}
	st.Workspaces["lib"] = &state.WorkspaceState{Name: "lib", Dir: "/ws/lib", Replicas: []state.ReplicaState{{Agent: "agent-1", Path: "/ws/lib/agent-1"}}}
	self := Invocation{Bin: "/bin/git mgr", Config: "/c.yaml"}

	p, err := Build(cfg, st, Request{Fleet: "daily", Workspaces: []string{"app", "lib"}, Fresh: true, Self: self})
	if err != nil {
		t.Fatal(err)
	}
	app, lib := p.Workspaces[0].Agents[0], p.Workspaces[1].Agents[0]
	if app.Profile != "claude" || lib.Profile != "codex" {
		t.Errorf("profiles = %s, %s; want the workspace's, then workspace_defaults'", app.Profile, lib.Profile)
	}
	if want := `'/bin/git mgr' --config /c.yaml ws run app agent-1 --profile claude --fresh --no-check`; app.Command != want {
		t.Errorf("command = %s\nwant      %s", app.Command, want)
	}
	if app.Title != "app/agent-1" || app.State != "ready" || app.Dir != "/ws/app/agent-1" {
		t.Errorf("agent = %+v", app)
	}

	p, err = Build(cfg, st, Request{Fleet: "x", Workspaces: []string{"app"}, Agent: "codex", Self: self})
	if err != nil || p.Workspaces[0].Agents[0].Profile != "codex" {
		t.Errorf("a fleet's agent overrides the workspace's: %+v, %v", p, err)
	}
	if _, err := Build(cfg, st, Request{Workspaces: []string{"nope"}, Self: self}); err == nil {
		t.Error("planning a workspace that isn't created succeeded")
	}
}

// fakeTmux is a tmux session in memory.
type fakeTmux struct {
	calls   []string
	windows map[string]string // id -> name
	panes   []string          // window id \t agent
	exists  bool
}

func (f *fakeTmux) run(_ context.Context, _ string, args ...string) (string, error) {
	f.calls = append(f.calls, strings.Join(args, " "))
	argAfter := func(flag string) string {
		if i := slices.Index(args, flag); i >= 0 && i+1 < len(args) {
			return args[i+1]
		}
		return ""
	}
	switch args[0] {
	case "has-session":
		if !f.exists {
			return "", errors.New("no session")
		}
	case "new-session", "new-window":
		f.exists = true
		id := fmt.Sprintf("@%d", len(f.windows)+1)
		f.windows[id] = argAfter("-n")
		f.panes = append(f.panes, id+"\t")
		return fmt.Sprintf("%s\t%%%d", id, len(f.panes)), nil
	case "split-window":
		id := argAfter("-t")
		f.panes = append(f.panes, id+"\t")
		return fmt.Sprintf("%s\t%%%d", id, len(f.panes)), nil
	case "set-option":
		if args[1] == "-p" && args[4] == agentOption {
			var n int
			fmt.Sscanf(args[3], "%%%d", &n)
			w, _, _ := strings.Cut(f.panes[n-1], "\t")
			f.panes[n-1] = w + "\t" + args[5]
		}
	case "list-panes":
		var out []string
		for _, p := range f.panes {
			w, agent, _ := strings.Cut(p, "\t")
			out = append(out, w+"\t"+f.windows[w]+"\t"+agent)
		}
		return strings.Join(out, "\n"), nil
	}
	return "", nil
}

func (f *fakeTmux) count(prefix string) int {
	n := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func TestTmux_BuildsAndAddsWhatIsMissing(t *testing.T) {
	fake := &fakeTmux{windows: map[string]string{}}
	var attached []string
	tm := &Tmux{Opts: launchers().Tmux, run: fake.run, Env: Env{Getenv: func(string) string { return "" },
		Exec: func(argv []string) error { attached = argv; return nil }}}

	if err := tm.Open(context.Background(), testPlan()); err != nil {
		t.Fatal(err)
	}
	// app's three agents spill past two panes into app-2; lib gets its own.
	names := map[string]bool{}
	for _, n := range fake.windows {
		names[n] = true
	}
	if len(fake.windows) != 3 || !names["app"] || !names["app-2"] || !names["lib"] {
		t.Errorf("windows = %v", fake.windows)
	}
	if fake.count("new-session -d -s gitten-daily") != 1 || fake.count("split-window") != 1 {
		t.Errorf("calls:\n%s", strings.Join(fake.calls, "\n"))
	}
	if !slices.Contains(fake.calls, "select-pane -t %1 -T app/agent-1") {
		t.Error("panes aren't titled")
	}
	if want := []string{"tmux", "attach-session", "-t", "=gitten-daily"}; !slices.Equal(attached, want) {
		t.Errorf("attach = %v", attached)
	}

	// Opening again adds only the agent that wasn't there.
	more := testPlan()
	more.Workspaces[1].Agents = append(more.Workspaces[1].Agents, Agent{Name: "agent-2", Dir: "/ws/lib/agent-2", Command: "run lib 2", Title: "lib/agent-2"})
	fake.calls = nil
	if err := tm.Open(context.Background(), more); err != nil {
		t.Fatal(err)
	}
	if fake.count("new-session") != 0 || fake.count("new-window") != 0 || fake.count("split-window -t @3") != 1 {
		t.Errorf("reopening should only split lib's window:\n%s", strings.Join(fake.calls, "\n"))
	}

	// recreate kills and rebuilds; attach only attaches.
	tm.Opts.OnExists = "recreate"
	fake.calls = nil
	_ = tm.Open(context.Background(), testPlan())
	if fake.count("kill-session") != 1 {
		t.Error("recreate didn't kill the session")
	}
	tm.Opts.OnExists = "attach"
	fake.calls = nil
	_ = tm.Open(context.Background(), testPlan())
	if len(fake.calls) != 1 {
		t.Errorf("attach made calls beyond has-session: %v", fake.calls)
	}
}

func TestTmux_ControlModeAndRemote(t *testing.T) {
	fake := &fakeTmux{windows: map[string]string{}}
	var attached []string
	tm := &Tmux{Opts: launchers().Tmux, run: fake.run, Env: Env{
		Getenv: func(k string) string { return map[string]string{"TERM_PROGRAM": "iTerm.app"}[k] },
		Exec:   func(argv []string) error { attached = argv; return nil }}}
	p := testPlan()
	p.Host = "me@buildbox"
	if err := tm.Open(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	if want := []string{"ssh", "-t", "me@buildbox", "tmux -CC attach-session -t '=gitten-daily'"}; !slices.Equal(attached, want) {
		t.Errorf("attach = %q", attached)
	}
	out, err := tm.Print(p)
	if err != nil || !strings.Contains(out, "ssh me@buildbox 'tmux new-session") {
		t.Errorf("Print = %s, %v", out, err)
	}
	tm.Opts.Socket = "agents"
	if out, err := tm.Print(testPlan()); err != nil || !strings.Contains(out, "tmux -L agents split-window") {
		t.Errorf("Print on a socket = %s, %v", out, err)
	}
}

func TestGridSplits(t *testing.T) {
	for _, tt := range []struct {
		n      int
		layout string
		want   string
	}{
		{1, "grid", ""},
		{2, "grid", "1v"},
		{3, "grid", "1v 1h"},
		{4, "grid", "1v 1h 2h"},
		{5, "grid", "1v 2v 1h 2h"},
		{3, "columns", "1v 2v"},
		{3, "rows", "1h 2h"},
	} {
		var got []string
		for _, s := range gridSplits(tt.n, tt.layout) {
			got = append(got, fmt.Sprintf("%d%c", s.from, s.direction[0]))
		}
		if strings.Join(got, " ") != tt.want {
			t.Errorf("gridSplits(%d, %s) = %v, want %s", tt.n, tt.layout, got, tt.want)
		}
	}
}

func TestITerm_Script(t *testing.T) {
	it := &ITerm{Opts: launchers().ITerm}
	p := testPlan()
	p.Host = "me@buildbox"
	p.Workspaces[0].Agents[0].Title = `say "hi"`
	script, err := it.Script(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`set w to (create window with profile "Default")`,
		`tell s1 to set s2 to (split vertically with profile "Default")`,
		`tell s1 to set s3 to (split horizontally with profile "Default")`,
		`tell w to set t to (create tab with profile "Default")`, // lib's tab
		`set name to "say \"hi\""`,
		"write text \"printf '\\\\033]1337;SetBadgeFormat=%s\\\\007' YWdlbnQtMQ==; ssh -t me@buildbox 'gitten ws run app agent-1'\"",
		"delay 0.150",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("script lacks %s\n%s", want, script)
		}
	}
}

// fakeMuxy is a Muxy server in memory.
type fakeMuxy struct {
	calls    [][]string
	projects map[string]string // dir -> id
}

func (f *fakeMuxy) cli(_ context.Context, args ...string) ([]byte, error) {
	f.calls = append(f.calls, args)
	if args[0] == "--host" {
		args = args[2:]
	}
	if args[0] == "project" && args[1] == "add" {
		id, ok := f.projects[args[2]]
		if !ok {
			id = fmt.Sprintf("p%d", len(f.projects)+1)
			f.projects[args[2]] = id
		}
		return []byte(`{"id":"` + id + `","name":"x"}`), nil
	}
	if args[0] == "session" && args[1] == "create" {
		return []byte("1234\n"), nil
	}
	return nil, nil
}

func (f *fakeMuxy) find(words ...string) [][]string {
	var out [][]string
	for _, c := range f.calls {
		if strings.Contains(strings.Join(c, " "), strings.Join(words, " ")) {
			out = append(out, c)
		}
	}
	return out
}

func TestMuxy_LayoutsSessionsAndPruning(t *testing.T) {
	fake := &fakeMuxy{projects: map[string]string{}}
	var out bytes.Buffer
	m := &Muxy{Opts: launchers().Muxy, cli: fake.cli, Env: Env{Stdout: &out}, Recorded: map[string]string{"gone": "p9"}}
	m.Opts.Host, m.Opts.PruneProjects = "me@buildbox", true

	if err := m.Open(context.Background(), testPlan()); err != nil {
		t.Fatal(err)
	}
	if m.Projects["app"] != "p1" || m.Projects["lib"] != "p2" {
		t.Errorf("projects = %v", m.Projects)
	}
	if got := fake.find("--host me@buildbox project add /ws/app --name app --reuse --json"); len(got) != 1 {
		t.Errorf("project add: %v", fake.calls)
	}
	if got := fake.find("project set-color p1 #4F8EF7"); len(got) != 1 {
		t.Error("projects aren't colored")
	}
	if got := fake.find("project delete p9 --yes"); len(got) != 1 {
		t.Error("a project for a workspace no longer in the fleet wasn't pruned")
	}
	writes := fake.find("exec p1 -- /bin/sh -c")
	if len(writes) != 1 {
		t.Fatalf("layout writes: %v", fake.calls)
	}
	w := writes[0]
	if w[len(w)-2] != ".muxy/layouts/claude.json" {
		t.Errorf("layout file = %s", w[len(w)-2])
	}
	var layout muxyLayout
	if err := json.Unmarshal([]byte(w[len(w)-1]), &layout); err != nil || len(layout.Tabs) != 3 || layout.Tabs[2].Command != "gitten ws run app agent-3" {
		t.Errorf("layout = %+v, %v", layout, err)
	}

	m.Opts.Open = "sessions"
	fake.calls = nil
	p := testPlan()
	p.Workspaces[0].Agents[1].State = "running"
	if err := m.Open(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	if got := fake.find("session create"); len(got) != 3 {
		t.Errorf("sessions for 3 of 4 agents, one running: %v", got)
	}
	if got := fake.find("session create p1 --directory /ws/app/agent-1 -- gitten ws run app agent-1"); len(got) != 1 {
		t.Errorf("session create: %v", fake.calls)
	}
}

// fakeApp is the Muxy desktop app, its state in a file.
type fakeApp struct {
	path    string
	running bool
	quits   int
}

func (a *fakeApp) StatePath() string                     { return a.path }
func (a *fakeApp) Running(context.Context) (bool, error) { return a.running, nil }
func (a *fakeApp) Quit(context.Context) error            { a.running = false; a.quits++; return nil }
func (a *fakeApp) Launch(context.Context) error          { a.running = true; return nil }

func TestMuxy_State(t *testing.T) {
	path := filepath.Join(t.TempDir(), "desktop-state.json")
	initial := `{"version": 3, "unknown": {"kept": true}, "window": {}, "projects": [
		{"id": "p1", "name": "app", "tabs": []},
		{"id": "p2", "name": "lib", "tabs": [{"id": "existing"}]}]}`
	if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}
	app := &fakeApp{path: path, running: true}
	fake := &fakeMuxy{projects: map[string]string{}}
	live := 0
	cli := func(ctx context.Context, args ...string) ([]byte, error) {
		if args[0] == "session" && args[1] == "list" {
			live = 3
			return []byte(strings.Repeat("s\tp1\tLive\tno\t/d\n", live)), nil
		}
		return fake.cli(ctx, args...)
	}
	asked := ""
	var out bytes.Buffer
	m := &Muxy{Opts: launchers().Muxy, cli: cli, app: app, Env: Env{Stdout: &out, Confirm: func(q string) bool { asked = q; return true }}}
	m.Opts.Open = "state"

	if err := m.Open(context.Background(), testPlan()); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if asked == "" || app.quits != 1 || !app.running {
		t.Errorf("asked %q, quits %d, running %v", asked, app.quits, app.running)
	}
	st, err := loadMuxyState(path)
	if err != nil {
		t.Fatal(err)
	}
	tabs := tabsOf(st, "p1")
	if len(tabs) != 3 || len(tabsOf(st, "p2")) != 1 {
		t.Fatalf("tabs: app %d, lib %d; want 3 added to app, lib's left", len(tabs), len(tabsOf(st, "p2")))
	}
	tab := tabs[0].(map[string]any)
	pane := tab["panes"].([]any)[0].(map[string]any)["id"].(string)
	if cmd := st["startup_commands"].(map[string]any)[pane]; cmd != "gitten ws run app agent-1" || tab["custom_title"] != "app/agent-1" {
		t.Errorf("tab %v, command %v", tab, cmd)
	}
	if st["unknown"] == nil {
		t.Error("state gitten doesn't know was dropped")
	}
	if _, err := os.Stat(path + ".gitten-backup"); err != nil {
		t.Error("the previous state wasn't kept")
	}

	// Another version is refused, untouched.
	if err := os.WriteFile(path, []byte(`{"version": 4, "projects": []}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := m.Open(context.Background(), testPlan()); err == nil || !strings.Contains(err.Error(), "version") {
		t.Errorf("Open on version 4 = %v", err)
	}
	m.Opts.Restart = "never"
}

func TestPlugin(t *testing.T) {
	dir := t.TempDir()
	got := filepath.Join(dir, "plan.json")
	script := "#!/bin/sh\ncat > " + got + "\n"
	if err := os.WriteFile(filepath.Join(dir, PluginPrefix+"wez"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := launchers()
	cfg.Plugins = map[string]map[string]any{"wez": {"tabs": "per-agent"}}
	env := Env{Getenv: func(string) string { return "" }, LookPath: func(name string) (string, error) {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err != nil {
			return "", err
		}
		return p, nil
	}, Stdout: os.Stdout, Stderr: os.Stderr}

	l, err := New("wez", cfg, env)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Open(context.Background(), testPlan()); err != nil {
		t.Fatal(err)
	}
	var p Plan
	data, _ := os.ReadFile(got)
	if err := json.Unmarshal(data, &p); err != nil || p.Agents() != 4 || p.Options["tabs"] != "per-agent" {
		t.Errorf("the plugin got %s (%v)", data, err)
	}
	if _, err := New("nope", cfg, env); err == nil {
		t.Error("a launcher with no plugin on PATH was made")
	}
}

func TestAuto(t *testing.T) {
	for _, tt := range []struct {
		env  map[string]string
		tmux bool
		want string
	}{
		{map[string]string{"TMUX": "/tmp/x"}, true, "tmux"},
		{map[string]string{"MUXY_SESSION_ID": "1"}, true, "muxy"},
		{map[string]string{"TERM_PROGRAM": "iTerm.app"}, true, "iterm"},
		{map[string]string{}, true, "tmux"},
		{map[string]string{}, false, ""},
	} {
		env := Env{Getenv: func(k string) string { return tt.env[k] }, LookPath: func(string) (string, error) {
			if tt.tmux {
				return "/usr/bin/tmux", nil
			}
			return "", errors.New("not found")
		}}
		l, err := New("auto", launchers(), env)
		switch {
		case tt.want == "" && !errors.Is(err, ErrNoLauncher):
			t.Errorf("%v: err = %v, want ErrNoLauncher", tt.env, err)
		case tt.want != "" && (err != nil || l.Name() != tt.want):
			t.Errorf("%v: launcher = %v, %v; want %s", tt.env, l, err, tt.want)
		}
	}
}
