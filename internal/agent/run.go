package agent

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// RunOptions is one agent run.
type RunOptions struct {
	// Dir is the replica the agent works in.
	Dir string
	// Command is typed into an interactive shell, so shell functions and
	// aliases resolve.
	Command string
	// Env is added to the environment.
	Env []string
	// Shell is the shell to run Command in; "" for $SHELL, else /bin/sh.
	Shell string
}

// Shell is the user's shell: $SHELL, else /bin/sh.
func Shell() string {
	if sh := os.Getenv("SHELL"); sh != "" {
		return sh
	}
	return "/bin/sh"
}

// Run runs opts.Command in an interactive shell in opts.Dir, on this
// process's terminal, and returns its exit status (128+n when signal n ended
// it). The agent has the terminal, so Ctrl-C and Ctrl-\ are its own; this
// process catches them only so they don't end it first, and takes no
// context, which a Ctrl-C would cancel. SIGTERM and SIGHUP hang the agent up
// as closing its terminal would: SIGHUP to the terminal's foreground job and
// to the shell.
func Run(opts RunOptions) (int, error) {
	shell := opts.Shell
	if shell == "" {
		shell = Shell()
	}
	cmd := exec.Command(shell, "-i", "-c", opts.Command)
	cmd.Dir = opts.Dir
	cmd.Env = append(os.Environ(), opts.Env...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr

	// Caught, not ignored: an ignored signal would stay ignored in the agent.
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGQUIT, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sigs)

	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("starting %s: %w", shell, err)
	}
	done := make(chan struct{})
	defer close(done)
	go func() {
		for {
			select {
			case sig := <-sigs:
				if sig == syscall.SIGTERM || sig == syscall.SIGHUP {
					hangUp(cmd.Process)
				}
			case <-done:
				return
			}
		}
	}()

	err := cmd.Wait()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return 0, nil
	case errors.As(err, &exit):
		if ws, ok := exit.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return 128 + int(ws.Signal()), nil
		}
		return exit.ExitCode(), nil
	default:
		return 0, err
	}
}

// hangUp does what a terminal hanging up does: SIGHUP to its foreground job
// -- the agent, which an interactive shell runs in a process group of its
// own and doesn't pass the signal to -- and to the shell.
func hangUp(shell *os.Process) {
	if fg, err := unix.IoctlGetInt(int(os.Stdin.Fd()), unix.TIOCGPGRP); err == nil && fg > 0 && fg != syscall.Getpgrp() {
		_ = syscall.Kill(-fg, syscall.SIGHUP)
	}
	_ = shell.Signal(syscall.SIGHUP)
}

// Stop ends the run holding the lease at path: SIGTERM to it, which the run
// passes on as SIGHUP, then up to timeout for the lease to be released. With
// kill, a run still holding it then is killed with everything it started.
// It reports whether the run ended; a lease no one holds counts as ended.
func Stop(path string, timeout time.Duration, kill bool) (bool, error) {
	h, held := Read(path)
	if !held {
		return true, nil
	}
	if h.PID <= 0 {
		return false, fmt.Errorf("the lease at %s doesn't say which process holds it", path)
	}
	if err := syscall.Kill(h.PID, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return false, fmt.Errorf("signalling pid %d: %w", h.PID, err)
	}
	const poll = 100 * time.Millisecond
	if Wait(path, timeout, poll) {
		return true, nil
	}
	if !kill {
		return false, nil
	}
	for _, pid := range append(Descendants(h.PID), h.PID) {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
	return Wait(path, 5*time.Second, poll), nil
}

// Descendants lists the processes descended from pid, deepest first, from
// ps; nil when ps can't be read.
func Descendants(pid int) []int {
	out, err := exec.Command("ps", "-A", "-o", "pid=,ppid=").Output()
	if err != nil {
		return nil
	}
	children := map[int][]int{}
	for _, line := range bytes.Split(out, []byte("\n")) {
		f := strings.Fields(string(line))
		if len(f) != 2 {
			continue
		}
		p, err1 := strconv.Atoi(f[0])
		pp, err2 := strconv.Atoi(f[1])
		if err1 == nil && err2 == nil {
			children[pp] = append(children[pp], p)
		}
	}
	var walk func(int) []int
	walk = func(p int) []int {
		var out []int
		for _, c := range children[p] {
			out = append(out, walk(c)...)
			out = append(out, c)
		}
		return out
	}
	return walk(pid)
}
