// Package hook runs the commands config gives for an event in a checkout
// git-mgr has just made: post_clone in a repo's new clone, post_create in a
// new workspace copy. Commands come only from config, never from a
// repository.
package hook

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/ashtonian/git-mgr/internal/config"
	"github.com/ashtonian/git-mgr/internal/gitops"
	"github.com/ashtonian/git-mgr/internal/pathutil"
)

// Events.
const (
	PostClone  = "post_clone"
	PostCreate = "post_create"
)

// tailLines is how much of a failed command's output its error carries,
// from at most tailBytes of it.
const (
	tailLines = 10
	tailBytes = 8 << 10
)

// killGrace bounds how long a killed command's output is waited for.
const killGrace = 5 * time.Second

// Hook is one event's commands for one checkout.
type Hook struct {
	Event    string
	Commands []string
	// Dir is the checkout, where the commands run.
	Dir string
	// Name names the checkout in its log's file name: a repo key, or a
	// workspace/agent label.
	Name string
	Env  Env
	// Vars are more of the environment, NAME=value, such as an agent's.
	Vars []string
}

// Env is what a hook is told of its checkout, as GIT_MGR_* variables; empty
// fields are left out.
type Env struct {
	Repo      string // GIT_MGR_REPO: the tracked repo's key
	Source    string // GIT_MGR_SOURCE: the repo's clone, a copy is made from
	Workspace string // GIT_MGR_WORKSPACE
	Agent     string // GIT_MGR_AGENT
	Branch    string // GIT_MGR_BRANCH: the copy's branch
}

// Runner runs hooks.
type Runner struct {
	// LogDir holds each checkout's log of its last run of each event.
	LogDir string
	// Timeout bounds each command; 0 means none.
	Timeout time.Duration
	// Echo also gets the output, as it comes; nil for none.
	Echo io.Writer
}

// Error reports a hook command that failed.
type Error struct {
	Event, Command string
	// Log holds the run's output; Tail is its last lines.
	Log, Tail string
	Err       error
}

func (e *Error) Error() string {
	msg := fmt.Sprintf("%s `%s` failed: %v; its output is in %s", e.Event, e.Command, e.Err, e.Log)
	if e.Tail != "" {
		msg += ":\n" + e.Tail
	}
	return msg
}

func (e *Error) Unwrap() error { return e.Err }

// Run runs h's commands in order and stops at the first that fails,
// returning an *Error. Each runs with /bin/sh -c in h.Dir, with no input and
// no terminal, in a process group of its own that is killed when Timeout
// passes or ctx ends. The output of the run replaces h's log.
func (r Runner) Run(ctx context.Context, h Hook) error {
	if len(h.Commands) == 0 {
		return nil
	}
	if err := os.MkdirAll(r.LogDir, 0o700); err != nil {
		return fmt.Errorf("%s: creating the hook log dir: %w", h.Event, err)
	}
	logPath := filepath.Join(r.LogDir, pathutil.FileName(h.Name)+"."+h.Event+".log")
	log, err := os.OpenFile(logPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("%s: opening its log: %w", h.Event, err)
	}
	defer log.Close()
	fmt.Fprintf(log, "# %s in %s, %s\n", h.Event, h.Dir, time.Now().Format(time.RFC3339))

	for _, command := range h.Commands {
		fmt.Fprintf(log, "$ %s\n", command)
		tail := &tailBuffer{max: tailBytes}
		out := io.MultiWriter(log, tail)
		if r.Echo != nil {
			out = io.MultiWriter(out, r.Echo)
		}
		if err := r.run(ctx, h, command, out); err != nil {
			fmt.Fprintf(log, "# failed: %v\n", err)
			return &Error{Event: h.Event, Command: command, Log: logPath, Tail: lastLines(string(tail.buf), tailLines), Err: err}
		}
	}
	return nil
}

func (r Runner) run(ctx context.Context, h Hook, command string, out io.Writer) error {
	if r.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.Timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", command)
	cmd.Dir = h.Dir
	cmd.Env = append(append(config.Environ(), gitops.BatchEnv()...), h.environ()...)
	cmd.Stdout, cmd.Stderr = out, out
	// A session of its own: no terminal to prompt on, and the whole group --
	// whatever the command started -- is killed with it.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = killGrace
	err := cmd.Run()
	if err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) && r.Timeout > 0 {
		return fmt.Errorf("timed out after %s (hooks.timeout): %w", r.Timeout, err)
	}
	return err
}

func (h Hook) environ() []string {
	env := append(slices.Clone(h.Vars), "GIT_MGR_EVENT="+h.Event, "GIT_MGR_PATH="+h.Dir)
	for k, v := range map[string]string{
		"GIT_MGR_REPO":      h.Env.Repo,
		"GIT_MGR_SOURCE":    h.Env.Source,
		"GIT_MGR_WORKSPACE": h.Env.Workspace,
		"GIT_MGR_AGENT":     h.Env.Agent,
		"GIT_MGR_BRANCH":    h.Env.Branch,
	} {
		if v != "" {
			env = append(env, k+"="+v)
		}
	}
	return env
}

// tailBuffer keeps the last max bytes written to it.
type tailBuffer struct {
	buf []byte
	max int
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if len(t.buf) > 2*t.max {
		t.buf = append([]byte(nil), t.buf[len(t.buf)-t.max:]...)
	}
	return len(p), nil
}

// lastLines is the last n lines of s.
func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
