package view

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ashtonian/gitten/internal/activity"
	agents "github.com/ashtonian/gitten/internal/agent"
	"github.com/ashtonian/gitten/internal/config"
	"github.com/ashtonian/gitten/internal/state"
	"github.com/ashtonian/gitten/internal/testenv"
)

func TestMain(m *testing.M) {
	os.Exit(testenv.Main(m))
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func repo(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "init", "-q", "-b", "main")
	git(t, dir, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "init")
	return dir
}

func TestCollectAndRender(t *testing.T) {
	root := t.TempDir()
	clean, dirty := repo(t, filepath.Join(root, "clean")), repo(t, filepath.Join(root, "dirty"))
	if err := os.WriteFile(filepath.Join(dirty, "wip"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	copyPath := repo(t, filepath.Join(root, "ws", "agent-1"))
	st, err := state.Load(filepath.Join(root, "state.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	backup := time.Date(2026, 10, 13, 11, 0, 0, 0, time.UTC)
	st.Repos = map[string]*state.RepoState{
		"h/o/clean":    {LocalPath: clean, Cloned: true},
		"h/o/dirty":    {LocalPath: dirty, Cloned: true},
		"h/o/gone":     {LocalPath: filepath.Join(root, "gone"), Cloned: true, LastFetchError: "timeout\nmore"},
		"h/o/uncloned": {LocalPath: filepath.Join(root, "x")},
	}
	st.Workspaces = map[string]*state.WorkspaceState{"app": {Name: "app", Isolation: "clone", Replicas: []state.ReplicaState{
		{Agent: "agent-1", Path: copyPath, Copies: []state.CopyState{{Path: copyPath, Branch: "main", PR: "https://x/pull/1", BackupStatus: state.BackupStatus{LastBackup: backup}}}},
	}}}
	lease, err := agents.Acquire(agents.LeasePath(st.Dir(), "app", "agent-1"), agents.Holder{PID: 1, Launcher: "tmux", Socket: "/tmp/s", Session: "%3"})
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()

	now := backup.Add(2 * time.Hour)
	snap := Collect(context.Background(), &config.Config{}, st, Options{
		Concurrency: 4, Now: func() time.Time { return now },
		Activity: func(_ context.Context, hs []agents.Holder) map[string]activity.Activity {
			return map[string]activity.Activity{activity.Key(hs[0]): {Text: "printed 5m ago", Idle: 5 * time.Minute}}
		},
	})
	if len(snap.Repos) != 3 {
		t.Fatalf("repos = %+v; an uncloned repo isn't shown", snap.Repos)
	}
	states := map[string]string{}
	for _, r := range snap.Repos {
		states[r.Key] = r.State + " " + r.Problem
	}
	if states["h/o/clean"] != "clean " || states["h/o/dirty"] != "dirty " || states["h/o/gone"] != "missing fetch: timeout" {
		t.Errorf("repo states = %v", states)
	}
	c := snap.Copies[0]
	if c.AgentState != "running" || c.Activity != "printed 5m ago" || c.Branch != "main" || c.PR != "https://x/pull/1" || !c.LastBackup.Equal(backup) {
		t.Errorf("copy = %+v", c)
	}

	out := Render(snap, RenderOptions{})
	for _, want := range []string{"1 copy, 1 agent running", "3 repos, 2 need attention", "app/agent-1", "printed 5m ago", "2h ago", "https://x/pull/1", "h/o/dirty", "fetch: timeout"} {
		if !strings.Contains(out, want) {
			t.Errorf("render lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "h/o/clean") {
		t.Errorf("a clean repo is shown without All:\n%s", out)
	}
	if strings.Contains(out, "\x1b[") {
		t.Error("escape codes without Color")
	}
	if all := Render(snap, RenderOptions{All: true}); !strings.Contains(all, "h/o/clean") {
		t.Errorf("All doesn't show the clean repo:\n%s", all)
	}
	for _, line := range strings.Split(Render(snap, RenderOptions{Width: 40, Color: true}), "\n") {
		if n := len([]rune(stripANSI(line))); n > 40 {
			t.Errorf("line of %d columns at width 40: %q", n, line)
		}
	}
}

func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b {
			for i < len(s) && (s[i] < 'a' || s[i] > 'z') && (s[i] < 'A' || s[i] > 'Z') {
				i++
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// A running agent waiting for you says so, and what for.
func TestRender_Waiting(t *testing.T) {
	now := time.Now()
	s := Snapshot{Taken: now, Copies: []CopyRow{
		{Label: "app/agent-1", AgentState: "running", Waiting: now.Add(-time.Minute), WaitingFor: "Claude needs your permission to use Bash", State: "clean"},
		{Label: "app/agent-2", AgentState: "running", State: "clean"},
	}}
	out := Render(s, RenderOptions{})
	if !strings.Contains(out, "2 agents running, 1 waiting for you") {
		t.Errorf("summary:\n%s", out)
	}
	if !strings.Contains(out, "waiting") || !strings.Contains(out, "Claude needs your permission to use Bash") {
		t.Errorf("rows:\n%s", out)
	}
}
