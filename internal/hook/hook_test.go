package hook

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ashtonian/git-mgr/internal/config"
)

func TestRun(t *testing.T) {
	dir, logs := t.TempDir(), t.TempDir()
	var echo bytes.Buffer
	r := Runner{LogDir: logs, Echo: &echo}
	h := Hook{
		Event: PostCreate, Dir: dir, Name: "app/agent-1",
		Commands: []string{
			`printf '%s|%s|%s|%s|%s' "$GIT_MGR_EVENT" "$GIT_MGR_REPO" "$GIT_MGR_SOURCE" "$GIT_MGR_AGENT" "$PWD" > env.txt`,
			`echo second`,
		},
		Env: Env{Repo: "github.com/acme/app", Source: "/src/app", Agent: "agent-1"},
	}
	if err := r.Run(context.Background(), h); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "env.txt"))
	want := "post_create|github.com/acme/app|/src/app|agent-1|"
	if !strings.HasPrefix(string(got), want) || !strings.HasSuffix(string(got), filepath.Base(dir)) {
		t.Errorf("env = %q, want %q<dir>", got, want)
	}
	if echo.String() != "second\n" {
		t.Errorf("echo = %q", echo.String())
	}
	log, err := os.ReadFile(filepath.Join(logs, "app_agent-1.post_create.log"))
	if err != nil || !strings.Contains(string(log), "$ echo second\nsecond\n") {
		t.Errorf("log = %q, %v", log, err)
	}
}

// The first failing command stops the run, and its error carries the tail of
// its output and where the whole is.
func TestRun_StopsAtTheFirstFailure(t *testing.T) {
	dir := t.TempDir()
	r := Runner{LogDir: t.TempDir()}
	h := Hook{Event: PostClone, Dir: dir, Name: "github.com/acme/app",
		Commands: []string{"seq 1 30; echo boom >&2; exit 3", "touch never"}}
	err := r.Run(context.Background(), h)
	var hookErr *Error
	if !errors.As(err, &hookErr) {
		t.Fatalf("Run = %v, want an *Error", err)
	}
	if hookErr.Command != "seq 1 30; echo boom >&2; exit 3" || !strings.HasSuffix(hookErr.Tail, "30\nboom") || strings.Contains(hookErr.Tail, "\n20\n") {
		t.Errorf("error = %+v", hookErr)
	}
	if _, err := os.Stat(filepath.Join(dir, "never")); err == nil {
		t.Error("a command after the failure ran")
	}
	if log, _ := os.ReadFile(hookErr.Log); !strings.Contains(string(log), "1\n2\n") {
		t.Errorf("the log should hold all the output:\n%s", log)
	}
}

// A command past the timeout is killed, with whatever it started.
func TestRun_Timeout(t *testing.T) {
	dir := t.TempDir()
	r := Runner{LogDir: t.TempDir(), Timeout: 200 * time.Millisecond}
	h := Hook{Event: PostClone, Dir: dir, Name: "x", Commands: []string{"(sleep 5; touch late) & wait"}}
	start := time.Now()
	err := r.Run(context.Background(), h)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("Run = %v, want a timeout", err)
	}
	if time.Since(start) > 4*time.Second {
		t.Error("the run waited for the command instead of killing it")
	}
}

// No input, so a command waiting for some ends at once.
func TestRun_NoInput(t *testing.T) {
	r := Runner{LogDir: t.TempDir(), Timeout: 5 * time.Second}
	h := Hook{Event: PostClone, Dir: t.TempDir(), Name: "x", Commands: []string{"read line || exit 0; exit 1"}}
	if err := r.Run(context.Background(), h); err != nil {
		t.Fatalf("a command reading input should see none: %v", err)
	}
}

func TestTailBuffer(t *testing.T) {
	tb := &tailBuffer{max: 4}
	for _, s := range []string{"abc", "defgh", "ij"} {
		tb.Write([]byte(s))
	}
	if got := string(tb.buf); !strings.HasSuffix(got, "ghij") || len(got) > 8 {
		t.Errorf("buf = %q", got)
	}
}

// A hook started from inside an agent's run gets its own context, not the
// agent's.
func TestRun_LeavesTheCallersContext(t *testing.T) {
	t.Setenv("GIT_MGR_PORT", "41000")
	t.Setenv("GIT_MGR_WORKSPACE", "caller")
	dir := t.TempDir()
	h := Hook{Event: PostClone, Dir: dir, Name: "x", Env: Env{Repo: "github.com/a/b"},
		Commands: []string{`printf '%s|%s|%s' "$GIT_MGR_PORT" "$GIT_MGR_WORKSPACE" "$GIT_MGR_REPO" > env.txt`}}
	if err := (Runner{LogDir: t.TempDir()}).Run(context.Background(), h); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "env.txt")); string(got) != "||github.com/a/b" {
		t.Errorf("env = %q", got)
	}
	for _, kv := range h.environ() {
		name, _, _ := strings.Cut(kv, "=")
		if !slices.Contains(config.ContextVars, name) {
			t.Errorf("%s isn't in config.ContextVars", name)
		}
	}
}
