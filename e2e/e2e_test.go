// Package e2e contains gitten's end-to-end tests. They spin up a real
// Forgejo instance via testcontainers, seed it over the API, then drive the
// compiled gitten binary as a subprocess and assert on its output, exit
// code, and the state it writes. Nothing here imports gitten's internal
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
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go/modules/forgejo"
)

// --- isolation ---

// binHome is the home directory gitten runs with in these tests, so its
// caches and lock files never land in the real one. The test process keeps
// its own HOME, where the Docker client finds its context.
var binHome string

// TestMain keeps git -- the tests' and gitten's -- from the developer's git
// config and from any repository the temporary directory sits inside.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "gitten-e2e-home-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	binHome = home
	ceilings := []string{os.TempDir()}
	if real, err := filepath.EvalSymlinks(os.TempDir()); err == nil && real != os.TempDir() {
		ceilings = append(ceilings, real)
	}
	os.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	os.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	os.Setenv("GIT_CEILING_DIRECTORIES", strings.Join(ceilings, string(os.PathListSeparator)))
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}

// binEnv is the environment gitten runs with: this process's, with
// binHome as its home, then extra.
func binEnv(extra ...string) []string {
	env := slices.DeleteFunc(os.Environ(), func(kv string) bool {
		name, _, _ := strings.Cut(kv, "=")
		return name == "HOME" || strings.HasPrefix(name, "XDG_")
	})
	env = append(env, "HOME="+binHome, "XDG_CACHE_HOME="+filepath.Join(binHome, ".cache"),
		"XDG_CONFIG_HOME="+filepath.Join(binHome, ".config"), "XDG_DATA_HOME="+filepath.Join(binHome, ".local", "share"))
	return append(env, extra...)
}

// --- gitten binary (built once for the whole suite) ---

var (
	binOnce sync.Once
	binPath string
	binErr  error
)

// gittenBin builds the gitten binary from the parent module once and returns
// its path. Building (rather than `go run`) keeps each invocation cheap.
func gittenBin(t *testing.T) string {
	t.Helper()
	binOnce.Do(func() {
		dir, err := os.MkdirTemp("", "gitten-bin")
		if err != nil {
			binErr = err
			return
		}
		binPath = filepath.Join(dir, "gitten")
		// -C .. builds the root module; the e2e module has no code dependency on it.
		cmd := exec.Command("go", "build", "-C", "..", "-o", binPath, ".")
		if out, err := cmd.CombinedOutput(); err != nil {
			binErr = fmt.Errorf("building gitten: %w\n%s", err, out)
		}
	})
	if binErr != nil {
		t.Fatalf("gitten binary unavailable: %v", binErr)
	}
	return binPath
}

// --- test environment ---

// testEnv holds a running Forgejo instance and the paths gitten writes to.
type testEnv struct {
	httpURL    string
	adminUser  string
	adminPass  string
	bin        string
	baseDir    string
	stateFile  string
	configFile string
	// extraEnv is added to gitten's environment.
	extraEnv []string
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
		bin:        gittenBin(t),
		baseDir:    filepath.Join(tmp, "repos"),
		stateFile:  filepath.Join(tmp, "state.yaml"),
		configFile: filepath.Join(tmp, "config.yaml"),
	}
}

// run executes gitten with the given args, pointing it at this env's config
// and state files, and returns combined output plus the exit code.
func (e *testEnv) run(t *testing.T, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(e.bin, args...)
	cmd.Env = binEnv(append([]string{"GITTEN_CONFIG=" + e.configFile, "GITTEN_STATE=" + e.stateFile}, e.extraEnv...)...)
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
			t.Fatalf("running gitten %v: %v", args, err)
		}
	}
	return buf.String(), code
}

// runJSON runs gitten with --json and returns its stdout, which must be
// exactly one JSON document whatever the outcome, decoded into v; its
// stderr; and its exit code.
func (e *testEnv) runJSON(t *testing.T, v any, args ...string) (stderr string, code int) {
	t.Helper()
	cmd := exec.Command(e.bin, append([]string{"--json"}, args...)...)
	cmd.Env = binEnv(append([]string{"GITTEN_CONFIG=" + e.configFile, "GITTEN_STATE=" + e.stateFile}, e.extraEnv...)...)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatalf("running gitten %v: %v", args, err)
		}
		code = ee.ExitCode()
	}
	if v == nil {
		v = new(json.RawMessage)
	}
	text := out.String()
	dec := json.NewDecoder(&out)
	if err := dec.Decode(v); err != nil {
		t.Fatalf("gitten %v --json: stdout isn't JSON: %v\nstdout: %s\nstderr: %s", args, err, text, errOut.String())
	}
	if err := dec.Decode(new(json.RawMessage)); !errors.Is(err, io.EOF) {
		t.Fatalf("gitten %v --json printed more than one document (%v):\n%s", args, err, text)
	}
	return errOut.String(), code
}

// statusJSON runs `gitten status --json` and decodes it into a map keyed by
// repo key. Asserting via the CLI's own JSON output keeps the tests decoupled
// from the on-disk state format.
func (e *testEnv) statusJSON(t *testing.T) map[string]map[string]any {
	t.Helper()
	m := map[string]map[string]any{}
	if stderr, code := e.runJSON(t, &m, "status"); code != 0 {
		t.Fatalf("status --json exit %d:\n%s", code, stderr)
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
// Forgejo container only exposes HTTP conveniently; gitten's own clone path is
// SSH, so adopt/backup/sync tests operate on these pre-cloned working trees.
// httpClone clones a public repo over HTTP. The URL carries no credential:
// gitten refuses one in an origin, and git's credential helper is where
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

// writeConfig writes a gitten config pointing a "gitea" provider at the
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
// through the real binary. (gitten's default transport is SSH, which the
// container doesn't expose, so this also proves the protocol switch works.)
// A private repo is cloned with what the user's git credential helper
// supplies, and its origin keeps no credential.
func TestE2E_DiscoverApplyHTTPS(t *testing.T) {
	env := setupTestEnv(t)
	token := env.createToken(t)
	env.createRepo(t, token, "http-repo", false) // public: cloneable over HTTP without auth
	env.createRepo(t, token, "private-repo", true)

	cfg := fmt.Sprintf(`base_dir: %s
clone_protocol: https
concurrency: 2
accounts:
  - name: me
    hosts: [localhost]
    git:
      name: Discovered Me
      email: me@forgejo.example
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
	// The container advertises its internal URL, localhost:3000, in clone
	// URLs; git -- not gitten -- is told where that is mapped to, and given
	// a credential helper, as a user's git config would.
	env.extraEnv = []string{
		"GIT_CONFIG_COUNT=2",
		"GIT_CONFIG_KEY_0=url." + strings.TrimSuffix(env.httpURL, "/") + "/.insteadOf",
		"GIT_CONFIG_VALUE_0=http://localhost:3000/",
		"GIT_CONFIG_KEY_1=credential.helper",
		fmt.Sprintf("GIT_CONFIG_VALUE_1=!f() { test \"$1\" = get && echo username=%s && echo password=%s; }; f", env.adminUser, token),
	}

	out, code := env.run(t, "discover", "--apply")
	if code != 0 {
		t.Fatalf("discover --apply exit %d:\n%s", code, out)
	}
	clone := filepath.Join(env.baseDir, "localhost", env.adminUser, "http-repo")
	origin := gitCmd(t, clone, "remote", "get-url", "origin")
	if !strings.HasPrefix(origin, "http://") {
		t.Errorf("origin = %s, want the HTTP URL under clone_protocol: https", origin)
	}
	if got := gitCmd(t, clone, "config", "--local", "user.email"); got != "me@forgejo.example" {
		t.Errorf("user.email = %q, want the account's", got)
	}
	if st := env.statusJSON(t)["localhost/"+env.adminUser+"/http-repo"]; st == nil || st["cloned"] != true {
		t.Errorf("status: %v", st)
	}

	private := filepath.Join(env.baseDir, "localhost", env.adminUser, "private-repo")
	if _, err := os.Stat(filepath.Join(private, "README.md")); err != nil {
		t.Errorf("the private repo wasn't cloned with the credential helper's token: %v", err)
	}
	if origin := gitCmd(t, private, "remote", "get-url", "origin"); strings.Contains(origin, token) || strings.Contains(origin, "@") {
		t.Errorf("the private repo's origin = %s, want no credential in it", origin)
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
		bin:        gittenBin(t),
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
  post_clone: ['printf "%%s" "$GITTEN_REPO" > .cloned']
workspace_defaults:
  root: %s
  hooks:
    post_create: ['cp "$GITTEN_SOURCE/.env" .env', 'echo "$GITTEN_AGENT" >> ran']
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
	if one["PORT"] == two["PORT"] || one["PORT"] == "" || one["GITTEN_PORT"] != one["PORT"] {
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

// TestE2E_SharedManifest: two machines read one manifest. A repo cloned or
// adopted by hand on one is saved in it and cloned on the other by sync, at
// the same place relative to base_dir; one removed by hand leaves it; one
// tracked before the manifest existed is saved with manifest save.
func TestE2E_SharedManifest(t *testing.T) {
	tmp := t.TempDir()
	newRemote := func(name string) string {
		remote := filepath.Join(tmp, "remotes", name+".git")
		gitCmd(t, tmp, "init", "-q", "--bare", "-b", "main", remote)
		seed := filepath.Join(tmp, "seed-"+name)
		gitCmd(t, tmp, "clone", "-q", remote, seed)
		if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
		gitCmd(t, seed, "add", ".")
		gitCmd(t, seed, "-c", "user.name=e2e", "-c", "user.email=e2e@test.local", "commit", "-qm", "init")
		gitCmd(t, seed, "push", "-q", "origin", "HEAD:main")
		return remote
	}
	app, notes, old := newRemote("app"), newRemote("notes"), newRemote("old")
	manifest := filepath.Join(tmp, "dotfiles", "gitten", "repos.yaml")
	configure := func(e *testEnv, withManifest bool) {
		cfg := "base_dir: " + e.baseDir + "\n"
		if withManifest {
			cfg += "manifest: " + manifest + "\n"
		}
		if err := os.WriteFile(e.configFile, []byte(cfg), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	a, b := newLocalEnv(t), newLocalEnv(t)
	configure(b, true)

	// Tracked on a before it shared a manifest.
	configure(a, false)
	if out, code := a.run(t, "clone", old); code != 0 {
		t.Fatalf("clone exit %d:\n%s", code, out)
	}
	configure(a, true)
	if out, _ := a.run(t, "doctor"); !strings.Contains(out, "manifest save") {
		t.Errorf("doctor should name repos only this machine tracks:\n%s", out)
	}
	if out, code := a.run(t, "manifest", "save"); code != 0 || !strings.Contains(out, "Saved 1 repos") {
		t.Fatalf("manifest save exit %d:\n%s", code, out)
	}

	if out, code := a.run(t, "clone", app, "--tag", "work"); code != 0 {
		t.Fatalf("clone exit %d:\n%s", code, out)
	}
	custom := filepath.Join(a.baseDir, "personal", "notes")
	gitCmd(t, tmp, "clone", "-q", notes, custom)
	if out, code := a.run(t, "adopt", custom); code != 0 {
		t.Fatalf("adopt exit %d:\n%s", code, out)
	}
	data, _ := os.ReadFile(manifest)
	for _, want := range []string{app, notes, old, "path: personal/notes", "- work"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("manifest lacks %q:\n%s", want, data)
		}
	}

	if out, code := b.run(t, "sync"); code != 0 {
		t.Fatalf("sync on b exit %d:\n%s", code, out)
	}
	for _, p := range []string{
		filepath.Join(b.baseDir, "local", "remotes", "app", "README.md"),
		filepath.Join(b.baseDir, "personal", "notes", "README.md"),
		filepath.Join(b.baseDir, "local", "remotes", "old", "README.md"),
	} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("b should have %s: %v", p, err)
		}
	}

	if out, code := a.run(t, "remove", "local/remotes/app", "--yes"); code != 0 {
		t.Fatalf("remove exit %d:\n%s", code, out)
	}
	if data, _ := os.ReadFile(manifest); strings.Contains(string(data), app) {
		t.Errorf("a removed repo should leave the manifest:\n%s", data)
	}
	if _, ok := b.statusJSON(t)["local/remotes/app"]; !ok {
		t.Error("b keeps tracking a repo the manifest dropped, as with a repos: entry")
	}
}

// TestE2E_Mirror: sync mirrors each repo mirroring is on for, once per
// mirror.interval; `mirror --force` updates them now; a mirror that can't
// update is reported by status until one can.
func TestE2E_Mirror(t *testing.T) {
	env := newLocalEnv(t)
	tmp := t.TempDir()
	remote := filepath.Join(tmp, "remotes", "app.git")
	gitCmd(t, tmp, "init", "-q", "--bare", "-b", "main", remote)
	seed := filepath.Join(tmp, "seed")
	gitCmd(t, tmp, "clone", "-q", remote, seed)
	commit := func(msg string) string {
		if err := os.WriteFile(filepath.Join(seed, msg), []byte(msg), 0o644); err != nil {
			t.Fatal(err)
		}
		gitCmd(t, seed, "add", ".")
		gitCmd(t, seed, "-c", "user.name=e2e", "-c", "user.email=e2e@test.local", "commit", "-qm", msg)
		gitCmd(t, seed, "push", "-q", "origin", "HEAD:main")
		return gitCmd(t, seed, "rev-parse", "HEAD")
	}
	commit("one")
	mirrors := filepath.Join(tmp, "backup-disk")
	cfg := fmt.Sprintf("base_dir: %s\nmirror: {enabled: true, dir: %s}\nrepos:\n  - url: %s\n", env.baseDir, mirrors, remote)
	if err := os.WriteFile(env.configFile, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	out, code := env.run(t, "sync")
	if code != 0 || !strings.Contains(out, "1 mirrored") {
		t.Fatalf("sync exit %d:\n%s", code, out)
	}
	mirror := filepath.Join(mirrors, "local", "remotes", "app.git")
	if got := gitCmd(t, mirror, "config", "--bool", "remote.origin.mirror"); got != "true" {
		t.Fatalf("not a mirror: %s", got)
	}

	tip := commit("two")
	if out, _ := env.run(t, "mirror"); !strings.Contains(out, "1 skipped") {
		t.Errorf("a fresh mirror waits for mirror.interval:\n%s", out)
	}
	if out, code := env.run(t, "mirror", "--force"); code != 0 || !strings.Contains(out, "updated") {
		t.Fatalf("mirror --force exit %d:\n%s", code, out)
	}
	if got := gitCmd(t, mirror, "rev-parse", "refs/heads/main"); got != tip {
		t.Errorf("mirror main = %s, want %s", got, tip)
	}

	if err := os.Rename(remote, remote+".gone"); err != nil {
		t.Fatal(err)
	}
	if _, code := env.run(t, "mirror", "--force"); code == 0 {
		t.Error("a mirror that can't update must fail the command")
	}
	if out, _ := env.run(t, "status"); !strings.Contains(out, "1 mirrors failing") {
		t.Errorf("status should report the failing mirror:\n%s", out)
	}
	if got := gitCmd(t, mirror, "rev-parse", "refs/heads/main"); got != tip {
		t.Errorf("a failed update must leave the mirror as it was: %s", got)
	}
}

// TestE2E_JSON: under --json each command prints one JSON document on
// stdout -- people's output goes to stderr -- with exit codes that say what
// happened: 0, 2 for a check that found problems, 3 for a partial failure,
// 1 for a command that has no JSON output or needs --yes.
func TestE2E_JSON(t *testing.T) {
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
	cfg := fmt.Sprintf("base_dir: %s\nworkspace_defaults: {root: %s}\nrepos:\n  - url: %s\n", env.baseDir, root, remote)
	if err := os.WriteFile(env.configFile, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	var sync struct {
		Changes struct {
			Changes []struct {
				Kind, Target string
				OK           bool
			}
		}
		Copies  []map[string]any
		Fetched int
	}
	if stderr, code := env.runJSON(t, &sync, "sync"); code != 0 {
		t.Fatalf("sync exit %d:\n%s", code, stderr)
	}
	if len(sync.Changes.Changes) != 1 || sync.Changes.Changes[0].Kind != "clone" || !sync.Changes.Changes[0].OK || len(sync.Copies) != 1 {
		t.Errorf("sync --json = %+v", sync)
	}

	var created struct{ Changes []map[string]any }
	if stderr, code := env.runJSON(t, &created, "ws", "create", "app", "--repo", "local/remotes/app", "--agents", "2"); code != 0 || len(created.Changes) != 3 {
		t.Fatalf("ws create exit %d, %+v:\n%s", code, created, stderr)
	}
	var listed []struct {
		Name   string
		Agents int
		Status string
	}
	env.runJSON(t, &listed, "ws", "ls")
	if len(listed) != 1 || listed[0].Name != "app" || listed[0].Agents != 2 || listed[0].Status != "created" {
		t.Errorf("ws ls --json = %+v", listed)
	}
	var target map[string]string
	env.runJSON(t, &target, "path", "app", "agent-2")
	if target["path"] != filepath.Join(root, "app", "agent-2") || target["kind"] != "agent" {
		t.Errorf("path --json = %v", target)
	}

	var checked []map[string]any
	if _, code := env.runJSON(t, &checked, "ws", "check", "app"); code != 0 || len(checked) != 2 {
		t.Errorf("ws check: exit %d, %d copies; want 0 and 2", code, len(checked))
	}
	// A copy gone from disk: the check ran, and found a problem.
	if err := os.RemoveAll(filepath.Join(root, "app", "agent-2")); err != nil {
		t.Fatal(err)
	}
	if _, code := env.runJSON(t, &checked, "ws", "check", "app"); code != 2 {
		t.Errorf("ws check with a copy gone: exit %d, want 2", code)
	}
	if out, code := env.run(t, "ws", "ensure", "app"); code != 0 {
		t.Fatalf("ws ensure exit %d:\n%s", code, out)
	}

	// One copy fails, the other succeeds: a partial failure.
	var ran []struct {
		Path string
		OK   bool
	}
	if _, code := env.runJSON(t, &ran, "ws", "exec", "app", "--", "sh", "-c", `test "$GITTEN_AGENT" = agent-1`); code != 3 || len(ran) != 2 {
		t.Errorf("ws exec: exit %d, %+v; want 3", code, ran)
	}

	var doctor struct {
		Checks   []struct{ Level, Message string }
		Problems int
	}
	env.runJSON(t, &doctor, "doctor")
	if len(doctor.Checks) == 0 {
		t.Errorf("doctor --json = %+v", doctor)
	}
	if stderr, code := env.runJSON(t, nil, "ws", "run", "app", "agent-1"); code != 1 || !strings.Contains(stderr, "no JSON output") {
		t.Errorf("ws run --json: exit %d:\n%s", code, stderr)
	}
	if stderr, code := env.runJSON(t, nil, "remove", "local/remotes/app"); code != 1 || !strings.Contains(stderr, "--yes") {
		t.Errorf("remove --json without --yes: exit %d:\n%s", code, stderr)
	}
	var version map[string]string
	if _, code := env.runJSON(t, &version, "version"); code != 0 || version["version"] == "" {
		t.Errorf("version --json = %v", version)
	}
}

// TestE2E_RemoteFleetJSON: a fleet on another host is planned, shown and
// stopped by gitten there, over ssh -- here an ssh that runs the command on
// this machine with the "remote" config -- and under --json its document
// comes through as this command's, alone on stdout.
func TestE2E_RemoteFleetJSON(t *testing.T) {
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

	// The host's own config: the workspace lives there, its fleet is local.
	root := filepath.Join(tmp, "box", "workspaces")
	hostConfig, hostState := filepath.Join(tmp, "box", "config.yaml"), filepath.Join(tmp, "box", "state.yaml")
	hostYAML := fmt.Sprintf(`base_dir: %s
workspace_defaults: {root: %s}
repos:
  - url: %s
workspaces:
  - {name: app, repos: [local/remotes/app], agents: 2}
fleets:
  - {name: far, workspaces: [app], launcher: tmux}
`, filepath.Join(tmp, "box", "repos"), root, remote)
	if err := os.MkdirAll(filepath.Dir(hostConfig), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hostConfig, []byte(hostYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	box := &testEnv{bin: env.bin, configFile: hostConfig, stateFile: hostState}
	if out, code := box.run(t, "sync"); code != 0 {
		t.Fatalf("sync on the host exit %d:\n%s", code, out)
	}
	if out, code := box.run(t, "apply", "--yes"); code != 0 {
		t.Fatalf("apply on the host exit %d:\n%s", code, out)
	}

	// An ssh that runs the remote line here: options and the host dropped.
	bin := filepath.Join(tmp, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	fakeSSH := "#!/bin/sh\nwhile [ $# -gt 0 ]; do case $1 in -t) shift;; -o) shift 2;; *) break;; esac; done\nshift\nexec sh -c \"$*\"\n"
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(fakeSSH), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	// The launcher is named, not detected: the test needs no tmux installed.
	localYAML := fmt.Sprintf("fleets:\n  - {name: far, workspaces: [app], host: box, launcher: tmux, remote_command: '%s --config %s --state %s'}\n", env.bin, hostConfig, hostState)
	if err := os.WriteFile(env.configFile, []byte(localYAML), 0o600); err != nil {
		t.Fatal(err)
	}

	var status map[string][]struct {
		Agent string `json:"agent"`
		Path  string `json:"path"`
	}
	if stderr, code := env.runJSON(t, &status, "fleet", "status", "far"); code != 0 {
		t.Fatalf("fleet status exit %d:\n%s", code, stderr)
	}
	if len(status["app"]) != 2 || !strings.HasPrefix(status["app"][0].Path, root) {
		t.Errorf("the host's copies, through ssh: %+v", status)
	}
	var plan struct {
		Fleet      string
		Workspaces []struct {
			Name   string
			Agents []struct{ Name, Dir string }
		}
	}
	if stderr, code := env.runJSON(t, &plan, "fleet", "open", "far", "--print", "--cmd", "echo hi"); code != 0 {
		t.Fatalf("fleet open --print exit %d:\n%s", code, stderr)
	}
	if len(plan.Workspaces) != 1 || len(plan.Workspaces[0].Agents) != 2 || !strings.HasPrefix(plan.Workspaces[0].Agents[0].Dir, root) {
		t.Errorf("the plan the host made: %+v", plan)
	}
	var stopped []map[string]any
	if stderr, code := env.runJSON(t, &stopped, "fleet", "stop", "far"); code != 0 || len(stopped) != 0 {
		t.Errorf("fleet stop with nothing running: exit %d, %v:\n%s", code, stopped, stderr)
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

	refs := gitCmd(t, remote, "for-each-ref", "--format=%(refname)", "refs/gitten/")
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

// TestE2E_RemoteSyncSubmodules: a submodule's uncommitted work and the
// commits it has that no remote does go offsite to the submodule's own
// remote, under its own copy's namespace, and come back from that remote
// alone; a dry run plans from the latest backup, and --list shows them.
func TestE2E_RemoteSyncSubmodules(t *testing.T) {
	env := newLocalEnv(t)
	tmp := t.TempDir()
	seed := func(name string) string {
		gitCmd(t, tmp, "init", "-q", "--bare", "-b", "main", name+".git")
		dir := filepath.Join(tmp, "seed-"+name)
		gitCmd(t, tmp, "clone", "-q", name+".git", dir)
		if err := os.WriteFile(filepath.Join(dir, name+".txt"), []byte("v1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		gitCmd(t, dir, "add", ".")
		gitCmd(t, dir, "-c", "user.email=e@e", "-c", "user.name=e", "commit", "-qm", "init")
		gitCmd(t, dir, "push", "-q", "origin", "HEAD")
		return dir
	}
	seed("lib")
	top := seed("app")
	gitCmd(t, top, "-c", "protocol.file.allow=always", "submodule", "add", "-q", filepath.Join(tmp, "lib.git"), "lib")
	gitCmd(t, top, "-c", "user.email=e@e", "-c", "user.name=e", "commit", "-qm", "lib")
	gitCmd(t, top, "push", "-q", "origin", "HEAD")
	app := filepath.Join(tmp, "app")
	gitCmd(t, tmp, "-c", "protocol.file.allow=always", "clone", "-q", "--recurse-submodules", "app.git", app)
	lib := filepath.Join(app, "lib")

	cfg := fmt.Sprintf("base_dir: %s\nsync:\n  auto_discover: false\nbackup:\n  patch_dir: %s\nremote_sync:\n  enabled: true\n",
		env.baseDir, filepath.Join(tmp, "patches"))
	if err := os.WriteFile(env.configFile, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, code := env.run(t, "adopt", app); code != 0 {
		t.Fatalf("adopt exit %d:\n%s", code, out)
	}

	// In the submodule, on its detached HEAD: a commit no remote has, then
	// uncommitted work on top.
	if err := os.WriteFile(filepath.Join(lib, "lib.txt"), []byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, lib, "-c", "user.email=e@e", "-c", "user.name=e", "commit", "-qam", "local only")
	if err := os.WriteFile(filepath.Join(lib, "lib.txt"), []byte("v2\nwip\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var synced struct {
		Copies []struct {
			OffsiteSubmodules map[string][]string `json:"offsite_submodules"`
		} `json:"copies"`
	}
	if stderr, code := env.runJSON(t, &synced, "sync", "--no-fetch"); code != 0 {
		t.Fatalf("sync exit %d:\n%s", code, stderr)
	}
	if len(synced.Copies) != 1 || len(synced.Copies[0].OffsiteSubmodules["lib"]) == 0 {
		t.Errorf("sync --json should report lib's offsite push: %+v", synced)
	}

	libRemote := filepath.Join(tmp, "lib.git")
	refs := gitCmd(t, libRemote, "for-each-ref", "--format=%(refname)", "refs/gitten/")
	var wipRef, headRef string
	for _, r := range strings.Split(refs, "\n") {
		switch {
		case strings.Contains(r, "/wip/"):
			wipRef = r
		case strings.HasSuffix(r, "/heads/HEAD"):
			headRef = r
		}
	}
	if wipRef == "" || headRef == "" {
		t.Fatalf("lib's remote after sync:\n%s\nwant a WIP ref and the unpushed detached HEAD", refs)
	}
	if appRefs := gitCmd(t, filepath.Join(tmp, "app.git"), "for-each-ref", "--format=%(refname)", "refs/gitten/"); strings.Contains(appRefs, "/heads/HEAD") {
		t.Errorf("lib's commit went to app's remote:\n%s", appRefs)
	}

	// lib's remote alone gives it back.
	restore := filepath.Join(tmp, "restore")
	gitCmd(t, tmp, "clone", "-q", "lib.git", restore)
	gitCmd(t, restore, "fetch", "-q", "origin", headRef+":refs/heads/recovered")
	gitCmd(t, restore, "checkout", "-q", "recovered")
	gitCmd(t, restore, "fetch", "-q", "origin", wipRef)
	gitCmd(t, restore, "stash", "apply", "-q", "--index", "FETCH_HEAD")
	if got, _ := os.ReadFile(filepath.Join(restore, "lib.txt")); string(got) != "v2\nwip\n" {
		t.Errorf("restored lib.txt = %q", got)
	}

	// New work, backed up: a dry run plans its push from that backup.
	if err := os.WriteFile(filepath.Join(lib, "lib.txt"), []byte("v2\nmore wip\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, code := env.run(t, "backup"); code != 0 {
		t.Fatalf("backup exit %d:\n%s", code, out)
	}
	if out, code := env.run(t, "remote-sync", "--dry-run"); code != 0 || !strings.Contains(out, "submodule lib: push ") || !strings.Contains(out, "/wip/") {
		t.Errorf("remote-sync --dry-run exit %d, want it to plan lib's new WIP:\n%s", code, out)
	}
	if out, code := env.run(t, "remote-sync", "--list"); code != 0 || !strings.Contains(out, "submodule lib") || !strings.Contains(out, headRef) {
		t.Errorf("remote-sync --list exit %d, want lib's refs:\n%s", code, out)
	}

	// Once lib's commit is on its remote, its saved copy is deleted.
	gitCmd(t, lib, "push", "-q", "origin", "HEAD:refs/heads/main")
	if out, code := env.run(t, "sync"); code != 0 {
		t.Fatalf("sync exit %d:\n%s", code, out)
	}
	if refs := gitCmd(t, libRemote, "for-each-ref", "--format=%(refname)", "refs/gitten/"); strings.Contains(refs, "/heads/HEAD") {
		t.Errorf("lib's saved commit should be deleted once pushed:\n%s", refs)
	}
}

// gatedGit puts a git in front of the real one, on a PATH it returns: once
// one of its arguments is gateOn, it runs the real command, then holds until
// gate/go exists, having made gate/reached; every command is logged to
// gate/log. gateOn "" only logs.
func gatedGit(t *testing.T, gateOn string) (path, gate string) {
	t.Helper()
	real, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	gate = t.TempDir()
	bin := filepath.Join(gate, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	script := fmt.Sprintf(`#!/bin/sh
echo "$*" >> %[2]q/log
%[1]q "$@"
rc=$?
if [ -n %[3]q ]; then
  for a in "$@"; do
    if [ "$a" = %[3]q ] && mkdir %[2]q/reached 2>/dev/null; then
      while [ ! -e %[2]q/go ]; do sleep 0.05; done
      break
    fi
  done
fi
exit $rc
`, real, gate, gateOn)
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin + string(os.PathListSeparator) + os.Getenv("PATH"), gate
}

// start runs gitten in the background with PATH set to path.
func (e *testEnv) start(t *testing.T, path string, args ...string) func() (string, int) {
	t.Helper()
	cmd := exec.Command(e.bin, args...)
	cmd.Env = binEnv(append([]string{"GITTEN_CONFIG=" + e.configFile, "GITTEN_STATE=" + e.stateFile, "PATH=" + path}, e.extraEnv...)...)
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	return func() (string, int) {
		err := cmd.Wait()
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return buf.String(), ee.ExitCode()
		}
		if err != nil {
			t.Fatal(err)
		}
		return buf.String(), 0
	}
}

// waitFor polls until exists(path) or the deadline, reporting which.
func waitFor(path string, within time.Duration) bool {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

// TestE2E_RemoveAndCopyDontRace: a repo is never deleted out from under a
// workspace copy being made from it, whichever command starts first. The
// copy borrows the repo's objects (reference-clone), so losing the repo
// would leave it unreadable.
func TestE2E_RemoveAndCopyDontRace(t *testing.T) {
	setup := func(t *testing.T) (*testEnv, string, string) { return adoptedApp(t, "60s") }
	const key = "local/remotes/app"
	create := []string{"ws", "create", "app", "--repo", key, "--agents", "1", "--isolation", "reference-clone"}

	t.Run("copy first", func(t *testing.T) {
		env, src, root := setup(t)
		createPath, createGate := gatedGit(t, "clone")
		creating := env.start(t, createPath, create...)
		if !waitFor(filepath.Join(createGate, "reached"), 30*time.Second) {
			t.Fatal("ws create never reached its clone")
		}
		// Remove must wait for the copy to be recorded; meanwhile it runs no
		// git in the repo.
		removePath, removeGate := gatedGit(t, "")
		removing := env.start(t, removePath, "remove", key, "--delete", "--yes")
		if waitFor(filepath.Join(removeGate, "log"), 2*time.Second) {
			log, _ := os.ReadFile(filepath.Join(removeGate, "log"))
			t.Errorf("remove ran git while the copy was being made:\n%s", log)
		}
		if err := os.WriteFile(filepath.Join(createGate, "go"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if out, code := creating(); code != 0 {
			t.Fatalf("ws create exit %d:\n%s", code, out)
		}
		out, code := removing()
		if code == 0 || !strings.Contains(out, "workspace copies") {
			t.Errorf("remove exit %d, want it to keep a repo a copy was made from:\n%s", code, out)
		}
		if !gitOK(filepath.Join(src, ".git")) {
			t.Error("the repo was deleted")
		}
		if out, err := exec.Command("git", "-C", filepath.Join(root, "app", "agent-1"), "fsck", "--full").CombinedOutput(); err != nil {
			t.Errorf("the copy is broken: %v\n%s", err, out)
		}
	})

	t.Run("remove first", func(t *testing.T) {
		env, src, root := setup(t)
		removePath, removeGate := gatedGit(t, "status")
		removing := env.start(t, removePath, "remove", key, "--delete", "--yes")
		if !waitFor(filepath.Join(removeGate, "reached"), 30*time.Second) {
			t.Fatal("remove never inspected the repo")
		}
		creating := env.start(t, os.Getenv("PATH"), create...)
		if err := os.WriteFile(filepath.Join(removeGate, "go"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if out, code := removing(); code != 0 {
			t.Fatalf("remove exit %d:\n%s", code, out)
		}
		out, code := creating()
		if code == 0 {
			t.Errorf("ws create made a copy of a repo being deleted:\n%s", out)
		}
		if gitOK(filepath.Join(src, ".git")) {
			t.Error("the repo should be deleted")
		}
		if copyPath := filepath.Join(root, "app", "agent-1"); gitOK(filepath.Join(copyPath, ".git")) {
			out, err := exec.Command("git", "-C", copyPath, "fsck", "--full").CombinedOutput()
			t.Errorf("a copy of the deleted repo was left (fsck: %v):\n%s", err, out)
		}
	})
}

// adoptedApp tracks a clone of a fresh remote, local/remotes/app, with
// lockTimeout as lock_timeout, and returns its env, its clone and its
// workspace root.
func adoptedApp(t *testing.T, lockTimeout string) (env *testEnv, src, root string) {
	t.Helper()
	env = newLocalEnv(t)
	tmp := t.TempDir()
	remote := filepath.Join(tmp, "remotes", "app.git")
	gitCmd(t, tmp, "init", "-q", "--bare", "-b", "main", remote)
	src = filepath.Join(env.baseDir, "app")
	gitCmd(t, tmp, "clone", "-q", remote, src)
	gitCmd(t, src, "-c", "user.email=e@e", "-c", "user.name=e", "commit", "-q", "--allow-empty", "-m", "init")
	gitCmd(t, src, "push", "-q", "origin", "HEAD:main")
	root = filepath.Join(tmp, "workspaces")
	cfg := fmt.Sprintf("base_dir: %s\nlock_timeout: %s\nbackup:\n  archive_dir: %s\nworkspace_defaults:\n  root: %s\n", env.baseDir, lockTimeout, filepath.Join(tmp, "archive"), root)
	if err := os.WriteFile(env.configFile, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, code := env.run(t, "adopt", src); code != 0 {
		t.Fatalf("adopt exit %d:\n%s", code, out)
	}
	return env, src, root
}

// TestE2E_HeldRepoLockIsBusy: a command that gives up waiting for a repo
// another gitten command holds, and so changes nothing, exits 75 (try again
// later), not 1; one that makes other changes meanwhile exits 3.
func TestE2E_HeldRepoLockIsBusy(t *testing.T) {
	env, src, root := adoptedApp(t, "200ms")
	lock, err := os.OpenFile(filepath.Join(src, ".git", "gitten.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}

	var removed struct {
		Kept []struct{ Target, Error string } `json:"kept"`
	}
	if stderr, code := env.runJSON(t, &removed, "remove", "local/remotes/app", "--delete", "--yes"); code != 75 || len(removed.Kept) != 1 {
		t.Errorf("remove under a held lock: exit %d, %+v, want 75 and the repo kept:\n%s", code, removed, stderr)
	}
	if !gitOK(filepath.Join(src, ".git")) {
		t.Error("the repo was deleted")
	}
	// The workspace is made; its copy isn't.
	if out, code := env.run(t, "ws", "create", "app", "--repo", "local/remotes/app", "--agents", "1", "--isolation", "reference-clone"); code != 3 {
		t.Errorf("ws create under a held lock: exit %d, want 3:\n%s", code, out)
	}
	if out, code := env.run(t, "ws", "ensure", "app"); code != 75 {
		t.Errorf("ws ensure under a held lock: exit %d, want 75:\n%s", code, out)
	}
	if gitOK(filepath.Join(root, "app", "agent-1", ".git")) {
		t.Error("a copy was made while the repo was locked")
	}
}

func gitOK(gitDir string) bool {
	_, err := os.Stat(gitDir)
	return err == nil
}

// TestE2E_ManifestKeepsAccounts: a manifest entry's account and SSH key are
// kept where config reads them -- config's repos:, or the shared manifest --
// so the clone gets that account's identity and key and keeps them; export
// writes them back out.
func TestE2E_ManifestKeepsAccounts(t *testing.T) {
	tmp := t.TempDir()
	remote := filepath.Join(tmp, "remotes", "app.git")
	gitCmd(t, tmp, "init", "-q", "--bare", "-b", "main", remote)
	seed := filepath.Join(tmp, "seed")
	gitCmd(t, tmp, "clone", "-q", remote, seed)
	gitCmd(t, seed, "-c", "user.name=e2e", "-c", "user.email=e2e@test.local", "commit", "-q", "--allow-empty", "-m", "init")
	gitCmd(t, seed, "push", "-q", "origin", "HEAD:main")
	key := filepath.Join(tmp, "keys", "id_work")

	for _, shared := range []bool{false, true} {
		t.Run(fmt.Sprintf("shared manifest=%v", shared), func(t *testing.T) {
			env := newLocalEnv(t)
			manifest := filepath.Join(t.TempDir(), "repos.yaml")
			cfg := fmt.Sprintf("base_dir: %s\naccounts:\n  - name: work\n    git:\n      name: Work Me\n      email: me@work.example\n", env.baseDir)
			if shared {
				cfg += "manifest: " + manifest + "\n"
			}
			if err := os.WriteFile(env.configFile, []byte(cfg), 0o600); err != nil {
				t.Fatal(err)
			}
			in := filepath.Join(t.TempDir(), "import.yaml")
			entry := fmt.Sprintf("repos:\n  - url: %s\n    account: work\n    ssh_key: %s\n", remote, key)
			if err := os.WriteFile(in, []byte(entry), 0o600); err != nil {
				t.Fatal(err)
			}
			if out, code := env.run(t, "manifest", "import", in); code != 0 {
				t.Fatalf("manifest import exit %d:\n%s", code, out)
			}
			clone := filepath.Join(env.baseDir, "local", "remotes", "app")
			if got := gitCmd(t, clone, "config", "--local", "user.email"); got != "me@work.example" {
				t.Errorf("user.email = %q, want the work account's", got)
			}
			if got := gitCmd(t, clone, "config", "--local", "core.sshCommand"); !strings.Contains(got, key) {
				t.Errorf("core.sshCommand = %q, want it to use %s", got, key)
			}
			keptIn := env.configFile
			if shared {
				keptIn = manifest
			}
			if b, _ := os.ReadFile(keptIn); !strings.Contains(string(b), "account: work") || !strings.Contains(string(b), key) {
				t.Errorf("the binding wasn't kept in %s:\n%s", keptIn, b)
			}
			// A later run keeps them: nothing to change.
			if out, code := env.run(t, "plan"); code != 0 || strings.Contains(out, "user.email") {
				t.Errorf("plan exit %d, wants a change:\n%s", code, out)
			}
			out, code := env.run(t, "manifest", "export")
			if code != 0 || !strings.Contains(out, "account: work") || !strings.Contains(out, "ssh_key: "+key) {
				t.Errorf("export exit %d, without the binding:\n%s", code, out)
			}
		})
	}
}

// TestE2E_ConfigPath: only the default config, when none is named, may be
// missing; one named -- even at the default path -- must exist. config --edit
// hands the editor the path as one argument, whatever it holds.
func TestE2E_ConfigPath(t *testing.T) {
	bin := gittenBin(t)
	home := t.TempDir()
	run := func(env []string, args ...string) (string, int) {
		cmd := exec.Command(bin, args...)
		cmd.Env = append(slices.DeleteFunc(os.Environ(), func(kv string) bool {
			return strings.HasPrefix(kv, "GITTEN_CONFIG=") || strings.HasPrefix(kv, "HOME=")
		}), append([]string{"HOME=" + home, "GITTEN_STATE=" + filepath.Join(home, "state.yaml")}, env...)...)
		out, err := cmd.CombinedOutput()
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return string(out), ee.ExitCode()
		}
		if err != nil {
			t.Fatal(err)
		}
		return string(out), 0
	}
	if out, code := run(nil, "config", "--validate"); code != 0 {
		t.Errorf("no config named, none at the default path: exit %d\n%s", code, out)
	}
	defaultPath := filepath.Join(home, ".config", "gitten", "config.yaml")
	if out, code := run(nil, "--config", defaultPath, "config", "--validate"); code == 0 {
		t.Errorf("a named config that's missing must fail, even at the default path:\n%s", out)
	}
	if out, code := run([]string{"GITTEN_CONFIG=" + defaultPath}, "config", "--validate"); code == 0 {
		t.Errorf("$GITTEN_CONFIG naming a missing file must fail:\n%s", out)
	}

	dir := filepath.Join(t.TempDir(), "my configs $(touch pwned)")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfg, []byte("concurrency: 2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := filepath.Join(t.TempDir(), "args")
	editor := filepath.Join(t.TempDir(), "editor")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$#\" \"$1\" > %q\n", got)
	if err := os.WriteFile(editor, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if out, code := run([]string{"EDITOR=" + editor, "VISUAL="}, "--config", cfg, "config", "--edit"); code != 0 {
		t.Fatalf("config --edit exit %d:\n%s", code, out)
	}
	if b, _ := os.ReadFile(got); string(b) != "1\n"+cfg+"\n" {
		t.Errorf("the editor got %q, want the path as one argument", b)
	}
	if _, err := os.Stat(filepath.Join(dir, "pwned")); err == nil {
		t.Error("the path was run as shell")
	}
}

// TestE2E_JSONErrors: under --json a command that fails, a config that
// doesn't load and a command line that doesn't parse each print one
// document, the error, and exit with its code; --help prints one too, of
// its text.
func TestE2E_JSONErrors(t *testing.T) {
	env := newLocalEnv(t)
	if err := os.WriteFile(env.configFile, []byte("base_dir: "+env.baseDir+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	type errorDoc struct {
		Error    string `json:"error"`
		ExitCode int    `json:"exit_code"`
	}
	check := func(t *testing.T, wantCode int, args ...string) {
		t.Helper()
		var doc errorDoc
		_, code := env.runJSON(t, &doc, args...)
		if code != wantCode || doc.Error == "" || doc.ExitCode != code {
			t.Errorf("gitten --json %v: exit %d, document %+v; want exit %d and the error", args, code, doc, wantCode)
		}
	}
	t.Run("a failing command", func(t *testing.T) { check(t, 1, "restore", "nothing-matches") })
	t.Run("a missing selector", func(t *testing.T) { check(t, 1, "remove") })
	t.Run("a command that doesn't exist", func(t *testing.T) { check(t, 80, "no-such-command") })
	t.Run("a bad flag", func(t *testing.T) { check(t, 80, "status", "--no-such-flag") })
	t.Run("help", func(t *testing.T) {
		var doc struct{ Help string }
		stderr, code := env.runJSON(t, &doc, "sync", "--help")
		if code != 0 || !strings.HasPrefix(doc.Help, "Usage: gitten sync") || !strings.Contains(stderr, "Usage: gitten sync") {
			t.Errorf("gitten --json sync --help: exit %d, document %+v; want the help in it, and on stderr", code, doc)
		}
	})
	t.Run("a config that doesn't load", func(t *testing.T) {
		if err := os.WriteFile(env.configFile, []byte("unknown_key: true\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		check(t, 1, "sync")
	})
}

// TestE2E_SyncUpdate: with sync.update: ff-only-clean, sync fast-forwards a
// clean clone that is behind, and leaves one with uncommitted changes.
func TestE2E_SyncUpdate(t *testing.T) {
	env := newLocalEnv(t)
	tmp := t.TempDir()
	remote := filepath.Join(tmp, "remotes", "app.git")
	gitCmd(t, tmp, "init", "-q", "--bare", "-b", "main", remote)
	clone := func(name string) string {
		path := filepath.Join(env.baseDir, name)
		gitCmd(t, tmp, "clone", "-q", remote, path)
		gitCmd(t, path, "config", "user.email", "e2e@test.local")
		gitCmd(t, path, "config", "user.name", "e2e")
		return path
	}
	upstream := clone("upstream")
	gitCmd(t, upstream, "commit", "-q", "--allow-empty", "-m", "first")
	gitCmd(t, upstream, "push", "-q", "origin", "HEAD:main")
	app := clone("app")
	if err := os.WriteFile(env.configFile, []byte("base_dir: "+env.baseDir+"\nsync:\n  update: ff-only-clean\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, code := env.run(t, "adopt", app); code != 0 {
		t.Fatalf("adopt exit %d:\n%s", code, out)
	}
	gitCmd(t, upstream, "commit", "-q", "--allow-empty", "-m", "second")
	gitCmd(t, upstream, "push", "-q", "origin", "HEAD:main")

	var doc struct {
		Updated int `json:"updated"`
		Copies  []struct {
			Repo    string `json:"repo"`
			Updated bool   `json:"updated"`
		} `json:"copies"`
	}
	if stderr, code := env.runJSON(t, &doc, "sync", "--force", "--no-discover"); code != 0 {
		t.Fatalf("sync exit %d:\n%s", code, stderr)
	}
	if doc.Updated != 1 || gitCmd(t, app, "rev-parse", "HEAD") != gitCmd(t, upstream, "rev-parse", "HEAD") {
		t.Fatalf("the clean clone wasn't fast-forwarded: %+v", doc)
	}

	gitCmd(t, upstream, "commit", "-q", "--allow-empty", "-m", "third")
	gitCmd(t, upstream, "push", "-q", "origin", "HEAD:main")
	if err := os.WriteFile(filepath.Join(app, "wip.txt"), []byte("staged\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, app, "add", "wip.txt")
	before := gitCmd(t, app, "rev-parse", "HEAD")
	var again struct {
		Copies []struct {
			UpdateSkipped string `json:"update_skipped"`
		} `json:"copies"`
	}
	if stderr, code := env.runJSON(t, &again, "sync", "--force", "--no-discover"); code != 0 {
		t.Fatalf("sync exit %d:\n%s", code, stderr)
	}
	if gitCmd(t, app, "rev-parse", "HEAD") != before || len(again.Copies) != 1 || !strings.Contains(again.Copies[0].UpdateSkipped, "uncommitted") {
		t.Errorf("a clone with staged work must be left: %+v", again)
	}
}

// TestE2E_AccountsGitconfig: accounts gitconfig --install makes each
// account's identity git's own for repos under its folders, through
// includeIf, including repos gitten never touched. Account names that make
// the same file name stay apart, and folders and files whose paths hold
// characters git config quotes or git's wildcards match only themselves.
func TestE2E_AccountsGitconfig(t *testing.T) {
	env := newLocalEnv(t)
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(tmp, "src #1 [x]")
	global := filepath.Join(tmp, "gitconfig")
	cfg := fmt.Sprintf(`base_dir: %q
accounts:
  - name: work/a
    hosts: [github.com]
    owners: [acme]
    ssh_key: ~/.ssh/id_work
    git: {name: Work Me, email: me@acme.example}
  - name: work_a
    hosts: [github.com]
    owners: [me]
    git: {email: me@home.example}
`, base)
	if err := os.WriteFile(env.configFile, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	env.extraEnv = []string{"GIT_CONFIG_GLOBAL=" + global, "XDG_CONFIG_HOME=" + filepath.Join(tmp, `config #"\ dir`)}
	if out, code := env.run(t, "accounts", "gitconfig", "--install"); code != 0 {
		t.Fatalf("accounts gitconfig --install exit %d:\n%s", code, out)
	}
	if out, code := env.run(t, "accounts", "gitconfig", "--install"); code != 0 || strings.Count(mustRead(t, global), "include") != 1 {
		t.Fatalf("a second --install must not include it twice (exit %d):\n%s\n%s", code, out, mustRead(t, global))
	}

	email := func(path string) string {
		t.Helper()
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
		gitCmd(t, path, "init", "-q")
		cmd := exec.Command("git", "-C", path, "config", "user.email")
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+global)
		out, _ := cmd.Output()
		return strings.TrimSpace(string(out))
	}
	if got := email(filepath.Join(base, "github.com", "acme", "api")); got != "me@acme.example" {
		t.Errorf("a repo under acme's folder: user.email %q", got)
	}
	if got := email(filepath.Join(base, "github.com", "me", "dots")); got != "me@home.example" {
		t.Errorf("a repo under me's folder: user.email %q", got)
	}
	if got := email(filepath.Join(base, "gitlab.com", "x", "y")); got != "" {
		t.Errorf("a repo no account covers: user.email %q", got)
	}
}

// TestE2E_SetupAnswers: setup from an answers file writes the config, tracks
// the clone it names where it is and adds the shell integration to the
// shell's startup file; run again, it changes nothing twice. (The background
// sync stays off: a test never installs one.)
func TestE2E_SetupAnswers(t *testing.T) {
	env := newLocalEnv(t)
	home := t.TempDir()
	app := filepath.Join(home, "code", "app")
	gitCmd(t, home, "init", "-q", "--bare", "-b", "main", filepath.Join(home, "remotes", "app.git"))
	gitCmd(t, home, "clone", "-q", filepath.Join(home, "remotes", "app.git"), app)
	answers := filepath.Join(home, "answers.yaml")
	if err := os.WriteFile(answers, []byte(fmt.Sprintf(`base_dir: %s
layout: owner/name
accounts: []
adopt: [%s]
backups: true
schedule: false
remote_sync: false
update: false
reconcile_auto: safe
cleanup: mark_stale
shell: true
git_includes: false
clone: false
`, filepath.Join(home, "repos"), app)), 0o600); err != nil {
		t.Fatal(err)
	}
	env.extraEnv = []string{"HOME=" + home, "SHELL=/bin/zsh", "ZDOTDIR="}
	rc := filepath.Join(home, ".zshrc")
	if err := os.WriteFile(rc, []byte("export EDITOR=vim\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	for run := range 2 {
		var doc struct {
			Written bool                       `json:"written"`
			Steps   map[string]json.RawMessage `json:"steps"`
		}
		stderr, code := env.runJSON(t, &doc, "setup", "--answers", answers, "--offline")
		if code != 0 || !doc.Written || doc.Steps["shell"] == nil {
			t.Fatalf("setup run %d: exit %d, %+v\n%s", run+1, code, doc, stderr)
		}
	}
	text := mustRead(t, rc)
	if !strings.HasPrefix(text, "export EDITOR=vim\n") || strings.Count(text, "gitten shell-init zsh") != 1 {
		t.Errorf(".zshrc after two setups:\n%s", text)
	}
	if cfg := mustRead(t, env.configFile); !strings.Contains(cfg, "layout: owner/name") {
		t.Errorf("config:\n%s", cfg)
	}
	if repos := env.statusJSON(t); len(repos) != 1 {
		t.Errorf("tracked after setup: %v, want the adopted clone", repos)
	}
	if _, err := os.Stat(filepath.Join(home, "Library", "LaunchAgents")); err == nil {
		t.Error("setup installed a launchd agent with schedule: false")
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
