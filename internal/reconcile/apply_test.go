package reconcile_test

import (
	"context"
	"errors"
	"fmt"
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

// bareRemote creates <root>/remotes/<name>.git with one commit. Its tracking
// key is local/remotes/<name>.
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
	t     *testing.T
	cfg   *config.Config
	st    *state.State
	found map[string]discovery.Repo // stands in for discovery when set
}

func newWorld(t *testing.T, root string) *world {
	cfg := config.Defaults()
	cfg.BaseDir = filepath.Join(root, "repos")
	cfg.Backup.ArchiveDir = filepath.Join(root, "archive")
	st, err := state.Load(filepath.Join(root, "state.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return &world{t: t, cfg: cfg, st: st}
}

func (w *world) plan() *reconcile.Plan {
	w.t.Helper()
	ctx := context.Background()
	results, discovered := []discovery.DiscoverResult(nil), w.found
	if discovered == nil {
		var err error
		results, discovered, err = discovery.RunDiscovery(ctx, w.cfg, "", "")
		if err != nil {
			w.t.Fatal(err)
		}
	}
	p := reconcile.Compute(ctx, w.cfg, w.st, reconcile.Scope{
		Discovered: discovered, Rules: discovery.StaleScopeOf(results, "", ""),
	})
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
		KeepDirs:    []string{w.cfg.BaseDir},
	}
	results := p.Apply(context.Background(), env, include)
	for _, r := range results {
		if r.Err != nil {
			w.t.Fatalf("%s %s: %v", r.Action.Kind, r.Action.Target, r.Err)
		}
	}
	return results
}

// edit changes the state as another command would: under the lock, saved.
func (w *world) edit(change func(*state.State)) {
	w.t.Helper()
	err := w.st.Update(time.Second, func(st *state.State) error {
		change(st)
		return nil
	})
	if err != nil {
		w.t.Fatal(err)
	}
}

// converge applies everything and checks a second plan is empty.
func (w *world) converge() {
	w.t.Helper()
	w.apply(w.plan(), reconcile.All)
	if p := w.plan(); !p.Empty() {
		w.t.Fatalf("a second plan should be empty, got %+v", p)
	}
}

func kinds(p *reconcile.Plan) []string {
	var out []string
	for _, a := range p.Actions {
		k := string(a.Kind)
		if a.Disruptive {
			k += "!"
		}
		out = append(out, k)
	}
	return out
}

func assertKinds(t *testing.T, p *reconcile.Plan, want ...string) {
	t.Helper()
	if got := strings.Join(kinds(p), ","); got != strings.Join(want, ",") {
		t.Fatalf("plan kinds = %s, want %s\nplan: %+v", got, strings.Join(want, ","), p)
	}
}

func TestApply_RepoLifecycle(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	remote := bareRemote(t, root, "app")
	const key = "local/remotes/app"
	w := newWorld(t, root)
	w.cfg.Repos = []config.ManualRepo{{URL: remote, SSHKey: "/keys/work"}}
	clonePath := filepath.Join(w.cfg.BaseDir, "local", "remotes", "app")

	// A first run clones and writes the managed git config.
	p := w.plan()
	assertKinds(t, p, "clone")
	w.converge()
	if got := mustGit(t, clonePath, "config", "--local", "core.sshCommand"); got != reconcile.SSHCommand("/keys/work") {
		t.Errorf("core.sshCommand = %q", got)
	}
	a := w.st.Repos[key]
	if a == nil || a.CopyID == "" || a.ManagedConfig["core.sshcommand"] == "" {
		t.Fatalf("state after clone: %+v", a)
	}

	// A linked worktree follows it when it moves.
	wt := filepath.Join(root, "elsewhere", "wt")
	mustGit(t, clonePath, "worktree", "add", "-q", "-b", "side", wt)

	// A new path_template is a move, which only apply makes.
	w.cfg.PathTemplate = "{{.Name}}"
	newPath := filepath.Join(w.cfg.BaseDir, "app")
	p = w.plan()
	assertKinds(t, p, "move!")
	w.apply(p, reconcile.Safe)
	if _, err := os.Stat(clonePath); err != nil {
		t.Fatal("a safe-only apply must not move anything")
	}
	w.converge()
	if _, err := os.Stat(filepath.Join(newPath, ".git")); err != nil {
		t.Fatalf("repo not at %s: %v", newPath, err)
	}
	if _, err := os.Stat(filepath.Join(w.cfg.BaseDir, "local")); !os.IsNotExist(err) {
		t.Error("the move should remove the empty directories it left")
	}
	mustGit(t, wt, "status", "--short") // the linked worktree still works
	mustGit(t, newPath, "worktree", "remove", wt)

	// A move interrupted after the rename, before state was saved, is recorded.
	w.cfg.PathTemplate = "{{.Host}}/{{.Owner}}/{{.Name}}"
	if err := os.MkdirAll(filepath.Dir(clonePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(newPath, clonePath); err != nil {
		t.Fatal(err)
	}
	p = w.plan()
	assertKinds(t, p, "move")
	w.converge()

	// Removing the key from config unsets it.
	w.cfg.Repos[0].SSHKey = ""
	assertKinds(t, w.plan(), "config")
	w.converge()
	if _, err := gitops.Git(ctx, clonePath, "config", "--local", "core.sshCommand"); err == nil {
		t.Error("core.sshCommand should be unset")
	}

	// A value changed by hand is reported, not overwritten.
	mustGit(t, clonePath, "remote", "set-url", "origin", filepath.Join(root, "mirror.git"))
	p = w.plan()
	if len(p.Drift) != 1 || len(p.Actions) != 0 {
		t.Fatalf("hand-changed origin: %+v", p)
	}
	w.cfg.Reconcile.Drift = "overwrite"
	w.converge()
	if got := mustGit(t, clonePath, "remote", "get-url", "origin"); got != remote {
		t.Errorf("origin = %s, want it restored to %s", got, remote)
	}

	// A clone deleted by hand comes back.
	if err := os.RemoveAll(clonePath); err != nil {
		t.Fatal(err)
	}
	assertKinds(t, w.plan(), "clone")
	w.converge()

	// A clone made by an interrupted run, never recorded, is adopted.
	w.edit(func(st *state.State) { delete(st.Repos, key) })
	assertKinds(t, w.plan(), "adopt")
	w.converge()
}

// A repo renamed upstream keeps its copy, history and backups: it is rekeyed,
// re-pointed at the new URL and moved, not cloned again beside the old one.
func TestApply_RenameFollowsTheProviderID(t *testing.T) {
	root := t.TempDir()
	remote := bareRemote(t, root, "app")
	w := newWorld(t, root)
	w.found = map[string]discovery.Repo{"local/remotes/app": {
		Host: "local", Owner: "remotes", Name: "app", FullName: "remotes/app", ProviderID: "42", SSHURL: remote,
	}}
	w.converge()
	oldPath := w.st.Repos["local/remotes/app"].LocalPath
	if err := os.WriteFile(filepath.Join(oldPath, "wip.txt"), []byte("local work\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	renamed := filepath.Join(root, "remotes", "app-v2.git")
	if err := os.Rename(remote, renamed); err != nil {
		t.Fatal(err)
	}
	w.found = map[string]discovery.Repo{"local/remotes/app-v2": {
		Host: "local", Owner: "remotes", Name: "app-v2", FullName: "remotes/app-v2", ProviderID: "42", SSHURL: renamed,
	}}
	assertKinds(t, w.plan(), "rename", "url", "move!")
	w.converge()

	a := w.st.Repos["local/remotes/app-v2"]
	if a == nil || w.st.Repos["local/remotes/app"] != nil {
		t.Fatalf("state keys after rename: %v", w.st.Repos)
	}
	if got, _ := os.ReadFile(filepath.Join(a.LocalPath, "wip.txt")); string(got) != "local work\n" {
		t.Error("the local work must move with the repo")
	}
	mustGit(t, a.LocalPath, "fetch", "-q") // origin points at the renamed remote
}

// Adopting a clone never overwrites git config you set yourself; the
// difference is reported as drift instead.
func TestApply_AdoptKeepsConfigSetByHand(t *testing.T) {
	root := t.TempDir()
	remote := bareRemote(t, root, "app")
	w := newWorld(t, root)
	w.cfg.Repos = []config.ManualRepo{{URL: remote, SSHKey: "/keys/work"}}
	clonePath := filepath.Join(w.cfg.BaseDir, "local", "remotes", "app")
	mustGit(t, root, "clone", "-q", remote, clonePath)
	mustGit(t, clonePath, "config", "core.sshCommand", "ssh -i /keys/mine")

	assertKinds(t, w.plan(), "adopt")
	w.apply(w.plan(), reconcile.All)
	if got := mustGit(t, clonePath, "config", "--local", "core.sshCommand"); got != "ssh -i /keys/mine" {
		t.Errorf("core.sshCommand = %q, want the value set by hand", got)
	}
	if managed := w.st.Repos["local/remotes/app"].ManagedConfig; managed["core.sshcommand"] != "" {
		t.Errorf("a value set by hand must not be recorded as git-mgr's: %v", managed)
	}
	p := w.plan()
	if len(p.Actions) != 0 || len(p.Drift) != 1 || p.Drift[0].Found != "ssh -i /keys/mine" || p.Drift[0].Recorded != "(unset)" {
		t.Fatalf("hand-set config should be drift: %+v", p)
	}
}

// An account's commit identity reaches every repo it covers, and follows the
// account when it changes.
func TestApply_AccountIdentity(t *testing.T) {
	root := t.TempDir()
	remote := bareRemote(t, root, "app")
	w := newWorld(t, root)
	w.cfg.Accounts = []config.Account{{Name: "work", Hosts: []string{"local"}, Owners: []string{"remotes"},
		Git: config.GitIdentity{Name: "Work Me", Email: "me@work"}}}
	w.cfg.Repos = []config.ManualRepo{{URL: remote}}
	w.converge()

	repo := w.st.Repos["local/remotes/app"].LocalPath
	if got := mustGit(t, repo, "config", "--local", "user.email"); got != "me@work" {
		t.Errorf("user.email = %q", got)
	}
	w.cfg.Accounts[0].Git.Email = "me@new-work"
	assertKinds(t, w.plan(), "config")
	w.converge()
	if got := mustGit(t, repo, "config", "--local", "user.email"); got != "me@new-work" {
		t.Errorf("user.email = %q after the account changed", got)
	}
}

// Signing settings follow the account: required writes them, off turns
// signing off, inherit hands it back to your global config.
func TestApply_AccountSigning(t *testing.T) {
	root := t.TempDir()
	remote := bareRemote(t, root, "app")
	w := newWorld(t, root)
	w.cfg.Accounts = []config.Account{{Name: "work", Hosts: []string{"local"},
		Signing: config.Signing{Mode: "required", Key: "/keys/sign.pub"}}}
	w.cfg.Repos = []config.ManualRepo{{URL: remote}}
	w.converge()
	repo := w.st.Repos["local/remotes/app"].LocalPath
	for k, v := range map[string]string{"commit.gpgsign": "true", "gpg.format": "ssh", "user.signingkey": "/keys/sign.pub"} {
		if got := mustGit(t, repo, "config", "--local", k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}

	w.cfg.Accounts[0].Signing = config.Signing{Mode: "off"}
	w.converge()
	if got := mustGit(t, repo, "config", "--local", "commit.gpgsign"); got != "false" {
		t.Errorf("commit.gpgsign = %q after turning signing off", got)
	}
	if _, err := gitops.Git(context.Background(), repo, "config", "--local", "gpg.format"); err == nil {
		t.Error("gpg.format should be unset once signing is off")
	}

	w.cfg.Accounts[0].Signing = config.Signing{}
	w.converge()
	if _, err := gitops.Git(context.Background(), repo, "config", "--local", "commit.gpgsign"); err == nil {
		t.Error("commit.gpgsign should be unset under inherit")
	}
}

// Many repos clone at once, then all move at once when the layout changes,
// and every one is recorded.
func TestApply_ManyReposAtOnce(t *testing.T) {
	root := t.TempDir()
	w := newWorld(t, root)
	w.cfg.Concurrency = 4
	w.found = map[string]discovery.Repo{}
	for i := range 6 {
		name := fmt.Sprintf("app%d", i)
		w.found["local/remotes/"+name] = discovery.Repo{
			Host: "local", Owner: "remotes", Name: name, FullName: "remotes/" + name, SSHURL: bareRemote(t, root, name),
		}
	}
	assertKinds(t, w.plan(), "clone", "clone", "clone", "clone", "clone", "clone")
	w.converge()

	w.cfg.PathTemplate = "{{.Name}}"
	assertKinds(t, w.plan(), "move!", "move!", "move!", "move!", "move!", "move!")
	w.converge()
	for key, r := range w.found {
		want := filepath.Join(w.cfg.BaseDir, r.Name)
		if got := w.st.Repos[key].LocalPath; got != want {
			t.Errorf("%s recorded at %s, want %s", key, got, want)
		}
		mustGit(t, want, "status", "--short")
	}
	if _, err := os.Stat(filepath.Join(w.cfg.BaseDir, "local")); !os.IsNotExist(err) {
		t.Error("the moves should remove the directories they emptied")
	}
}

// A write by another command while a plan is applied survives: each change
// is made to the latest state, not the snapshot the plan was computed from.
func TestApply_KeepsConcurrentWrites(t *testing.T) {
	root := t.TempDir()
	w := newWorld(t, root)
	w.cfg.Repos = []config.ManualRepo{{URL: bareRemote(t, root, "app")}}
	p := w.plan()
	assertKinds(t, p, "clone")

	other, err := state.Load(filepath.Join(root, "state.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	err = other.Update(time.Second, func(st *state.State) error {
		st.Repos["example.com/someone/else"] = &state.RepoState{Name: "else"}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	w.apply(p, reconcile.All)

	fresh, err := state.Load(filepath.Join(root, "state.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Repos["example.com/someone/else"] == nil {
		t.Error("the other command's write was lost")
	}
	if fresh.Repos["local/remotes/app"] == nil {
		t.Error("the clone was not recorded")
	}
}

// clone.filter and clone.depth shape new clones.
func TestApply_CloneFilterAndDepth(t *testing.T) {
	root := t.TempDir()
	remote := bareRemote(t, root, "app")
	mustGit(t, remote, "config", "uploadpack.allowFilter", "true")
	w := newWorld(t, root)
	w.cfg.Clone = config.CloneConfig{Filter: "blob:none", Depth: 1}
	w.cfg.Repos = []config.ManualRepo{{URL: "file://" + remote}}
	w.converge()
	for _, r := range w.st.Repos {
		if !gitops.IsPartial(context.Background(), r.LocalPath) {
			t.Error("the clone isn't partial")
		}
		if _, err := os.Stat(filepath.Join(r.LocalPath, ".git", "shallow")); err != nil {
			t.Error("the clone isn't shallow")
		}
	}
}

// A clone whose submodule can't be fetched is otherwise whole: it is recorded,
// so the next run doesn't clone it again, and the failure is reported once.
func TestApply_CloneWithAnUnreachableSubmoduleIsRecorded(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "protocol.file.allow")
	t.Setenv("GIT_CONFIG_VALUE_0", "always")
	lib := bareRemote(t, root, "lib")
	remote := bareRemote(t, root, "app")
	seed := filepath.Join(root, "seed-app")
	mustGit(t, seed, "submodule", "add", "-q", "file://"+lib, "lib")
	mustGit(t, seed, "commit", "-qm", "add lib")
	mustGit(t, seed, "push", "-q", remote, "main")
	if err := os.RemoveAll(lib); err != nil {
		t.Fatal(err)
	}

	w := newWorld(t, root)
	w.cfg.Clone.Submodules = true
	w.cfg.Repos = []config.ManualRepo{{URL: remote}}
	env := &reconcile.Env{
		State:       w.st,
		Update:      func(change func(*state.State) error) error { return w.st.Update(time.Second, change) },
		Concurrency: w.cfg.Concurrency,
	}
	results := w.plan().Apply(context.Background(), env, reconcile.All)
	var sub *gitops.SubmoduleError
	if len(results) != 1 || !errors.As(results[0].Err, &sub) {
		t.Fatalf("want one result failing on the submodule, got %+v", results)
	}
	repo := w.st.Repos["local/remotes/app"]
	if repo == nil || !repo.Cloned {
		t.Fatalf("the clone should be recorded, got %+v", repo)
	}
	if _, err := os.Stat(filepath.Join(repo.LocalPath, "README")); err != nil {
		t.Errorf("the clone should be on disk: %v", err)
	}
	if p := w.plan(); !p.Empty() {
		t.Errorf("the next run should have nothing to do, got %+v", p)
	}
}
