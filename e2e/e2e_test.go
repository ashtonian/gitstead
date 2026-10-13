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
	"strconv"
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
// httpClone clones a public repo over HTTP. The URL carries no credential:
// git-mgr refuses one in an origin, and git's credential helper is where
// credentials belong.
func (e *testEnv) httpClone(t *testing.T, owner, repo, destPath string) {
	t.Helper()
	url := fmt.Sprintf("%s/%s/%s.git", e.httpURL, owner, repo)
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

// TestE2E_StaleRepoArchived: under cleanup.policy archive, a repo deleted
// upstream goes stale on one discover --apply and, past the grace period,
// the next moves its clone into the archive and stops tracking it; the repo
// still upstream is untouched.
func TestE2E_StaleRepoArchived(t *testing.T) {
	env := setupTestEnv(t)
	token := env.createToken(t)
	env.createRepo(t, token, "keep-me", false)
	env.createRepo(t, token, "drop-me", false)
	archiveDir := filepath.Join(t.TempDir(), "archive")
	cfg := fmt.Sprintf(`base_dir: %s
clone_protocol: https
providers:
  - name: forgejo
    type: gitea
    url: %s
    token: %s
discovery:
  - name: me
    provider: forgejo
    user: %s
cleanup:
  policy: archive
  grace_period: 0s
  protect_manual: false
backup:
  archive_dir: %s
`, env.baseDir, env.httpURL, token, env.adminUser, archiveDir)
	if err := os.WriteFile(env.configFile, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	// The container advertises a clone URL it can't be reached at, so the
	// repos are cloned over its mapped port and adopted; with protect_manual
	// off, discovery judges them like any other.
	host := strings.Split(strings.TrimPrefix(env.httpURL, "http://"), ":")[0] // a key has no port
	path := func(name string) string { return filepath.Join(env.baseDir, name) }
	for _, name := range []string{"keep-me", "drop-me"} {
		env.httpClone(t, env.adminUser, name, path(name))
		if out, code := env.run(t, "adopt", path(name)); code != 0 {
			t.Fatalf("adopt exit %d:\n%s", code, out)
		}
	}
	if out, code := env.run(t, "discover", "--apply"); code != 0 {
		t.Fatalf("discover --apply exit %d:\n%s", code, out)
	}
	if repos := env.statusJSON(t); len(repos) != 2 {
		t.Fatalf("want both repos tracked, got %v", repos)
	}

	resp := env.api(t, "DELETE", "/api/v1/repos/"+env.adminUser+"/drop-me", nil, token)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("deleting drop-me: status %d", resp.StatusCode)
	}
	// The first pass marks it stale; the next, past the grace period, archives it.
	for range 2 {
		if out, code := env.run(t, "discover", "--apply"); code != 0 {
			t.Fatalf("discover --apply exit %d:\n%s", code, out)
		}
	}

	repos := env.statusJSON(t)
	if _, ok := repos[host+"/"+env.adminUser+"/drop-me"]; ok || len(repos) != 1 {
		t.Errorf("only keep-me should be tracked, got %v", repos)
	}
	if _, err := os.Lstat(path("drop-me")); !os.IsNotExist(err) {
		t.Errorf("drop-me's clone should have moved: %v", err)
	}
	if _, err := os.Stat(filepath.Join(path("keep-me"), ".git")); err != nil {
		t.Errorf("keep-me's clone must stay: %v", err)
	}
	out, _ := env.run(t, "archives", "ls")
	if !strings.Contains(out, "drop-me") || !strings.Contains(out, "the whole clone") {
		t.Errorf("archives ls should list drop-me's clone:\n%s", out)
	}
	entries, _ := os.ReadDir(archiveDir)
	var archived string
	for _, e := range entries {
		if e.IsDir() {
			archived = filepath.Join(archiveDir, e.Name())
		}
	}
	if archived == "" {
		t.Fatalf("no clone in %s", archiveDir)
	}
	if _, err := os.Stat(filepath.Join(archived, "README.md")); err != nil {
		t.Errorf("the archived clone is incomplete: %v", err)
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

// TestE2E_WorkspaceFromFlags drives `ws create` as a one-off from CLI flags:
// it adopts the local repo it names, makes a worktree per agent on the
// agent's own branch without touching config, --save writes it to config,
// and `ws rm --delete` removes the copies and leaves the repo.
func TestE2E_WorkspaceFromFlags(t *testing.T) {
	env := newLocalEnv(t)
	tmp := t.TempDir()

	// A local clone of a bare remote, with one commit.
	remote := filepath.Join(tmp, "remotes", "app.git")
	gitCmd(t, tmp, "init", "-q", "--bare", "-b", "main", remote)
	src := filepath.Join(tmp, "src", "app")
	gitCmd(t, tmp, "clone", "-q", remote, src)
	gitCmd(t, src, "config", "user.email", "e2e@test.local")
	gitCmd(t, src, "config", "user.name", "e2e")
	if err := os.WriteFile(filepath.Join(src, "README.md"), []byte("# app"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, src, "add", ".")
	gitCmd(t, src, "commit", "-qm", "init")
	gitCmd(t, src, "push", "-q", "origin", "HEAD:main")

	root := filepath.Join(tmp, "workspaces")
	cfg := fmt.Sprintf("base_dir: %s\nworkspace_defaults:\n  root: %s\n", env.baseDir, root)
	if err := os.WriteFile(env.configFile, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	out, code := env.run(t, "ws", "create", "scratch", "--repo", src, "--agents", "2")
	if code != 0 {
		t.Fatalf("ws create exit %d:\n%s", code, out)
	}
	for i := 1; i <= 2; i++ {
		copyPath := filepath.Join(root, "scratch", fmt.Sprintf("agent-%d", i))
		if got, want := gitCmd(t, copyPath, "rev-parse", "--abbrev-ref", "HEAD"), fmt.Sprintf("agents/scratch/agent-%d", i); got != want {
			t.Errorf("agent-%d is on %q, want %q", i, got, want)
		}
	}
	if b, _ := os.ReadFile(env.configFile); strings.Contains(string(b), "scratch") {
		t.Error("a one-off create should not change config without --save")
	}
	if out, code := env.run(t, "ws", "check", "scratch"); code != 0 {
		t.Errorf("ws check exit %d:\n%s", code, out)
	}

	// --save writes the workspace to config, which then declares it.
	out, code = env.run(t, "ws", "create", "scratch", "--repo", src, "--agents", "2", "--save")
	if code != 0 {
		t.Fatalf("ws create --save exit %d:\n%s", code, out)
	}
	if b, _ := os.ReadFile(env.configFile); !strings.Contains(string(b), "name: scratch") || !strings.Contains(string(b), src) {
		t.Errorf("--save did not write the workspace:\n%s", b)
	}

	// wg stays an alias.
	out, code = env.run(t, "wg", "rm", "scratch", "--delete", "--yes")
	if code != 0 {
		t.Fatalf("ws rm exit %d:\n%s", code, out)
	}
	if _, err := os.Stat(filepath.Join(root, "scratch")); !os.IsNotExist(err) {
		t.Errorf("the workspace's copies should be gone:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(src, ".git")); err != nil {
		t.Errorf("the repo itself must stay: %v", err)
	}
}

// TestE2E_SyncRefreshesClones: sync fetches a repo once and brings its
// workspace clones up to date from that clone, never fetching them itself.
func TestE2E_SyncRefreshesClones(t *testing.T) {
	env := newLocalEnv(t)
	tmp := t.TempDir()
	remote := filepath.Join(tmp, "remotes", "app.git")
	gitCmd(t, tmp, "init", "-q", "--bare", "-b", "main", remote)
	seed := filepath.Join(tmp, "seed")
	gitCmd(t, tmp, "clone", "-q", remote, seed)
	gitCmd(t, seed, "config", "user.email", "e2e@test.local")
	gitCmd(t, seed, "config", "user.name", "e2e")
	commit := func(name string) string {
		if err := os.WriteFile(filepath.Join(seed, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
		gitCmd(t, seed, "add", ".")
		gitCmd(t, seed, "commit", "-qm", name)
		gitCmd(t, seed, "push", "-q", "origin", "HEAD:main")
		return gitCmd(t, seed, "rev-parse", "HEAD")
	}
	commit("first")
	root := filepath.Join(tmp, "workspaces")
	cfg := fmt.Sprintf("base_dir: %s\nworkspace_defaults:\n  root: %s\nrepos:\n  - url: %s\n", env.baseDir, root, remote)
	if err := os.WriteFile(env.configFile, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, code := env.run(t, "sync"); code != 0 {
		t.Fatalf("first sync exit %d:\n%s", code, out)
	}
	if out, code := env.run(t, "ws", "create", "app", "--repo", "local/remotes/app", "--isolation", "clone"); code != 0 {
		t.Fatalf("ws create exit %d:\n%s", code, out)
	}
	copyPath := filepath.Join(root, "app", "agent-1")
	// The copy's own origin is unreachable: anything new must come through
	// the repo's clone.
	gitCmd(t, copyPath, "remote", "set-url", "origin", filepath.Join(tmp, "nowhere.git"))

	head := commit("second")
	out, code := env.run(t, "sync", "--force")
	if code != 0 {
		t.Fatalf("sync exit %d:\n%s", code, out)
	}
	if got := gitCmd(t, copyPath, "rev-parse", "origin/main"); got != head {
		t.Errorf("the clone's origin/main = %s, want %s\n%s", got, head, out)
	}
}

// TestE2E_ArchiveAndRestore: remove --archive moves a clone, whole, into the
// archive and catalogs it; archives restore moves it back and tracks it
// again, or puts it elsewhere when the repo is tracked again; archives rm
// deletes it.
func TestE2E_ArchiveAndRestore(t *testing.T) {
	env := newLocalEnv(t)
	tmp := t.TempDir()
	archiveDir := filepath.Join(tmp, "archive")
	remote := filepath.Join(tmp, "remotes", "app.git")
	gitCmd(t, tmp, "init", "-q", "--bare", "-b", "main", remote)
	clone := filepath.Join(env.baseDir, "app")
	cloneAt := func() {
		gitCmd(t, tmp, "clone", "-q", remote, clone)
		gitCmd(t, clone, "config", "user.email", "e2e@test.local")
		gitCmd(t, clone, "config", "user.name", "e2e")
	}
	cloneAt()
	write := func(name string) {
		if err := os.WriteFile(filepath.Join(clone, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("pushed.txt")
	gitCmd(t, clone, "add", ".")
	gitCmd(t, clone, "commit", "-qm", "pushed")
	gitCmd(t, clone, "push", "-q", "origin", "HEAD:main")
	write("unpushed.txt")
	gitCmd(t, clone, "add", ".")
	gitCmd(t, clone, "commit", "-qm", "unpushed")
	write("notes.txt")
	tip := gitCmd(t, clone, "rev-parse", "HEAD")

	cfg := fmt.Sprintf("base_dir: %s\nbackup:\n  archive_dir: %s\n", env.baseDir, archiveDir)
	if err := os.WriteFile(env.configFile, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, code := env.run(t, "adopt", clone); code != 0 {
		t.Fatalf("adopt exit %d:\n%s", code, out)
	}
	const key = "local/remotes/app"
	archive := func() string {
		t.Helper()
		out, code := env.run(t, "remove", key, "--archive", "--yes")
		if code != 0 {
			t.Fatalf("remove --archive exit %d:\n%s", code, out)
		}
		if _, err := os.Lstat(clone); !os.IsNotExist(err) {
			t.Fatalf("the clone should have moved:\n%s", out)
		}
		entries, _ := os.ReadDir(archiveDir)
		for _, e := range entries {
			if e.IsDir() {
				return e.Name()
			}
		}
		t.Fatalf("no archived clone in %s:\n%s", archiveDir, out)
		return ""
	}

	id := archive()
	if _, ok := env.statusJSON(t)[key]; ok {
		t.Error("an archived repo should no longer be tracked")
	}
	out, _ := env.run(t, "archives", "ls")
	if !strings.Contains(out, id) || !strings.Contains(out, "the whole clone, with 1 untracked file, 1 unpushed commit") {
		t.Errorf("archives ls:\n%s", out)
	}

	out, code := env.run(t, "archives", "restore", id)
	if code != 0 {
		t.Fatalf("archives restore exit %d:\n%s", code, out)
	}
	if got := gitCmd(t, clone, "rev-parse", "HEAD"); got != tip {
		t.Errorf("restored HEAD = %s, want %s", got, tip)
	}
	if _, err := os.Stat(filepath.Join(clone, "notes.txt")); err != nil {
		t.Errorf("the untracked file didn't come back: %v", err)
	}
	if r, ok := env.statusJSON(t)[key]; !ok || r["manually_added"] != true {
		t.Errorf("a restored repo is tracked by hand, got %v", r)
	}
	if out, _ := env.run(t, "archives", "ls"); !strings.Contains(out, "No archives on record.") {
		t.Errorf("a restored archive should leave the catalog:\n%s", out)
	}

	// Tracked again from a fresh clone, the repo's place is taken: the
	// archive goes elsewhere, untracked.
	id = archive()
	cloneAt()
	if out, code := env.run(t, "adopt", clone); code != 0 {
		t.Fatalf("adopt exit %d:\n%s", code, out)
	}
	if out, code := env.run(t, "archives", "restore", id); code == 0 || !strings.Contains(out, "tracked again") {
		t.Errorf("restoring over a tracked repo must fail, exit %d:\n%s", code, out)
	}
	elsewhere := filepath.Join(tmp, "elsewhere", "app")
	out, code = env.run(t, "archives", "restore", id, "--to", elsewhere)
	if code != 0 || !strings.Contains(out, "Not tracked") {
		t.Fatalf("archives restore --to exit %d:\n%s", code, out)
	}
	if got := gitCmd(t, elsewhere, "rev-parse", "HEAD"); got != tip {
		t.Errorf("restored HEAD = %s, want %s", got, tip)
	}

	id = archive()
	if out, code := env.run(t, "archives", "rm", id); code == 0 {
		t.Errorf("archives rm without a terminal must ask for --yes:\n%s", out)
	}
	if out, code := env.run(t, "archives", "rm", id, "--yes"); code != 0 {
		t.Fatalf("archives rm exit %d:\n%s", code, out)
	}
	if entries, _ := os.ReadDir(archiveDir); len(entries) != 0 {
		t.Errorf("archives rm left %d files behind", len(entries))
	}
}

// TestE2E_Hooks: sync runs post_clone in a clone it makes; ws create runs
// post_create in each copy, told where the repo's clone is; `hooks` runs
// either again, and a failing hook fails the command but keeps the copy.
func TestE2E_Hooks(t *testing.T) {
	env := newLocalEnv(t)
	tmp := t.TempDir()
	remote := filepath.Join(tmp, "remotes", "app.git")
	gitCmd(t, tmp, "init", "-q", "--bare", "-b", "main", remote)
	seed := filepath.Join(tmp, "seed")
	gitCmd(t, tmp, "clone", "-q", remote, seed)
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("# app"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, seed, "add", ".")
	gitCmd(t, seed, "-c", "user.name=e2e", "-c", "user.email=e2e@test.local", "commit", "-qm", "init")
	gitCmd(t, seed, "push", "-q", "origin", "HEAD:main")

	root := filepath.Join(tmp, "workspaces")
	cfg := fmt.Sprintf(`base_dir: %s
hooks:
  post_clone: ['printf "%%s" "$GIT_MGR_REPO" > .cloned']
workspace_defaults:
  root: %s
  hooks:
    post_create: ['cp "$GIT_MGR_SOURCE/.env" .env', 'echo "$GIT_MGR_AGENT" >> ran']
repos:
  - url: %s
`, env.baseDir, root, remote)
	if err := os.WriteFile(env.configFile, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, code := env.run(t, "sync"); code != 0 {
		t.Fatalf("sync exit %d:\n%s", code, out)
	}
	clone := filepath.Join(env.baseDir, "local", "remotes", "app")
	if got, _ := os.ReadFile(filepath.Join(clone, ".cloned")); string(got) != "local/remotes/app" {
		t.Errorf("post_clone wrote %q", got)
	}
	if err := os.WriteFile(filepath.Join(clone, ".env"), []byte("TOKEN=x\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if out, code := env.run(t, "ws", "create", "app", "--repo", "local/remotes/app", "--agents", "2"); code != 0 {
		t.Fatalf("ws create exit %d:\n%s", code, out)
	}
	copyOf := func(agent string) string { return filepath.Join(root, "app", agent) }
	for _, agent := range []string{"agent-1", "agent-2"} {
		if got, _ := os.ReadFile(filepath.Join(copyOf(agent), ".env")); string(got) != "TOKEN=x\n" {
			t.Errorf("%s: .env = %q", agent, got)
		}
	}

	out, code := env.run(t, "hooks", "post-create", "app", "agent-2")
	if code != 0 {
		t.Fatalf("hooks post-create exit %d:\n%s", code, out)
	}
	if got, _ := os.ReadFile(filepath.Join(copyOf("agent-2"), "ran")); string(got) != "agent-2\nagent-2\n" {
		t.Errorf("agent-2 ran = %q, want it twice", got)
	}
	if got, _ := os.ReadFile(filepath.Join(copyOf("agent-1"), "ran")); string(got) != "agent-1\n" {
		t.Errorf("agent-1 ran = %q, want it once", got)
	}
	os.Remove(filepath.Join(clone, ".cloned"))
	if out, code := env.run(t, "hooks", "post-clone", "local/remotes/app"); code != 0 {
		t.Fatalf("hooks post-clone exit %d:\n%s", code, out)
	}
	if _, err := os.Stat(filepath.Join(clone, ".cloned")); err != nil {
		t.Errorf("hooks post-clone didn't run: %v", err)
	}

	// A hook that fails fails the command, and the copy stays.
	os.Remove(filepath.Join(clone, ".env"))
	out, code = env.run(t, "ws", "create", "other", "--repo", "local/remotes/app")
	if code == 0 || !strings.Contains(out, "post_create") {
		t.Errorf("a failing hook must fail ws create, exit %d:\n%s", code, out)
	}
	if _, err := os.Stat(filepath.Join(root, "other", "agent-1", "README.md")); err != nil {
		t.Errorf("the copy must stay: %v", err)
	}
	if out, _ := env.run(t, "ws", "ls"); !strings.Contains(out, "other") {
		t.Errorf("the workspace must be recorded:\n%s", out)
	}
}

// TestE2E_AgentEnvironment: each agent gets ports of its own, kept, and its
// workspace's env rendered for it -- in ws env, ws exec, post_create hooks
// and its run, where `ws waiting` records it waiting for you; ws rm --delete
// takes the workspace's shared cache with it.
func TestE2E_AgentEnvironment(t *testing.T) {
	env := newLocalEnv(t)
	tmp := t.TempDir()
	t.Setenv("SHELL", "/bin/sh") // ws run's interactive shell, not the developer's
	remote := filepath.Join(tmp, "remotes", "app.git")
	gitCmd(t, tmp, "init", "-q", "--bare", "-b", "main", remote)
	seed := filepath.Join(tmp, "seed")
	gitCmd(t, tmp, "clone", "-q", remote, seed)
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("# app"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, seed, "add", ".")
	gitCmd(t, seed, "-c", "user.name=e2e", "-c", "user.email=e2e@test.local", "commit", "-qm", "init")
	gitCmd(t, seed, "push", "-q", "origin", "HEAD:main")

	root, cache := filepath.Join(tmp, "workspaces"), filepath.Join(tmp, "cache")
	cfg := fmt.Sprintf(`base_dir: %s
workspace_defaults:
  root: %s
  cache_root: %s
  ports: {base: 47100, count: 5}
  env:
    PORT: "{{.Port}}"
    API_PORT: "{{add .Port 1}}"
    DB: "app_{{.N}}"
  hooks:
    post_create: ['echo "$PORT $DB" > hooked']
repos:
  - url: %s
`, env.baseDir, root, cache, remote)
	if err := os.WriteFile(env.configFile, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, code := env.run(t, "sync"); code != 0 {
		t.Fatalf("sync exit %d:\n%s", code, out)
	}
	if out, code := env.run(t, "ws", "create", "app", "--repo", "local/remotes/app", "--agents", "2"); code != 0 {
		t.Fatalf("ws create exit %d:\n%s", code, out)
	}

	exports := func(agent string) map[string]string {
		t.Helper()
		out, code := env.run(t, "ws", "env", "app", agent, "--json")
		if code != 0 {
			t.Fatalf("ws env exit %d:\n%s", code, out)
		}
		var vars map[string]string
		if err := json.Unmarshal([]byte(out), &vars); err != nil {
			t.Fatalf("ws env --json: %v\n%s", err, out)
		}
		return vars
	}
	one, two := exports("agent-1"), exports("agent-2")
	if one["PORT"] == two["PORT"] || one["PORT"] == "" || one["GIT_MGR_PORT"] != one["PORT"] {
		t.Errorf("each agent needs ports of its own: %v / %v", one, two)
	}
	if p, _ := strconv.Atoi(two["PORT"]); two["API_PORT"] != strconv.Itoa(p+1) || two["DB"] != "app_2" {
		t.Errorf("agent-2's env = %v", two)
	}
	if again := exports("agent-1"); again["PORT"] != one["PORT"] {
		t.Errorf("an agent keeps its ports: %s then %s", one["PORT"], again["PORT"])
	}
	copy2 := filepath.Join(root, "app", "agent-2")
	if got, _ := os.ReadFile(filepath.Join(copy2, "hooked")); strings.TrimSpace(string(got)) != two["PORT"]+" app_2" {
		t.Errorf("post_create saw %q, want %q", got, two["PORT"]+" app_2")
	}
	out, code := env.run(t, "ws", "exec", "app", "--agent", "agent-1", "--", "sh", "-c", "echo port=$PORT")
	if code != 0 || !strings.Contains(out, "port="+one["PORT"]) {
		t.Errorf("ws exec exit %d:\n%s", code, out)
	}

	// Inside its run, the agent says it waits; ws status shows it.
	script := fmt.Sprintf("%s ws waiting --message 'needs a decision' && %s ws status app > status.txt", env.bin, env.bin)
	if out, code := env.run(t, "ws", "run", "app", "agent-1", "--no-check", "--no-shell", "--command", script); code != 0 {
		t.Fatalf("ws run exit %d:\n%s", code, out)
	}
	status, _ := os.ReadFile(filepath.Join(root, "app", "agent-1", "status.txt"))
	if !strings.Contains(string(status), "waiting since") || !strings.Contains(string(status), "needs a decision") {
		t.Errorf("ws status during the run:\n%s", status)
	}
	if out, code := env.run(t, "ws", "waiting", "--message", "x"); code != 0 || strings.TrimSpace(out) != "" {
		t.Errorf("outside a run, ws waiting does nothing, quietly: exit %d %q", code, out)
	}

	if _, err := os.Stat(filepath.Join(cache, "app")); err != nil {
		t.Fatalf("the shared cache should exist: %v", err)
	}
	if out, code := env.run(t, "ws", "rm", "app", "--delete", "--yes"); code != 0 {
		t.Fatalf("ws rm exit %d:\n%s", code, out)
	}
	if _, err := os.Stat(filepath.Join(cache, "app")); !os.IsNotExist(err) {
		t.Errorf("ws rm --delete should take the cache: %v", err)
	}
}

// TestE2E_PublishAndRetire: publish pushes an agent's branch to its remote;
// retire archives the copy's uncommitted work, removes it, and catalogs the
// archive.
func TestE2E_PublishAndRetire(t *testing.T) {
	env := newLocalEnv(t)
	tmp := t.TempDir()
	remote := filepath.Join(tmp, "remotes", "app.git")
	gitCmd(t, tmp, "init", "-q", "--bare", "-b", "main", remote)
	src := filepath.Join(tmp, "src", "app")
	gitCmd(t, tmp, "clone", "-q", remote, src)
	gitCmd(t, src, "-c", "user.email=e2e@test.local", "-c", "user.name=e2e", "commit", "-q", "--allow-empty", "-m", "init")
	gitCmd(t, src, "push", "-q", "origin", "HEAD:main")
	root := filepath.Join(tmp, "workspaces")
	cfg := fmt.Sprintf("base_dir: %s\nworkspace_defaults:\n  root: %s\n", env.baseDir, root)
	if err := os.WriteFile(env.configFile, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, code := env.run(t, "ws", "create", "app", "--repo", src, "--agents", "2"); code != 0 {
		t.Fatalf("ws create exit %d:\n%s", code, out)
	}
	a1 := filepath.Join(root, "app", "agent-1")
	gitCmd(t, a1, "-c", "user.email=e2e@test.local", "-c", "user.name=e2e", "commit", "-q", "--allow-empty", "-m", "agent work")

	if out, code := env.run(t, "ws", "publish", "app", "agent-1"); code != 0 {
		t.Fatalf("ws publish exit %d:\n%s", code, out)
	}
	if got := gitCmd(t, remote, "rev-parse", "refs/heads/agents/app/agent-1"); got != gitCmd(t, a1, "rev-parse", "HEAD") {
		t.Errorf("the remote's agents/app/agent-1 = %s", got)
	}
	if got := gitCmd(t, a1, "rev-parse", "--abbrev-ref", "@{u}"); got != "origin/agents/app/agent-1" {
		t.Errorf("upstream = %s", got)
	}

	if err := os.WriteFile(filepath.Join(a1, "notes.txt"), []byte("unsaved"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, code := env.run(t, "ws", "retire", "app", "agent-1", "--yes")
	if code != 0 || !strings.Contains(out, "archived to") {
		t.Fatalf("ws retire exit %d:\n%s", code, out)
	}
	if _, err := os.Stat(a1); !os.IsNotExist(err) {
		t.Error("agent-1's copy is still there")
	}
	out, code = env.run(t, "archives", "ls", "--workspace", "app")
	if code != 0 || !strings.Contains(out, "app/agent-1") || !strings.Contains(out, "untracked") {
		t.Errorf("archives ls exit %d:\n%s", code, out)
	}
	if out, _ := env.run(t, "ws", "status", "app"); !strings.Contains(out, "retired app/agent-1") {
		t.Errorf("ws status doesn't show the retirement:\n%s", out)
	}
}

// TestE2E_RemoteSync adopts a repo whose origin is a local bare remote, gives
// it uncommitted work and an unpushed commit, and runs `sync` with
// remote_sync on: both land on the remote under the copy's hidden namespace,
// no branch is pushed, and the working tree is untouched.
func TestE2E_RemoteSync(t *testing.T) {
	env := newLocalEnv(t)
	tmp := t.TempDir()

	gitCmd(t, tmp, "init", "-q", "--bare", "-b", "main", "remote.git")
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

	cfg := fmt.Sprintf("base_dir: %s\nsync:\n  auto_discover: false\nbackup:\n  patch_dir: %s\nremote_sync:\n  enabled: true\n",
		env.baseDir, filepath.Join(tmp, "patches"))
	if err := os.WriteFile(env.configFile, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, code := env.run(t, "adopt", app); code != 0 {
		t.Fatalf("adopt exit %d:\n%s", code, out)
	}

	// An unpushed commit, then uncommitted work on top.
	if err := os.WriteFile(filepath.Join(app, "file.txt"), []byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, app, "commit", "-qam", "local only")
	if err := os.WriteFile(filepath.Join(app, "file.txt"), []byte("v2\nwip\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, code := env.run(t, "sync", "--no-fetch"); code != 0 {
		t.Fatalf("sync exit %d:\n%s", code, out)
	}

	refs := gitCmd(t, remote, "for-each-ref", "--format=%(refname)", "refs/git-mgr/")
	if !strings.Contains(refs, "/wip/") || !strings.Contains(refs, "/heads/main") {
		t.Errorf("remote refs after sync:\n%s\nwant a WIP ref and the unpushed main", refs)
	}
	if heads := gitCmd(t, remote, "for-each-ref", "--format=%(refname)", "refs/heads/"); heads != "refs/heads/main" {
		t.Errorf("sync pushed branches: %s", heads)
	}
	if got := gitCmd(t, remote, "rev-parse", "main"); got == gitCmd(t, app, "rev-parse", "HEAD") {
		t.Error("the unpushed commit was pushed to main itself")
	}
	if st := gitCmd(t, app, "status", "--porcelain"); st == "" {
		t.Error("sync should not have cleaned the working tree")
	}

	// The remote alone gives everything back: the unpushed commit, then the
	// uncommitted work on top of it.
	var wipRef, headRef string
	for _, r := range strings.Split(refs, "\n") {
		switch {
		case strings.Contains(r, "/wip/"):
			wipRef = r
		case strings.HasSuffix(r, "/heads/main"):
			headRef = r
		}
	}
	gitCmd(t, tmp, "clone", "-q", "remote.git", "restore")
	restore := filepath.Join(tmp, "restore")
	gitCmd(t, restore, "fetch", "-q", "origin", headRef+":refs/heads/recovered")
	gitCmd(t, restore, "checkout", "-q", "recovered")
	gitCmd(t, restore, "fetch", "-q", "origin", wipRef)
	gitCmd(t, restore, "stash", "apply", "-q", "--index", "FETCH_HEAD")
	if got, _ := os.ReadFile(filepath.Join(restore, "file.txt")); string(got) != "v2\nwip\n" {
		t.Errorf("restored file.txt = %q", got)
	}

	if out, code := env.run(t, "remote-sync", "--list"); code != 0 || !strings.Contains(out, "/wip/") {
		t.Errorf("remote-sync --list exit %d:\n%s", code, out)
	}
}
