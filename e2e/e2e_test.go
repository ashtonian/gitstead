//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ashtonian/git-mgr/internal/config"
	"github.com/ashtonian/git-mgr/internal/discovery"
	"github.com/ashtonian/git-mgr/internal/gitops"
	"github.com/ashtonian/git-mgr/internal/state"
	"github.com/testcontainers/testcontainers-go/modules/forgejo"
	"gopkg.in/yaml.v3"
)

// testEnv holds a running Forgejo instance and test paths.
type testEnv struct {
	ctx        context.Context
	container  *forgejo.Container
	httpURL    string // e.g. "http://localhost:32771"
	adminUser  string
	adminPass  string
	baseDir    string // temp dir for cloned repos
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
	t.Cleanup(func() { ctr.Terminate(ctx) })

	httpURL, err := ctr.ConnectionString(ctx)
	if err != nil {
		t.Fatalf("getting connection string: %v", err)
	}

	tmpDir := t.TempDir()

	env := &testEnv{
		ctx:        ctx,
		container:  ctr,
		httpURL:    httpURL,
		adminUser:  ctr.AdminUsername(),
		adminPass:  ctr.AdminPassword(),
		baseDir:    filepath.Join(tmpDir, "repos"),
		stateFile:  filepath.Join(tmpDir, "state.yaml"),
		configFile: filepath.Join(tmpDir, "config.yaml"),
	}

	return env
}

// createToken creates an API token for the admin user.
func (e *testEnv) createToken(t *testing.T) string {
	t.Helper()
	body := map[string]any{
		"name":   "test-token-" + fmt.Sprintf("%d", time.Now().UnixNano()),
		"scopes": []string{"all"},
	}
	resp := e.apiCall(t, "POST", "/api/v1/users/"+e.adminUser+"/tokens", body, true)
	var result map[string]any
	json.NewDecoder(resp.Body).Decode(&result)
	resp.Body.Close()
	token, ok := result["sha1"].(string)
	if !ok {
		t.Fatalf("no sha1 in token response: %v", result)
	}
	return token
}

// createRepo creates a repo via Forgejo API. Returns the SSH clone URL.
func (e *testEnv) createRepo(t *testing.T, token, name string, private bool) string {
	t.Helper()
	body := map[string]any{
		"name":           name,
		"private":        private,
		"auto_init":      true, // creates initial commit so clone works
		"default_branch": "main",
	}
	resp := e.apiCallWithToken(t, "POST", "/api/v1/user/repos", body, token)
	var result map[string]any
	json.NewDecoder(resp.Body).Decode(&result)
	resp.Body.Close()

	if resp.StatusCode != 201 {
		t.Fatalf("create repo %s: status %d", name, resp.StatusCode)
	}

	sshURL, _ := result["ssh_url"].(string)
	return sshURL
}

// createOrg creates an organization via Forgejo API.
func (e *testEnv) createOrg(t *testing.T, token, orgName string) {
	t.Helper()
	body := map[string]any{
		"username":   orgName,
		"visibility": "public",
	}
	resp := e.apiCallWithToken(t, "POST", "/api/v1/orgs", body, token)
	resp.Body.Close()
	if resp.StatusCode != 201 {
		t.Fatalf("create org %s: status %d", orgName, resp.StatusCode)
	}
}

// createOrgRepo creates a repo under an org.
func (e *testEnv) createOrgRepo(t *testing.T, token, org, name string) string {
	t.Helper()
	body := map[string]any{
		"name":           name,
		"auto_init":      true,
		"default_branch": "main",
	}
	resp := e.apiCallWithToken(t, "POST", "/api/v1/orgs/"+org+"/repos", body, token)
	var result map[string]any
	json.NewDecoder(resp.Body).Decode(&result)
	resp.Body.Close()
	if resp.StatusCode != 201 {
		t.Fatalf("create org repo %s/%s: status %d", org, name, resp.StatusCode)
	}
	sshURL, _ := result["ssh_url"].(string)
	return sshURL
}

func (e *testEnv) apiCall(t *testing.T, method, path string, body any, basicAuth bool) *http.Response {
	t.Helper()
	var bodyReader io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		bodyReader = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.httpURL+path, bodyReader)
	req.Header.Set("Content-Type", "application/json")
	if basicAuth {
		req.SetBasicAuth(e.adminUser, e.adminPass)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("API %s %s: %v", method, path, err)
	}
	return resp
}

func (e *testEnv) apiCallWithToken(t *testing.T, method, path string, body any, token string) *http.Response {
	t.Helper()
	var bodyReader io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		bodyReader = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.httpURL+path, bodyReader)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "token "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("API %s %s: %v", method, path, err)
	}
	return resp
}

// writeConfig writes a git-mgr config file pointing at the Forgejo instance.
func (e *testEnv) writeConfig(t *testing.T, token string, rules []config.DiscoveryRule) {
	t.Helper()
	cfg := config.Defaults()
	cfg.BaseDir = e.baseDir
	cfg.Providers = []config.Provider{
		{
			Name:  "forgejo",
			Type:  "github",               // Forgejo is GitHub API compatible
			URL:   e.httpURL + "/api/v1/", // Forgejo uses /api/v1/, not GHE's /api/v3/
			Token: token,
		},
	}
	cfg.Discovery = rules
	cfg.Backup.PatchDir = filepath.Join(e.baseDir, ".patches")

	out, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	os.MkdirAll(filepath.Dir(e.configFile), 0700)
	if err := os.WriteFile(e.configFile, out, 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

// --- Tests ---

func TestE2E_DiscoverAndClone(t *testing.T) {
	env := setupTestEnv(t)
	token := env.createToken(t)

	// Seed repos
	env.createRepo(t, token, "repo-alpha", false)
	env.createRepo(t, token, "repo-beta", false)
	env.createRepo(t, token, "repo-private", true)

	// Write config
	env.writeConfig(t, token, []config.DiscoveryRule{
		{
			Name:     "test-user",
			Provider: "forgejo",
			User:     env.adminUser,
			Tags:     []string{"test"},
		},
	})

	// Load config + state
	cfg, err := config.Load(env.configFile)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	st, err := state.Load(env.stateFile)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}

	// Run discovery
	results, discovered, err := discovery.RunDiscovery(env.ctx, cfg, "", "")
	if err != nil {
		t.Fatalf("discover: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Total < 3 {
		t.Errorf("expected at least 3 repos, got %d", results[0].Total)
	}

	// Verify discovered repos have correct host
	for key, repo := range discovered {
		t.Logf("discovered: %s (ssh: %s)", key, repo.SSHURL)
		if repo.DiscoveredBy != "test-user" {
			t.Errorf("expected DiscoveredBy=test-user, got %q", repo.DiscoveredBy)
		}
		if len(repo.Tags) == 0 || repo.Tags[0] != "test" {
			t.Errorf("expected tags=[test], got %v", repo.Tags)
		}
	}

	// Clone repos via HTTPS (SSH not easily testable in container without key setup)
	// Instead, test the HTTP clone path by using git directly
	for _, repo := range discovered {
		// Use HTTP clone URL (token-authenticated) instead of SSH
		httpCloneURL := fmt.Sprintf("%s/%s/%s.git", env.httpURL, repo.Owner, repo.Name)

		relPath := repo.Host + "/" + repo.Owner + "/" + repo.Name
		destPath := filepath.Join(cfg.BaseDir, relPath)

		cmd := exec.Command("git", "clone", httpCloneURL, destPath)
		cmd.Env = append(os.Environ(),
			"GIT_ASKPASS=echo",
			fmt.Sprintf("GIT_HTTP_EXTRAHEADER=Authorization: token %s", token),
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			// Try with basic auth in URL
			authedURL := strings.Replace(httpCloneURL, "http://", fmt.Sprintf("http://%s:%s@", env.adminUser, env.adminPass), 1)
			cmd2 := exec.Command("git", "clone", authedURL, destPath)
			if out2, err2 := cmd2.CombinedOutput(); err2 != nil {
				t.Logf("clone attempt 1: %s", out)
				t.Fatalf("clone %s failed: %v\n%s", repo.Name, err2, out2)
			}
		}

		// Add to state
		now := time.Now()
		st.Repos[repo.Key()] = &state.RepoState{
			CloneURL:       repo.SSHURL,
			Host:           repo.Host,
			Owner:          repo.Owner,
			Name:           repo.Name,
			FullName:       repo.FullName,
			Provider:       "forgejo",
			LocalPath:      destPath,
			Cloned:         true,
			DefaultBranch:  repo.DefaultBranch,
			DiscoveredBy:   repo.DiscoveredBy,
			Tags:           repo.Tags,
			Status:         "clean",
			FirstSeen:      now,
			LastDiscovered: now,
		}
	}

	if err := st.Save(); err != nil {
		t.Fatalf("save state: %v", err)
	}

	// Verify repos on disk
	if len(st.Repos) < 3 {
		t.Errorf("expected at least 3 repos in state, got %d", len(st.Repos))
	}
	for key, repo := range st.Repos {
		if !gitops.HasGitDir(repo.LocalPath) {
			t.Errorf("repo %s not cloned at %s", key, repo.LocalPath)
		}
	}
}

func TestE2E_BackupAndStatus(t *testing.T) {
	env := setupTestEnv(t)
	token := env.createToken(t)

	// Create and clone a single repo
	env.createRepo(t, token, "backup-test", false)
	env.writeConfig(t, token, []config.DiscoveryRule{
		{Name: "test", Provider: "forgejo", User: env.adminUser},
	})

	cfg, _ := config.Load(env.configFile)
	st, _ := state.Load(env.stateFile)

	// Discover + clone
	_, discovered, _ := discovery.RunDiscovery(env.ctx, cfg, "", "")

	var repo discovery.Repo
	for _, r := range discovered {
		if r.Name == "backup-test" {
			repo = r
			break
		}
	}
	if repo.Name == "" {
		t.Fatal("backup-test repo not discovered")
	}

	// Clone via HTTP with basic auth
	relPath := repo.Host + "/" + repo.Owner + "/" + repo.Name
	destPath := filepath.Join(cfg.BaseDir, relPath)
	cloneURL := fmt.Sprintf("http://%s:%s@%s/%s/%s.git",
		env.adminUser, env.adminPass,
		strings.TrimPrefix(env.httpURL, "http://"),
		repo.Owner, repo.Name)

	cmd := exec.Command("git", "clone", cloneURL, destPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("clone failed: %v\n%s", err, out)
	}

	// Configure git user in the repo
	gitops.Git(env.ctx, destPath, "config", "user.email", "test@test.com")
	gitops.Git(env.ctx, destPath, "config", "user.name", "Test")

	now := time.Now()
	st.Repos[repo.Key()] = &state.RepoState{
		CloneURL:  repo.SSHURL,
		Host:      repo.Host,
		Owner:     repo.Owner,
		Name:      repo.Name,
		FullName:  repo.FullName,
		Provider:  "forgejo",
		LocalPath: destPath,
		Cloned:    true,
		Status:    "clean",
		Tags:      []string{"test"},
		FirstSeen: now,
	}
	st.Save()

	// Verify clean status
	rs, err := gitops.GetStatus(env.ctx, destPath)
	if err != nil {
		t.Fatalf("get status: %v", err)
	}
	if rs.StatusString() != "clean" {
		t.Errorf("expected clean, got %s", rs.StatusString())
	}

	// Make it dirty
	os.WriteFile(filepath.Join(destPath, "dirty.txt"), []byte("dirty content"), 0644)

	// Run backup (both strategies)
	backupCfg := gitops.NewBackupConfig(
		gitops.WithStrategy("both"),
		gitops.WithPatchDir(filepath.Join(env.baseDir, ".patches")),
	)
	result, err := gitops.Backup(env.ctx, destPath, backupCfg)
	if err != nil {
		t.Fatalf("backup failed: %v", err)
	}

	// Verify stash ref was created
	if !result.IsDirty {
		t.Error("expected dirty result")
	}
	// stash create won't work on untracked-only changes, but we have an untracked file
	// so HasPatch should be true
	if !result.HasPatch {
		t.Error("expected patch backup")
	}

	// Verify working tree was NOT modified (transparency check)
	rs2, _ := gitops.GetStatus(env.ctx, destPath)
	if rs2.StatusString() == "clean" {
		t.Error("backup should not have cleaned the working tree")
	}

	// Verify status shows dirty
	if rs2.StatusString() != "dirty" {
		t.Errorf("expected dirty status, got %s", rs2.StatusString())
	}

	// Stage something and backup again
	os.WriteFile(filepath.Join(destPath, "staged.txt"), []byte("staged"), 0644)
	gitops.Git(env.ctx, destPath, "add", "staged.txt")

	result2, _ := gitops.Backup(env.ctx, destPath, backupCfg)
	if result2.RefName == "" {
		t.Error("expected stash ref for staged changes")
	}

	// List backup refs
	refs, err := gitops.ListBackupRefs(env.ctx, destPath)
	if err != nil {
		t.Fatalf("list refs: %v", err)
	}
	if len(refs) < 1 {
		t.Errorf("expected backup refs, got %d", len(refs))
	}
	t.Logf("backup refs: %v", refs)
}

func TestE2E_DiscoverByOrg(t *testing.T) {
	env := setupTestEnv(t)
	token := env.createToken(t)

	// Create an org with repos
	env.createOrg(t, token, "test-org")
	env.createOrgRepo(t, token, "test-org", "service-a")
	env.createOrgRepo(t, token, "test-org", "service-b")
	env.createOrgRepo(t, token, "test-org", "lib-shared")

	env.writeConfig(t, token, []config.DiscoveryRule{
		{
			Name:     "org-services",
			Provider: "forgejo",
			Org:      "test-org",
			Tags:     []string{"work", "services"},
		},
	})

	cfg, _ := config.Load(env.configFile)
	_, discovered, err := discovery.RunDiscovery(env.ctx, cfg, "", "")
	if err != nil {
		t.Fatalf("discover: %v", err)
	}

	if len(discovered) != 3 {
		t.Errorf("expected 3 repos, got %d", len(discovered))
		for k := range discovered {
			t.Logf("  %s", k)
		}
	}

	// Verify all repos have correct tags
	for _, repo := range discovered {
		if len(repo.Tags) != 2 || repo.Tags[0] != "work" || repo.Tags[1] != "services" {
			t.Errorf("repo %s: expected tags=[work,services], got %v", repo.Name, repo.Tags)
		}
	}
}

func TestE2E_DiscoverWithFilter(t *testing.T) {
	env := setupTestEnv(t)
	token := env.createToken(t)

	env.createRepo(t, token, "api-gateway", false)
	env.createRepo(t, token, "api-users", false)
	env.createRepo(t, token, "frontend-app", false)
	env.createRepo(t, token, "docs", false)

	// Filter: only repos starting with "api-"
	env.writeConfig(t, token, []config.DiscoveryRule{
		{
			Name:        "api-only",
			Provider:    "forgejo",
			User:        env.adminUser,
			NamePattern: "^api-",
		},
	})

	cfg, _ := config.Load(env.configFile)
	_, discovered, err := discovery.RunDiscovery(env.ctx, cfg, "", "")
	if err != nil {
		t.Fatalf("discover: %v", err)
	}

	if len(discovered) != 2 {
		t.Errorf("expected 2 api- repos, got %d", len(discovered))
		for k := range discovered {
			t.Logf("  %s", k)
		}
	}

	for _, repo := range discovered {
		if !strings.HasPrefix(repo.Name, "api-") {
			t.Errorf("unexpected repo %s (should match ^api-)", repo.Name)
		}
	}
}

func TestE2E_StaleDetection(t *testing.T) {
	env := setupTestEnv(t)
	token := env.createToken(t)

	env.createRepo(t, token, "still-exists", false)

	env.writeConfig(t, token, []config.DiscoveryRule{
		{Name: "test", Provider: "forgejo", User: env.adminUser},
	})

	cfg, _ := config.Load(env.configFile)
	st, _ := state.Load(env.stateFile)

	// Pre-populate state with a repo that no longer exists
	st.Repos["forgejo/fake-user/deleted-repo"] = &state.RepoState{
		Host:     "localhost",
		Owner:    "fake-user",
		Name:     "deleted-repo",
		FullName: "fake-user/deleted-repo",
		Status:   "clean",
	}
	st.Save()

	// Run discovery to detect stale
	_, discovered, _ := discovery.RunDiscovery(env.ctx, cfg, "", "")
	discoveredKeys := make(map[string]bool)
	for k := range discovered {
		discoveredKeys[k] = true
	}

	staleCount := discovery.DetectStale(st, discoveredKeys, cfg.Cleanup)
	if staleCount != 1 {
		t.Errorf("expected 1 stale repo, got %d", staleCount)
	}

	staleRepo := st.Repos["forgejo/fake-user/deleted-repo"]
	if staleRepo.Status != "stale" {
		t.Errorf("expected stale status, got %q", staleRepo.Status)
	}
	if staleRepo.StaleSince.IsZero() {
		t.Error("expected StaleSince to be set")
	}
}

func TestE2E_FetchFromForgejo(t *testing.T) {
	env := setupTestEnv(t)
	token := env.createToken(t)

	env.createRepo(t, token, "fetch-test", false)

	// Clone the repo
	cloneURL := fmt.Sprintf("http://%s:%s@%s/%s/fetch-test.git",
		env.adminUser, env.adminPass,
		strings.TrimPrefix(env.httpURL, "http://"),
		env.adminUser)

	destPath := filepath.Join(env.baseDir, "fetch-test")
	cmd := exec.Command("git", "clone", cloneURL, destPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("clone: %v\n%s", err, out)
	}

	// Fetch should work (no SSH needed for HTTP remotes)
	err := gitops.Fetch(env.ctx, destPath, "")
	if err != nil {
		t.Fatalf("fetch failed: %v", err)
	}

	// Verify HEAD exists
	head, err := gitops.Git(env.ctx, destPath, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}
	if len(head) != 40 {
		t.Errorf("invalid HEAD: %q", head)
	}
}

func TestE2E_AdoptExisting(t *testing.T) {
	env := setupTestEnv(t)
	token := env.createToken(t)

	env.createRepo(t, token, "adopt-me", false)

	// Clone to a random location (simulating pre-existing repo)
	cloneURL := fmt.Sprintf("http://%s:%s@%s/%s/adopt-me.git",
		env.adminUser, env.adminPass,
		strings.TrimPrefix(env.httpURL, "http://"),
		env.adminUser)

	existingPath := filepath.Join(env.baseDir, "random-location", "adopt-me")
	cmd := exec.Command("git", "clone", cloneURL, existingPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("clone: %v\n%s", err, out)
	}

	// Parse the remote to get identity
	identity, err := gitops.ParseRemoteURL(env.ctx, existingPath)
	if err != nil {
		t.Fatalf("parse remote: %v", err)
	}

	if identity.Name != "adopt-me" {
		t.Errorf("name: got %q, want adopt-me", identity.Name)
	}
	if identity.Owner != env.adminUser {
		t.Errorf("owner: got %q, want %s", identity.Owner, env.adminUser)
	}

	// Add to state (simulating adopt)
	st, _ := state.Load(env.stateFile)
	key := identity.Host + "/" + identity.FullName
	st.Repos[key] = &state.RepoState{
		CloneURL:      identity.SSHURL,
		Host:          identity.Host,
		Owner:         identity.Owner,
		Name:          identity.Name,
		FullName:      identity.FullName,
		LocalPath:     existingPath,
		Cloned:        true,
		ManuallyAdded: true,
		Status:        "clean",
		FirstSeen:     time.Now(),
	}
	st.Save()

	// Verify state
	st2, _ := state.Load(env.stateFile)
	if len(st2.Repos) != 1 {
		t.Fatalf("expected 1 repo, got %d", len(st2.Repos))
	}
	repo := st2.Repos[key]
	if !repo.ManuallyAdded {
		t.Error("expected manually_added=true")
	}
	if repo.LocalPath != existingPath {
		t.Errorf("path: got %q, want %q", repo.LocalPath, existingPath)
	}
}
