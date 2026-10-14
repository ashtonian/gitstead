package launch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/ashtonian/gitstead/internal/shellutil"
)

// commandTimeout bounds each command a launcher runs and waits for.
const commandTimeout = 60 * time.Second

// runner runs programs on this machine, or on host over ssh.
type runner struct {
	host string
}

// argv is the command line that runs name with args: as it is here, or
// through ssh -t on the host, for a program that needs the terminal.
func (r runner) argv(name string, args ...string) []string {
	if r.host == "" {
		return append([]string{name}, args...)
	}
	return []string{"ssh", "-t", r.host, remoteLine(name, args...)}
}

// output runs name with args and returns its stdout; over ssh, in batch
// mode, so it fails instead of prompting.
func (r runner) output(ctx context.Context, stdin []byte, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	argv := append([]string{name}, args...)
	if r.host != "" {
		argv = append(append([]string{"ssh"}, sshOptions()...), r.host, remoteLine(name, args...))
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		where := ""
		if r.host != "" {
			where = " on " + r.host
		}
		if ctx.Err() != nil {
			return nil, fmt.Errorf("%s%s timed out after %s", name, where, commandTimeout)
		}
		return nil, fmt.Errorf("%s %s%s: %w: %s", name, strings.Join(args, " "), where, err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// sshOptions make ssh fail rather than prompt, and share one connection
// between the many commands a launcher runs on a host.
func sshOptions() []string {
	return []string{
		"-o", "BatchMode=yes",
		"-o", "ControlMaster=auto",
		"-o", "ControlPath=" + filepath.Join(os.TempDir(), "gitstead-ssh-%C"),
		"-o", "ControlPersist=60",
	}
}

// remoteLine is name and args as one line for the remote shell, each
// argument quoted.
func remoteLine(name string, args ...string) string {
	parts := []string{shellutil.Word(name)}
	for _, a := range args {
		parts = append(parts, shellutil.Word(a))
	}
	return strings.Join(parts, " ")
}

// RemotePlan gets a fleet's launch plan from gitstead on host: the
// workspaces live there, so it knows their paths and how to run them.
// remoteCommand runs gitstead there, with any flags it needs, as written.
func RemotePlan(ctx context.Context, host, remoteCommand, fleet string, args ...string) (Plan, error) {
	if remoteCommand == "" {
		remoteCommand = "gitstead"
	}
	line := remoteCommand + " " + remoteLine("fleet", append([]string{"plan", fleet, "--json"}, args...)...)
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ssh", append(sshOptions(), host, line)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return Plan{}, fmt.Errorf("getting fleet %s's plan from %s (%s): %w: %s", fleet, host, line, err, strings.TrimSpace(stderr.String()))
	}
	var p Plan
	if err := json.Unmarshal(stdout.Bytes(), &p); err != nil {
		return Plan{}, fmt.Errorf("reading fleet %s's plan from %s: %w", fleet, host, err)
	}
	p.Host = host
	return p, nil
}
