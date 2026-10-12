// Package e2e contains git-mgr's end-to-end tests. They spin up a real
// Forgejo instance via testcontainers, seed it over the API, then drive the
// compiled git-mgr binary as a subprocess and assert on its output, exit
// code, and the state it writes. Nothing here imports git-mgr's internal
// packages — the binary is the contract under test, which is why this is a
// separate module (keeping testcontainers/Docker out of the root module).
//
// Run with: go test ./...   (from the e2e/ directory; requires Docker)
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go/modules/forgejo"
)

// --- git-mgr binary (built once for the whole suite) ---

var (
	binOnce sync.Once
	binPath string
	binErr  error
)

// gitMgrBin builds the git-mgr binary from the parent module once and returns
// its path. Building (rather than `go run`) keeps each invocation cheap.
func gitMgrBin(t *testing.T) string {
	t.Helper()
	binOnce.Do(func() {
		dir, err := os.MkdirTemp("", "git-mgr-bin")
		if err != nil {
			binErr = err
			return
		}
		binPath = filepath.Join(dir, "git-mgr")
		// -C .. builds the root module; the e2e module has no code dependency on it.
		cmd := exec.Command("go", "build", "-C", "..", "-o", binPath, ".")
		if out, err := cmd.CombinedOutput(); err != nil {
			binErr = fmt.Errorf("building git-mgr: %w\n%s", err, out)
		}
	})
	if binErr != nil {
		t.Fatalf("git-mgr binary unavailable: %v", binErr)
	}
	return binPath
}

// --- test environment ---

// testEnv holds a running Forgejo instance and the paths git-mgr writes to.
type testEnv struct {
	httpURL    string
	adminUser  string
	adminPass  string
	bin        string
	baseDir    string
	stateFile  string
	configFile string
}

func setupTestEnv(t *testing.T) *testEnv {
	t.Helper()
	ctx := context.Background()

	ctr, err := forgejo.Run(ctx, "codeberg.org/forgejo/forgejo:11")
	if err != nil {
		t.Fatalf("starting forgejo: %v", err)
	}
	t.Cleanup(func() { _ = ctr.Terminate(ctx) })

	httpURL, err := ctr.ConnectionString(ctx)
	if err != nil {
		t.Fatalf("getting connection string: %v", err)
	}

	tmp := t.TempDir()
	return &testEnv{
		httpURL:    httpURL,
		adminUser:  ctr.AdminUsername(),
		adminPass:  ctr.AdminPassword(),
		bin:        gitMgrBin(t),
		baseDir:    filepath.Join(tmp, "repos"),
		stateFile:  filepath.Join(tmp, "state.yaml"),
		configFile: filepath.Join(tmp, "config.yaml"),
	}
}

// run executes git-mgr with the given args, pointing it at this env's config
// and state files, and returns combined output plus the exit code.
func (e *testEnv) run(t *testing.T, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(e.bin, args...)
	cmd.Env = append(os.Environ(),
		"GIT_MGR_CONFIG="+e.configFile,
		"GIT_MGR_STATE="+e.stateFile,
	)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	code := 0
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		} else {
			t.Fatalf("running git-mgr %v: %v", args, err)
		}
	}
	return buf.String(), code
}

// statusJSON runs `git-mgr status --json` and decodes it into a map keyed by
// repo key. Asserting via the CLI's own JSON output keeps the tests decoupled
// from the on-disk state format.
func (e *testEnv) statusJSON(t *testing.T) map[string]map[string]any {
	t.Helper()
	out, code := e.run(t, "status", "--json")
	if code != 0 {
		t.Fatalf("status --json exit %d:\n%s", code, out)
	}
	out = strings.TrimSpace(out)
	if !strings.HasPrefix(out, "{") {
		return map[string]map[string]any{} // "no repos tracked" human line
	}
	var m map[string]map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("decoding status json: %v\n%s", err, out)
	}
	return m
}

// --- Forgejo API seeding ---

func (e *testEnv) createToken(t *testing.T) string {
	t.Helper()
	body := map[string]any{
		"name":   fmt.Sprintf("test-token-%d", time.Now().UnixNano()),
		"scopes": []string{"all"},
	}
	resp := e.api(t, "POST", "/api/v1/users/"+e.adminUser+"/tokens", body, "")
	defer resp.Body.Close()
	var result map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&result)
	token, ok := result["sha1"].(string)
	if !ok {
		t.Fatalf("no sha1 in token response: %v", result)
	}
	return token
}

func (e *testEnv) createRepo(t *testing.T, token, name string, private bool) {
	t.Helper()
	body := map[string]any{"name": name, "private": private, "auto_init": true, "default_branch": "main"}
	resp := e.api(t, "POST", "/api/v1/user/repos", body, token)
	defer resp.Body.Close()
	if resp.StatusCode != 201 {
		t.Fatalf("create repo %s: status %d", name, resp.StatusCode)
	}
}

func (e *testEnv) createOrg(t *testing.T, token, org string) {
	t.Helper()
	resp := e.api(t, "POST", "/api/v1/orgs", map[string]any{"username": org, "visibility": "public"}, token)
	defer resp.Body.Close()
	if resp.StatusCode != 201 {
		t.Fatalf("create org %s: status %d", org, resp.StatusCode)
	}
}

func (e *testEnv) createOrgRepo(t *testing.T, token, org, name string) {
	t.Helper()
	body := map[string]any{"name": name, "auto_init": true, "default_branch": "main"}
	resp := e.api(t, "POST", "/api/v1/orgs/"+org+"/repos", body, token)
	defer resp.Body.Close()
	if resp.StatusCode != 201 {
		t.Fatalf("create org repo %s/%s: status %d", org, name, resp.StatusCode)
	}
}

func (e *testEnv) setTopics(t *testing.T, token, owner, repo string, topics []string) {
	t.Helper()
	resp := e.api(t, "PUT", "/api/v1/repos/"+owner+"/"+repo+"/topics", map[string]any{"topics": topics}, token)
	defer resp.Body.Close()
	if resp.StatusCode != 204 && resp.StatusCode != 200 {
		t.Fatalf("set topics on %s/%s: status %d", owner, repo, resp.StatusCode)
	}
}

// api issues a Forgejo API call. A non-empty token uses token auth; otherwise
// admin basic auth.
func (e *testEnv) api(t *testing.T, method, path string, body any, token string) *http.Response {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.httpURL+path, r)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "token "+token)
	} else {
		req.SetBasicAuth(e.adminUser, e.adminPass)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("API %s %s: %v", method, path, err)
	}
	return resp
}

// httpClone clones a seeded repo over HTTP (basic auth) to destPath. The
// Forgejo container only exposes HTTP conveniently; git-mgr's own clone path is
// SSH, so adopt/backup/sync tests operate on these pre-cloned working trees.
func (e *testEnv) httpClone(t *testing.T, owner, repo, destPath string) {
	t.Helper()
	host := strings.TrimPrefix(e.httpURL, "http://")
	url := fmt.Sprintf("http://%s:%s@%s/%s/%s.git", e.adminUser, e.adminPass, host, owner, repo)
	if out, err := exec.Command("git", "clone", url, destPath).CombinedOutput(); err != nil {
		t.Fatalf("http clone %s/%s: %v\n%s", owner, repo, err, out)
	}
	// Give the clone a committer identity so backup's stash-create works.
	for _, kv := range [][2]string{{"user.email", "e2e@test.local"}, {"user.name", "e2e"}} {
		_ = exec.Command("git", "-C", destPath, "config", kv[0], kv[1]).Run()
	}
}

// writeConfig writes a git-mgr config pointing a "gitea" provider at the
// Forgejo instance. rulesYAML is spliced under discovery:.
func (e *testEnv) writeConfig(t *testing.T, token, rulesYAML string) {
	t.Helper()
	cfg := fmt.Sprintf(`base_dir: %s
concurrency: 4
backup:
  patch_dir: %s
providers:
  - name: forgejo
    type: gitea
    url: %s
    token: %s
discovery:
%s
`, e.baseDir, filepath.Join(e.baseDir, ".patches"), e.httpURL, token, rulesYAML)
	if err := os.MkdirAll(filepath.Dir(e.configFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e.configFile, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

// --- Tests ---

// TestE2E_ConfigValidate checks config loads + validates through the real
// binary (exercises the config loader end to end).
func TestE2E_ConfigValidate(t *testing.T) {
	env := setupTestEnv(t)
	token := env.createToken(t)
	env.writeConfig(t, token, "  - name: me\n    provider: forgejo\n    user: "+env.adminUser)

	out, code := env.run(t, "config", "--validate")
	if code != 0 {
		t.Fatalf("config --validate exit %d:\n%s", code, out)
	}
	if !strings.Contains(out, "valid") {
		t.Errorf("expected validation success, got:\n%s", out)
	}
}

// TestE2E_DiscoverByUser seeds repos for the admin user and asserts discover
// (dry-run) finds them via the native gitea provider.
func TestE2E_DiscoverByUser(t *testing.T) {
	env := setupTestEnv(t)
	token := env.createToken(t)
	env.createRepo(t, token, "repo-alpha", false)
	env.createRepo(t, token, "repo-beta", false)
	env.createRepo(t, token, "repo-private", true)

	env.writeConfig(t, token, "  - name: me\n    provider: forgejo\n    user: "+env.adminUser+"\n    tags: [test]")

	out, code := env.run(t, "discover")
	if code != 0 {
		t.Fatalf("discover exit %d:\n%s", code, out)
	}
	for _, name := range []string{"repo-alpha", "repo-beta", "repo-private"} {
		if !strings.Contains(out, name) {
			t.Errorf("discover output missing %s:\n%s", name, out)
		}
	}
}

// TestE2E_DiscoverApplyHTTPS verifies clone_protocol: https drives discover
// --apply to clone over HTTP (the Forgejo container's transport) -- exercising
// HTTPURL capture, the https clone path, and the cloned URL stored in state, all
// through the real binary. (git-mgr's default transport is SSH, which the
// container doesn't expose, so this also proves the protocol switch works.)
func TestE2E_DiscoverApplyHTTPS(t *testing.T) {
	env := setupTestEnv(t)
	token := env.createToken(t)
	env.createRepo(t, token, "http-repo", false) // public: cloneable over HTTP without auth

	cfg := fmt.Sprintf(`base_dir: %s
clone_protocol: https
concurrency: 2
providers:
  - name: forgejo
    type: gitea
    url: %s
    token: %s
discovery:
  - name: me
    provider: forgejo
    user: %s
`, env.baseDir, env.httpURL, token, env.adminUser)
	if err := os.WriteFile(env.configFile, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	// The clone is attempted over HTTP (not SSH), proving HTTPURL capture and the
	// clone_protocol: https selection through the real binary. We assert the
	// transport rather than a successful clone because the Forgejo container
	// advertises an unreachable internal ROOT_URL (localhost:3000) instead of its
	// mapped port -- a container quirk, not a git-mgr behavior. That failed clone
	// makes discover --apply exit non-zero, as any failed change does.
	out, _ := env.run(t, "discover", "--apply")
	if !strings.Contains(out, "git clone http") || !strings.Contains(out, "http-repo") {
		t.Errorf("expected an http clone attempt for http-repo, got:\n%s", out)
	}
	if strings.Contains(out, "git@") || strings.Contains(out, "ssh://") {
		t.Errorf("clone must not use SSH under clone_protocol: https:\n%s", out)
	}
}

// TestE2E_DiscoverByOrg checks org-scoped discovery.
func TestE2E_DiscoverByOrg(t *testing.T) {
	env := setupTestEnv(t)
	token := env.createToken(t)
	env.createOrg(t, token, "test-org")
	env.createOrgRepo(t, token, "test-org", "service-a")
	env.createOrgRepo(t, token, "test-org", "service-b")
	env.createOrgRepo(t, token, "test-org", "lib-shared")

	env.writeConfig(t, token, "  - name: org\n    provider: forgejo\n    org: test-org")

	out, code := env.run(t, "discover")
	if code != 0 {
		t.Fatalf("discover exit %d:\n%s", code, out)
	}
	for _, name := range []string{"service-a", "service-b", "lib-shared"} {
		if !strings.Contains(out, name) {
			t.Errorf("discover output missing %s:\n%s", name, out)
		}
	}
}

// TestE2E_DiscoverNamePattern checks the regex name filter narrows results.
func TestE2E_DiscoverNamePattern(t *testing.T) {
	env := setupTestEnv(t)
	token := env.createToken(t)
	env.createRepo(t, token, "api-gateway", false)
	env.createRepo(t, token, "api-users", false)
	env.createRepo(t, token, "frontend-app", false)

	env.writeConfig(t, token, "  - name: api-only\n    provider: forgejo\n    user: "+env.adminUser+"\n    name_pattern: \"^api-\"")

	out, code := env.run(t, "discover")
	if code != 0 {
		t.Fatalf("discover exit %d:\n%s", code, out)
	}
	if !strings.Contains(out, "api-gateway") || !strings.Contains(out, "api-users") {
		t.Errorf("expected api- repos in output:\n%s", out)
	}
	if strings.Contains(out, "frontend-app") {
		t.Errorf("frontend-app should have been filtered out:\n%s", out)
	}
}

// TestE2E_DiscoverTopicFilter exercises native-gitea topic enrichment (topics
// fetched per-repo from Forgejo's /topics endpoint).
func TestE2E_DiscoverTopicFilter(t *testing.T) {
	env := setupTestEnv(t)
	token := env.createToken(t)
	env.createRepo(t, token, "topic-match", false)
	env.createRepo(t, token, "topic-nomatch", false)
	env.setTopics(t, token, env.adminUser, "topic-match", []string{"backend"})

	env.writeConfig(t, token, "  - name: by-topic\n    provider: forgejo\n    user: "+env.adminUser+"\n    topics: [backend]")

	out, code := env.run(t, "discover")
	if code != 0 {
		t.Fatalf("discover exit %d:\n%s", code, out)
	}
	if !strings.Contains(out, "topic-match") {
		t.Errorf("expected topic-match in output:\n%s", out)
	}
	if strings.Contains(out, "topic-nomatch") {
		t.Errorf("topic-nomatch should have been filtered out:\n%s", out)
	}
}

// TestE2E_AdoptAndStatus clones a repo over HTTP, adopts it through the binary,
// and verifies it shows up in `status --json` as manually added.
func TestE2E_AdoptAndStatus(t *testing.T) {
	env := setupTestEnv(t)
	token := env.createToken(t)
	env.createRepo(t, token, "adopt-me", false)
	env.writeConfig(t, token, "  - name: me\n    provider: forgejo\n    user: "+env.adminUser)

	dest := filepath.Join(env.baseDir, "adopt-me")
	env.httpClone(t, env.adminUser, "adopt-me", dest)

	out, code := env.run(t, "adopt", dest, "--tag", "adopted")
	if code != 0 {
		t.Fatalf("adopt exit %d:\n%s", code, out)
	}

	repos := env.statusJSON(t)
	if len(repos) != 1 {
		t.Fatalf("expected 1 tracked repo after adopt, got %d:\n%v", len(repos), repos)
	}
	// status --json uses RepoState's json tags, which mirror the snake_case
	// yaml/config keys.
	for key, r := range repos {
		if ma, _ := r["manually_added"].(bool); !ma {
			t.Errorf("repo %s: expected manually_added=true, got %v", key, r["manually_added"])
		}
		tags, _ := r["tags"].([]any)
		if len(tags) != 1 || tags[0] != "adopted" {
			t.Errorf("repo %s: expected tags=[adopted], got %v", key, r["tags"])
		}
	}
}

// TestE2E_BackupTransparency adopts a repo, dirties it, runs backup, and
// verifies a backup is recorded without disturbing the working tree.
func TestE2E_BackupTransparency(t *testing.T) {
	env := setupTestEnv(t)
	token := env.createToken(t)
	env.createRepo(t, token, "backup-test", false)
	env.writeConfig(t, token, "  - name: me\n    provider: forgejo\n    user: "+env.adminUser)

	dest := filepath.Join(env.baseDir, "backup-test")
	env.httpClone(t, env.adminUser, "backup-test", dest)
	if out, code := env.run(t, "adopt", dest); code != 0 {
		t.Fatalf("adopt exit %d:\n%s", code, out)
	}

	// Dirty the working tree (staged + untracked).
	if err := os.WriteFile(filepath.Join(dest, "untracked.txt"), []byte("u"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, "staged.txt"), []byte("s"), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = exec.Command("git", "-C", dest, "add", "staged.txt").Run()

	if out, code := env.run(t, "backup"); code != 0 {
		t.Fatalf("backup exit %d:\n%s", code, out)
	}

	// Working tree must still be dirty (backup is non-destructive).
	st, _ := exec.Command("git", "-C", dest, "status", "--porcelain").CombinedOutput()
	if len(strings.TrimSpace(string(st))) == 0 {
		t.Error("backup should not have cleaned the working tree")
	}

	// backup --list should report a backup for the repo.
	out, code := env.run(t, "backup", "--list")
	if code != 0 {
		t.Fatalf("backup --list exit %d:\n%s", code, out)
	}
	if !strings.Contains(out, "backup-test") {
		t.Errorf("backup --list missing backup-test:\n%s", out)
	}
}

// TestE2E_SyncFetch adopts a repo and runs fetch-only sync through the binary.
func TestE2E_SyncFetch(t *testing.T) {
	env := setupTestEnv(t)
	token := env.createToken(t)
	env.createRepo(t, token, "fetch-test", false)
	env.writeConfig(t, token, "  - name: me\n    provider: forgejo\n    user: "+env.adminUser)

	dest := filepath.Join(env.baseDir, "fetch-test")
	env.httpClone(t, env.adminUser, "fetch-test", dest)
	if out, code := env.run(t, "adopt", dest); code != 0 {
		t.Fatalf("adopt exit %d:\n%s", code, out)
	}

	out, code := env.run(t, "sync", "--no-discover", "--no-backup", "--force")
	if code != 0 {
		t.Fatalf("sync exit %d:\n%s", code, out)
	}
	head, err := exec.Command("git", "-C", dest, "rev-parse", "HEAD").CombinedOutput()
	if err != nil || len(strings.TrimSpace(string(head))) != 40 {
		t.Errorf("invalid HEAD after sync: %q (%v)", head, err)
	}
}

// --- Docker-free local tests (no Forgejo/provider needed) ---

// newLocalEnv builds the binary and points it at fresh temp config/state files
// without starting Forgejo. For features that need no provider: workgroups
// created from a local source, and backups pushed to a local-path remote.
func newLocalEnv(t *testing.T) *testEnv {
	t.Helper()
	tmp := t.TempDir()
	return &testEnv{
		bin:        gitMgrBin(t),
		baseDir:    filepath.Join(tmp, "repos"),
		stateFile:  filepath.Join(tmp, "state.yaml"),
		configFile: filepath.Join(tmp, "config.yaml"),
	}
}

// gitCmd runs `git -C dir args...`, failing the test on error.
func gitCmd(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s (in %s): %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// TestE2E_WGCreateFromFlags drives `wg create` as a one-off from CLI flags (no
// workgroup declared in config), asserting it materialises N worktrees on
// namespaced branches without touching config, then that --save persists the
// synthesized declaration.
func TestE2E_WGCreateFromFlags(t *testing.T) {
	env := newLocalEnv(t)
	tmp := t.TempDir()

	// A local source repo with one commit.
	src := filepath.Join(tmp, "app")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, src, "init", "-q")
	gitCmd(t, src, "config", "user.email", "e2e@test.local")
	gitCmd(t, src, "config", "user.name", "e2e")
	if err := os.WriteFile(filepath.Join(src, "README.md"), []byte("# app"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, src, "add", ".")
	gitCmd(t, src, "commit", "-qm", "init")
	branch := gitCmd(t, src, "rev-parse", "--abbrev-ref", "HEAD")

	// Minimal config so workgroup_base_dir is isolated to the temp dir.
	wgBase := filepath.Join(tmp, "wg")
	cfg := fmt.Sprintf("base_dir: %s\nworkgroup_base_dir: %s\n", env.baseDir, wgBase)
	if err := os.WriteFile(env.configFile, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	// One-off create: 2 worktrees, nothing in config.
	out, code := env.run(t, "wg", "create", "scratch", "--source", src, "--count", "2", "--base-ref", branch)
	if code != 0 {
		t.Fatalf("wg create exit %d:\n%s", code, out)
	}

	// Two worktrees on namespaced branches (<name>/agent-N) must exist.
	for i := 1; i <= 2; i++ {
		member := filepath.Join(wgBase, "scratch", fmt.Sprintf("agent-%d", i))
		if _, err := os.Stat(filepath.Join(member, ".git")); err != nil {
			t.Fatalf("expected worktree at %s: %v\n%s", member, err, out)
		}
		if got, want := gitCmd(t, member, "rev-parse", "--abbrev-ref", "HEAD"), fmt.Sprintf("scratch/agent-%d", i); got != want {
			t.Errorf("member %d branch = %q, want %q", i, got, want)
		}
	}

	// The one-off must not have modified config.
	if b, _ := os.ReadFile(env.configFile); strings.Contains(string(b), "name: scratch") {
		t.Error("one-off create should not modify config without --save")
	}

	// --save persists the synthesized workgroup (create is idempotent, so this
	// exercises the save-after-materialise path).
	out, code = env.run(t, "wg", "create", "scratch", "--source", src, "--count", "2", "--base-ref", branch, "--save")
	if code != 0 {
		t.Fatalf("wg create --save exit %d:\n%s", code, out)
	}
	b, err := os.ReadFile(env.configFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "name: scratch") || !strings.Contains(string(b), "kind: worktree") {
		t.Errorf("--save did not persist workgroup:\n%s", b)
	}
}

// TestE2E_BackupPushBranch adopts a repo whose origin is a local bare remote,
// dirties it, and runs `backup --push-branch`, asserting the WIP snapshot lands
// on the remote as a normal branch while the working tree stays dirty.
func TestE2E_BackupPushBranch(t *testing.T) {
	env := newLocalEnv(t)
	tmp := t.TempDir()

	// Bare "remote" + a working clone with origin pointing at it.
	gitCmd(t, tmp, "init", "-q", "--bare", "remote.git")
	gitCmd(t, tmp, "clone", "-q", "remote.git", "app")
	remote := filepath.Join(tmp, "remote.git")
	app := filepath.Join(tmp, "app")
	gitCmd(t, app, "config", "user.email", "e2e@test.local")
	gitCmd(t, app, "config", "user.name", "e2e")
	if err := os.WriteFile(filepath.Join(app, "file.txt"), []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, app, "add", ".")
	gitCmd(t, app, "commit", "-qm", "init")
	gitCmd(t, app, "push", "-q", "origin", "HEAD")

	cfg := fmt.Sprintf("base_dir: %s\nbackup:\n  patch_dir: %s\n", env.baseDir, filepath.Join(tmp, "patches"))
	if err := os.WriteFile(env.configFile, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	// Adopt the working clone (its origin is a local-path remote).
	if out, code := env.run(t, "adopt", app); code != 0 {
		t.Fatalf("adopt exit %d:\n%s", code, out)
	}

	// Dirty a tracked file, then back up + push WIP.
	if err := os.WriteFile(filepath.Join(app, "file.txt"), []byte("v1\nwip\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, code := env.run(t, "backup", "--push-branch")
	if code != 0 {
		t.Fatalf("backup --push-branch exit %d:\n%s", code, out)
	}
	if !strings.Contains(out, "git-mgr/wip/") {
		t.Errorf("expected a pushed WIP branch in output:\n%s", out)
	}

	// The WIP branch must exist on the remote as a normal branch.
	if refs := gitCmd(t, remote, "for-each-ref", "refs/heads/git-mgr/wip/"); !strings.Contains(refs, "git-mgr/wip/") {
		t.Errorf("WIP branch missing on remote: %q", refs)
	}

	// Working tree stays dirty — backup is non-destructive.
	if st := gitCmd(t, app, "status", "--porcelain"); st == "" {
		t.Error("backup should not have cleaned the working tree")
	}
}
