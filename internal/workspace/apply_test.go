package workspace_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ashtonian/git-mgr/internal/config"
	"github.com/ashtonian/git-mgr/internal/discovery"
	"github.com/ashtonian/git-mgr/internal/gitops"
	"github.com/ashtonian/git-mgr/internal/reconcile"
	"github.com/ashtonian/git-mgr/internal/state"
	"github.com/ashtonian/git-mgr/internal/workspace"
)

func TestMain(m *testing.M) {
	os.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	os.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for _, kv := range []string{"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t"} {
		k, v, _ := strings.Cut(kv, "=")
		os.Setenv(k, v)
	}
	os.Exit(m.Run())
}

func mustGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gitops.Git(context.Background(), dir, args...)
	if err != nil {
		t.Fatalf("git %v in %s: %v", args, dir, err)
	}
	return out
}

// bareRemote creates <root>/remotes/<name>.git with one commit on main. Its
// tracking key is local/remotes/<name>.
func bareRemote(t *testing.T, root, name string) string {
	t.Helper()
	remote := filepath.Join(root, "remotes", name+".git")
	seed := filepath.Join(root, "seed-"+name)
	mustGit(t, root, "init", "-q", "--bare", "-b", "main", remote)
	mustGit(t, root, "init", "-q", "-b", "main", seed)
	if err := os.WriteFile(filepath.Join(seed, "README"), []byte(name+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, seed, "add", ".")
	mustGit(t, seed, "commit", "-qm", "init")
	mustGit(t, seed, "push", "-q", remote, "main")
	return remote
}

// world is one machine: config, state and the result of each run.
type world struct {
	t   *testing.T
	cfg *config.Config
	st  *state.State
}

func newWorld(t *testing.T, root string) *world {
	cfg := config.Defaults()
	cfg.BaseDir = filepath.Join(root, "repos")
	cfg.WorkspaceDefaults.Root = filepath.Join(root, "workspaces")
	cfg.Backup.ArchiveDir = filepath.Join(root, "archive")
	st, err := state.Load(filepath.Join(root, "state.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return &world{t: t, cfg: cfg, st: st}
}

func (w *world) scope() workspace.Scope {
	w.t.Helper()
	results, discovered, err := discovery.RunDiscovery(context.Background(), w.cfg, "", "")
	if err != nil {
		w.t.Fatal(err)
	}
	return workspace.Scope{Repos: true, Discovery: reconcile.Scope{Discovered: discovered, Rules: discovery.StaleScopeOf(results, "", "")}}
}

func (w *world) plan() *reconcile.Plan {
	w.t.Helper()
	p := workspace.Compute(context.Background(), w.cfg, w.st, w.scope())
	if len(p.Blocked) > 0 {
		w.t.Fatalf("blocked: %+v", p.Blocked)
	}
	return p
}

func (w *world) apply(p *reconcile.Plan, include func(reconcile.Action) bool) []reconcile.Result {
	w.t.Helper()
	env := &reconcile.Env{
		State:       w.st,
		Update:      func(change func(*state.State) error) error { return w.st.Update(time.Second, change) },
		Concurrency: w.cfg.Concurrency,
		ArchiveDir:  w.cfg.Backup.ArchiveDir,
		KeepDirs:    []string{w.cfg.BaseDir, w.cfg.WorkspaceDefaults.Root},
	}
	results := workspace.Apply(context.Background(), w.cfg, env, p, w.scope(), include)
	for _, r := range results {
		if r.Err != nil {
			w.t.Fatalf("%s %s: %v", r.Action.Kind, r.Action.Target, r.Err)
		}
	}
	return results
}

// converge applies everything and checks a second plan is empty.
func (w *world) converge() {
	w.t.Helper()
	w.apply(w.plan(), reconcile.All)
	if p := w.plan(); !p.Empty() {
		w.t.Fatalf("a second plan should be empty, got %+v", p)
	}
}

func (w *world) edit(change func(*state.State)) {
	w.t.Helper()
	if err := w.st.Update(time.Second, func(st *state.State) error { change(st); return nil }); err != nil {
		w.t.Fatal(err)
	}
}

func kinds(p *reconcile.Plan) string {
	var out []string
	for _, a := range p.Actions {
		k := string(a.Kind)
		if a.Disruptive {
			k += "!"
		}
		out = append(out, k)
	}
	return strings.Join(out, ",")
}

func assertKinds(t *testing.T, p *reconcile.Plan, want ...string) {
	t.Helper()
	if got := kinds(p); got != strings.Join(want, ",") {
		t.Fatalf("plan kinds = %s, want %s\nplan: %+v", got, strings.Join(want, ","), p)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestApply_WorktreeWorkspaceLifecycle(t *testing.T) {
	root := t.TempDir()
	remote := bareRemote(t, root, "app")
	w := newWorld(t, root)
	w.cfg.Repos = []config.ManualRepo{{URL: remote}}
	w.cfg.Workspaces = []config.Workspace{{Name: "app", Repos: []string{"local/remotes/app"}, Agents: 2}}
	dir := filepath.Join(w.cfg.WorkspaceDefaults.Root, "app")

	assertKinds(t, w.plan(), "clone", "create-workspace", "create-copy", "create-copy")
	w.converge()
	ws := w.st.Workspaces["app"]
	if ws == nil || !ws.Declared || len(ws.Replicas) != 2 {
		t.Fatalf("workspace state: %+v", ws)
	}
	a1 := filepath.Join(dir, "agent-1")
	if got := mustGit(t, a1, "symbolic-ref", "--short", "HEAD"); got != "agents/app/agent-1" {
		t.Errorf("agent-1 is on %s", got)
	}
	if c := ws.Replica("agent-1").Copies[0]; c.Repo != "local/remotes/app" || c.Path != a1 || c.Branch != "agents/app/agent-1" || c.CopyID == "" {
		t.Errorf("agent-1's copy: %+v", c)
	}

	// The repo moving takes its worktrees along; the workspace records
	// nothing about where the repo is.
	w.cfg.PathTemplate = "{{.Name}}"
	assertKinds(t, w.plan(), "move!")
	w.converge()
	mustGit(t, a1, "status", "--short")

	// Dropping an agent archives its uncommitted work, then removes its copy.
	writeFile(t, filepath.Join(dir, "agent-2", "notes.txt"), "agent work\n")
	w.cfg.Workspaces[0].Agents = 1
	p := w.plan()
	assertKinds(t, p, "remove-copy!")
	results := w.apply(p, reconcile.All)
	if !strings.Contains(results[0].Info, "archived to") {
		t.Errorf("removal should report the archive, got %q", results[0].Info)
	}
	if _, err := os.Stat(filepath.Join(dir, "agent-2")); !os.IsNotExist(err) {
		t.Fatal("agent-2's copy should be removed")
	}
	if w.st.Workspaces["app"].Replica("agent-2") != nil {
		t.Error("agent-2's replica should be forgotten")
	}

	// A copy deleted by hand is made again, on its branch.
	mustGit(t, w.st.Repos["local/remotes/app"].LocalPath, "worktree", "remove", "--force", a1)
	assertKinds(t, w.plan(), "create-copy")
	w.converge()

	// A new root moves the copies.
	w.cfg.WorkspaceDefaults.Root = filepath.Join(root, "agents")
	assertKinds(t, w.plan(), "workspace", "move-copy!")
	w.apply(w.plan(), reconcile.All)
	moved := filepath.Join(root, "agents", "app", "agent-1")
	mustGit(t, moved, "status", "--short")
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Error("the old workspace directory should be gone")
	}

	// Removing the declaration removes the workspace.
	w.cfg.Workspaces = nil
	assertKinds(t, w.plan(), "remove-workspace!")
	w.converge()
	if _, err := os.Stat(filepath.Dir(moved)); !os.IsNotExist(err) {
		t.Error("the workspace directory should be gone")
	}
	if w.st.Workspaces["app"] != nil {
		t.Error("the workspace should be forgotten")
	}
}

// Agents × repos: each agent gets a copy of every repo, side by side.
func TestApply_RelatedRepos(t *testing.T) {
	root := t.TempDir()
	w := newWorld(t, root)
	w.cfg.Repos = []config.ManualRepo{{URL: bareRemote(t, root, "api")}, {URL: bareRemote(t, root, "web")}}
	w.cfg.Workspaces = []config.Workspace{{Name: "platform", Repos: []string{"local/remotes/*"}, Agents: 2}}
	w.converge()
	for _, agent := range []string{"agent-1", "agent-2"} {
		for _, repo := range []string{"api", "web"} {
			path := filepath.Join(w.cfg.WorkspaceDefaults.Root, "platform", agent, repo)
			if got := mustGit(t, path, "symbolic-ref", "--short", "HEAD"); got != "agents/platform/"+agent {
				t.Errorf("%s is on %s", path, got)
			}
		}
	}
	if n := len(w.st.Workspaces["platform"].Replica("agent-2").Copies); n != 2 {
		t.Errorf("agent-2 has %d copies", n)
	}
}

// Clone copies get their objects from the repo's clone, their origin from
// the repo, and their account's identity; a value changed by hand is drift.
func TestApply_CloneIsolation(t *testing.T) {
	root := t.TempDir()
	remote := bareRemote(t, root, "app")
	w := newWorld(t, root)
	w.cfg.Accounts = []config.Account{{Name: "work", Hosts: []string{"local"}, Git: config.GitIdentity{Email: "me@work"}}}
	w.cfg.Repos = []config.ManualRepo{{URL: remote}}
	w.cfg.Workspaces = []config.Workspace{{Name: "app", Repos: []string{"local/remotes/app"}, Isolation: "clone"}}
	w.converge()

	copyPath := filepath.Join(w.cfg.WorkspaceDefaults.Root, "app", "agent-1")
	source := w.st.Repos["local/remotes/app"].LocalPath
	if got := mustGit(t, copyPath, "remote", "get-url", "origin"); got != remote {
		t.Errorf("origin = %s", got)
	}
	if got := mustGit(t, copyPath, "remote", "get-url", gitops.SourceRemote); got != source {
		t.Errorf("git-mgr remote = %s, want %s", got, source)
	}
	if got := mustGit(t, copyPath, "config", "--local", "user.email"); got != "me@work" {
		t.Errorf("user.email = %q", got)
	}

	w.cfg.Accounts[0].Git.Email = "me@new-work"
	assertKinds(t, w.plan(), "config", "copy-config")
	w.converge()
	if got := mustGit(t, copyPath, "config", "--local", "user.email"); got != "me@new-work" {
		t.Errorf("user.email = %q after the account changed", got)
	}
	mustGit(t, copyPath, "config", "user.email", "agent@elsewhere")
	if p := w.plan(); len(p.Drift) != 1 || p.Drift[0].Target != "app/agent-1" || len(p.Actions) != 0 {
		t.Fatalf("an identity changed by hand in a copy is drift: %+v", p)
	}
}

// A reference clone borrows the repo's objects; moving the repo repoints it
// as part of the move, so it never loses them.
func TestApply_ReferenceCloneFollowsTheRepo(t *testing.T) {
	root := t.TempDir()
	w := newWorld(t, root)
	w.cfg.Repos = []config.ManualRepo{{URL: bareRemote(t, root, "app")}}
	w.cfg.Workspaces = []config.Workspace{{Name: "app", Repos: []string{"local/remotes/app"}, Isolation: "reference-clone"}}
	w.converge()
	copyPath := filepath.Join(w.cfg.WorkspaceDefaults.Root, "app", "agent-1")

	w.cfg.PathTemplate = "{{.Name}}"
	assertKinds(t, w.plan(), "move!")
	w.converge()
	mustGit(t, copyPath, "log", "--oneline", "-1", "origin/main")
	if got := mustGit(t, copyPath, "remote", "get-url", gitops.SourceRemote); got != filepath.Join(w.cfg.BaseDir, "app") {
		t.Errorf("git-mgr remote = %s", got)
	}
}

// A workspace naming a local repo git-mgr doesn't track adopts it where it
// is, and its copies are made from it.
func TestApply_AdoptsARepoNamedByPath(t *testing.T) {
	root := t.TempDir()
	remote := bareRemote(t, root, "app")
	local := filepath.Join(root, "src", "app")
	mustGit(t, root, "clone", "-q", remote, local)
	w := newWorld(t, root)
	w.cfg.Workspaces = []config.Workspace{{Name: "app", Repos: []string{local}}}

	assertKinds(t, w.plan(), "adopt", "create-workspace", "create-copy")
	w.converge()
	if r := w.st.Repos["local/remotes/app"]; r == nil || r.LocalPath != local || !r.ManuallyAdded {
		t.Fatalf("adopted repo: %+v", r)
	}
	if c := w.st.Workspaces["app"].Replicas[0].Copies[0]; c.Repo != "local/remotes/app" {
		t.Errorf("copy: %+v", c)
	}
}

// A workgroup from state version 2 whose source wasn't tracked keeps its
// worktrees: declaring it as a workspace adopts the source and records which
// repo each copy is of, moving and creating nothing.
func TestApply_MigratedWorkgroupIsKept(t *testing.T) {
	root := t.TempDir()
	remote := bareRemote(t, root, "app")
	source := filepath.Join(root, "src", "app")
	mustGit(t, root, "clone", "-q", remote, source)
	wgDir := filepath.Join(root, "workgroups", "app")
	for _, agent := range []string{"agent-1", "agent-2"} {
		mustGit(t, source, "worktree", "add", "-q", "-b", "app/"+agent, filepath.Join(wgDir, agent))
	}
	writeFile(t, filepath.Join(wgDir, "agent-1", "wip.txt"), "in progress\n")
	v2 := `version: 2
workgroups:
  app:
    kind: worktree
    root: ` + wgDir + `
    source: ` + source + `
    members:
      - {name: agent-1, path: ` + filepath.Join(wgDir, "agent-1") + `, branch: app/agent-1}
      - {name: agent-2, path: ` + filepath.Join(wgDir, "agent-2") + `, branch: app/agent-2}
`
	writeFile(t, filepath.Join(root, "state.yaml"), v2)
	w := newWorld(t, root)
	w.cfg.WorkspaceDefaults.Root = filepath.Join(root, "workgroups")
	w.cfg.Workspaces = []config.Workspace{{Name: "app", Repos: []string{source}, Agents: 2}}

	assertKinds(t, w.plan(), "adopt", "workspace", "copy", "copy")
	w.converge()
	for _, r := range w.st.Workspaces["app"].Replicas {
		if c := r.Copies[0]; c.Repo != "local/remotes/app" || c.Branch != "app/"+r.Agent {
			t.Errorf("%s's copy: %+v; want its repo recorded and its branch kept", r.Agent, c)
		}
	}
	if got, _ := os.ReadFile(filepath.Join(wgDir, "agent-1", "wip.txt")); string(got) != "in progress\n" {
		t.Error("the agent's work was touched")
	}
}

// Applying a plan never runs a disruptive workspace action the plan didn't
// show, even when re-planning after the repo changes would find one.
func TestApply_RunsOnlyDisruptionsThatWereShown(t *testing.T) {
	root := t.TempDir()
	w := newWorld(t, root)
	w.cfg.Repos = []config.ManualRepo{{URL: bareRemote(t, root, "app")}}
	w.cfg.Workspaces = []config.Workspace{{Name: "app", Repos: []string{"local/remotes/app"}, Agents: 2}}
	w.converge()

	shown := w.plan() // nothing to do
	w.cfg.Workspaces[0].Agents = 1
	for _, r := range w.apply(shown, reconcile.All) {
		t.Errorf("unexpected action %s %s", r.Action.Kind, r.Action.Target)
	}
	if _, err := os.Stat(filepath.Join(w.cfg.WorkspaceDefaults.Root, "app", "agent-2")); err != nil {
		t.Fatal("a removal that wasn't shown must not run")
	}
}

// A workspace over a folder records its agents there and makes no copies.
func TestApply_SharedFolder(t *testing.T) {
	root := t.TempDir()
	w := newWorld(t, root)
	notes := filepath.Join(root, "notes")
	w.cfg.Workspaces = []config.Workspace{{Name: "notes", Path: notes, Agents: 2}}
	assertKinds(t, w.plan(), "create-workspace")
	w.converge()
	ws := w.st.Workspaces["notes"]
	if ws.Isolation != "shared" || len(ws.Replicas) != 2 || ws.Replicas[1].Path != notes {
		t.Fatalf("workspace: %+v", ws)
	}
	if info, err := os.Stat(notes); err != nil || !info.IsDir() {
		t.Errorf("the shared folder should be made: %v", err)
	}
	w.cfg.Workspaces[0].Agents = 3
	assertKinds(t, w.plan(), "workspace")
	w.converge()
}

// A path already holding something else blocks the copy rather than
// adopting it.
func TestPlan_BlocksAnOccupiedPath(t *testing.T) {
	root := t.TempDir()
	w := newWorld(t, root)
	w.cfg.Repos = []config.ManualRepo{{URL: bareRemote(t, root, "app")}}
	w.converge()
	occupied := filepath.Join(w.cfg.WorkspaceDefaults.Root, "app", "agent-1")
	if err := os.MkdirAll(occupied, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(occupied, "file"), "x")
	w.cfg.Workspaces = []config.Workspace{{Name: "app", Repos: []string{"local/remotes/app"}}}
	p := workspace.Compute(context.Background(), w.cfg, w.st, w.scope())
	if len(p.Blocked) != 1 || p.Blocked[0].Target != "app/agent-1" {
		t.Fatalf("blocked = %+v", p.Blocked)
	}
}

// A workspace naming a URL git-mgr doesn't track clones it where config puts
// repos, then makes the copies from that clone.
func TestApply_ClonesARepoNamedByURL(t *testing.T) {
	root := t.TempDir()
	remote := bareRemote(t, root, "app")
	w := newWorld(t, root)
	w.cfg.Workspaces = []config.Workspace{{Name: "app", Repos: []string{"file://" + remote}}}
	assertKinds(t, w.plan(), "clone", "create-workspace", "create-copy")
	w.converge()
	if len(w.st.Repos) != 1 {
		t.Fatalf("repos = %v", w.st.Repos)
	}
}
