package view

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agents "github.com/ashtonian/git-mgr/internal/agent"
	"github.com/ashtonian/git-mgr/internal/config"
	"github.com/ashtonian/git-mgr/internal/state"
)

func TestMain(m *testing.M) {
	os.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	os.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	os.Exit(m.Run())
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
		Activity: func(_ context.Context, hs []agents.Holder) map[string]Activity {
			return map[string]Activity{activityKey(hs[0]): {Text: "printed 5m ago", Idle: 5 * time.Minute}}
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

func TestParseMuxyActivity(t *testing.T) {
	out := ParseMuxyActivity([]byte("123\tp1\tclaude\twaiting\te9\tneeds input\n456\tp1\tcodex\tworking\t\t\n\n"))
	if out["123"] != "waiting: needs input" || out["456"] != "working" {
		t.Errorf("activity = %v", out)
	}
}

func TestAgo(t *testing.T) {
	for d, want := range map[time.Duration]string{12 * time.Second: "12s ago", 4 * time.Minute: "4m ago", 3 * time.Hour: "3h ago", 72 * time.Hour: "3d ago"} {
		if got := Ago(d); got != want {
			t.Errorf("Ago(%s) = %s", d, got)
		}
	}
}

// An agent alone in its tmux window gets the window's last output time; one
// sharing a window gets the time its screen was last seen to change.
func TestTracker(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	screens := map[string]string{"%2": "a"}
	tr := NewTracker()
	tr.now = func() time.Time { return now }
	tr.tmux = func(_ context.Context, args ...string) ([]byte, error) {
		if args[2] == "list-panes" {
			return []byte(fmt.Sprintf("%%1\t%d\t1\n%%2\t%d\t2\n%%9\t1\t1\n", now.Unix()-90, now.Unix())), nil
		}
		return []byte(screens[args[len(args)-1]]), nil
	}
	hs := []agents.Holder{{Launcher: "tmux", Socket: "/s", Session: "%1"}, {Launcher: "tmux", Socket: "/s", Session: "%2"}}
	key := func(pane string) string {
		return activityKey(agents.Holder{Launcher: "tmux", Socket: "/s", Session: pane})
	}

	got := tr.Read(context.Background(), hs)
	if got[key("%1")].Text != "printed 1m ago" || got[key("%2")].Text != "" || len(got) != 2 {
		t.Fatalf("first look = %v", got)
	}
	now = now.Add(30 * time.Second)
	if got := tr.Read(context.Background(), hs); got[key("%2")].Text != "quiet ≥ 30s" {
		t.Errorf("an unchanged screen = %v", got[key("%2")])
	}
	screens["%2"] = "b"
	now = now.Add(10 * time.Second)
	tr.Read(context.Background(), hs)
	now = now.Add(20 * time.Second)
	if got := tr.Read(context.Background(), hs); got[key("%2")].Text != "printed 20s ago" || got[key("%2")].Idle != 20*time.Second {
		t.Errorf("after a change = %v", got[key("%2")])
	}
}
