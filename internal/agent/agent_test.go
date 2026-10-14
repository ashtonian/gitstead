package agent

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ashtonian/gitten/internal/config"
)

func TestProfiles_DetectedWhenNoneDeclared(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "codex"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)

	got := Profiles(&config.Config{})
	if len(got) != 1 || got[0].Name != "codex" || got[0].Resume != "codex resume --last" {
		t.Errorf("detected = %+v; want codex, the one on PATH", got)
	}
	declared := &config.Config{Agents: []config.AgentProfile{{Name: "mine", Command: "claude-wrapper"}}}
	if p, err := Profile(declared, ""); err != nil || p.Name != "mine" {
		t.Errorf("default profile = %+v, %v; declared profiles replace detected ones", p, err)
	}
	if _, err := Profile(declared, "codex"); err == nil {
		t.Error("an undeclared profile resolved")
	}
	t.Setenv("PATH", t.TempDir())
	if _, err := Profile(&config.Config{}, ""); !errors.Is(err, ErrNoProfile) {
		t.Errorf("err = %v, want ErrNoProfile", err)
	}
}

func TestCommandFor(t *testing.T) {
	p := config.AgentProfile{Command: "claude", Resume: "claude --continue"}
	for _, tt := range []struct {
		ranBefore, fresh bool
		want             string
	}{
		{false, false, "claude"},
		{true, false, "claude --continue"},
		{true, true, "claude"},
	} {
		if got := CommandFor(p, tt.ranBefore, tt.fresh); got != tt.want {
			t.Errorf("CommandFor(ran %v, fresh %v) = %q", tt.ranBefore, tt.fresh, got)
		}
	}
	if got := CommandFor(config.AgentProfile{Command: "x"}, true, false); got != "x" {
		t.Errorf("without a resume command = %q", got)
	}
}

func TestLease(t *testing.T) {
	path := LeasePath(t.TempDir(), "app", "agent-1")
	if _, held := Read(path); held {
		t.Fatal("a lease no one took is held")
	}
	l, err := Acquire(path, Holder{PID: 42, Profile: "claude", Started: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if h, held := Read(path); !held || h.PID != 42 || h.Profile != "claude" {
		t.Errorf("Read = %+v, %v", h, held)
	}
	if _, err := Acquire(path, Holder{PID: 43}); !errors.Is(err, ErrRunning) {
		t.Errorf("a second Acquire = %v, want ErrRunning", err)
	}
	l.Release()
	if _, held := Read(path); held {
		t.Error("a released lease is held")
	}
	l2, err := Acquire(path, Holder{PID: 44})
	if err != nil {
		t.Fatalf("Acquire after release: %v", err)
	}
	l2.Release()
	Forget(path)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("Forget left the lease file")
	}
}

// TestHelperRun is not a test: run as a subprocess, it holds a lease as
// `ws run` does, optionally ignoring SIGTERM, and runs a long command.
func TestHelperRun(t *testing.T) {
	path := os.Getenv("GITTEN_TEST_LEASE")
	if path == "" {
		t.Skip("helper process")
	}
	l, err := Acquire(path, Holder{PID: os.Getpid(), Started: time.Now()})
	if err != nil {
		os.Exit(2)
	}
	defer l.Release()
	if os.Getenv("GITTEN_TEST_STUBBORN") != "" {
		signal.Ignore(syscall.SIGTERM)
		time.Sleep(time.Minute)
	}
	_, _ = Run(RunOptions{Dir: os.TempDir(), Command: "exec sleep 60", Shell: "/bin/sh"})
	os.Exit(0)
}

func startHelper(t *testing.T, path string, stubborn bool) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperRun$")
	cmd.Env = append(os.Environ(), "GITTEN_TEST_LEASE="+path)
	if stubborn {
		cmd.Env = append(cmd.Env, "GITTEN_TEST_STUBBORN=1")
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, held := Read(path); held {
			return cmd
		}
		if time.Now().After(deadline) {
			t.Fatal("the helper never took the lease")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestStop(t *testing.T) {
	path := LeasePath(t.TempDir(), "app", "agent-1")
	startHelper(t, path, false)
	ended, err := Stop(path, 10*time.Second, false)
	if err != nil || !ended {
		t.Fatalf("Stop = %v, %v", ended, err)
	}
	if ended, err := Stop(path, time.Second, false); !ended || err != nil {
		t.Errorf("stopping what isn't running = %v, %v", ended, err)
	}
}

func TestStop_KillsWhatWontEnd(t *testing.T) {
	path := LeasePath(t.TempDir(), "app", "agent-1")
	startHelper(t, path, true)
	if ended, err := Stop(path, 300*time.Millisecond, false); ended || err != nil {
		t.Fatalf("without --kill, Stop = %v, %v; want it still running", ended, err)
	}
	if ended, err := Stop(path, 300*time.Millisecond, true); !ended || err != nil {
		t.Fatalf("with --kill, Stop = %v, %v", ended, err)
	}
}

// A status probe gives its first line, says why it gave none, and is ended
// with what it started when its context does.
func TestProbe(t *testing.T) {
	dir := t.TempDir()
	if got := Probe(context.Background(), dir, `echo "fixing the parser"; echo second`, []string{"X=1"}); got != "fixing the parser" {
		t.Errorf("Probe = %q", got)
	}
	if got := Probe(context.Background(), dir, `echo "$X in $PWD"`, []string{"X=env"}); !strings.HasPrefix(got, "env in ") {
		t.Errorf("Probe with env = %q", got)
	}
	if got := Probe(context.Background(), dir, "exit 3", nil); !strings.Contains(got, "status probe failed") {
		t.Errorf("a failing probe = %q", got)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	if got := Probe(ctx, dir, "sleep 30 & wait", nil); !strings.Contains(got, "status probe failed") {
		t.Errorf("a probe past its deadline = %q", got)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("the probe outlived its deadline by %s", elapsed)
	}
}
