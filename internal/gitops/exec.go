package gitops

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Git executes a git command in the given repo directory and returns stdout.
func Git(ctx context.Context, repoPath string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", repoPath}, args...)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w\n%s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

// GitWithSSHKey executes a git command with a custom SSH key.
func GitWithSSHKey(ctx context.Context, repoPath, sshKeyPath string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", repoPath}, args...)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if sshKeyPath != "" {
		cmd.Env = append(os.Environ(), gitSSHCommandEnv(sshKeyPath))
	}
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w\n%s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

// GitExitCode runs a git command and returns just the exit code.
func GitExitCode(ctx context.Context, repoPath string, args ...string) int {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", repoPath}, args...)...)
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Run(); err != nil {
		if cmd.ProcessState != nil {
			return cmd.ProcessState.ExitCode()
		}
		return -1
	}
	return 0
}

// GitClone clones a repo. Does not use -C since the target doesn't exist yet.
func GitClone(ctx context.Context, sshURL, destPath, sshKeyPath string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "clone", sshURL, destPath)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if sshKeyPath != "" {
		cmd.Env = append(os.Environ(), gitSSHCommandEnv(sshKeyPath))
	}
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git clone %s: %w\n%s", sshURL, err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

// gitSSHCommandEnv builds the GIT_SSH_COMMAND env entry pointing git at a
// specific SSH key. The key path is shell-quoted because git runs
// GIT_SSH_COMMAND through the shell, so an unquoted path containing spaces
// would otherwise be split into separate arguments.
func gitSSHCommandEnv(sshKeyPath string) string {
	return fmt.Sprintf(
		"GIT_SSH_COMMAND=ssh -i %s -o IdentitiesOnly=yes -o StrictHostKeyChecking=accept-new",
		shellQuote(sshKeyPath),
	)
}

// shellQuote wraps s in single quotes for safe use inside a shell command,
// escaping any embedded single quotes.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
