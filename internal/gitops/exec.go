package gitops

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/ashtonian/git-mgr/internal/shellutil"
)

// gitCommand builds a git invocation in repoPath with env appended to the
// process environment. --no-optional-locks keeps read-only commands such as
// status from refreshing the index under an agent's running git command.
func gitCommand(ctx context.Context, repoPath string, env []string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", append([]string{"--no-optional-locks", "-C", repoPath}, args...)...)
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	return cmd
}

// runGit runs cmd and returns its stdout, folding stderr into the error.
func runGit(cmd *exec.Cmd, args []string) ([]byte, error) {
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git %s: %w\n%s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// Git executes a git command in the given repo directory and returns stdout
// with surrounding whitespace trimmed. Use GitOutput when the bytes matter.
func Git(ctx context.Context, repoPath string, args ...string) (string, error) {
	out, err := GitOutput(ctx, repoPath, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// GitOutput executes a git command in the given repo directory and returns
// stdout unchanged. Patches need this: trimming drops the final newline and
// leaves a patch git refuses to apply.
func GitOutput(ctx context.Context, repoPath string, args ...string) ([]byte, error) {
	return runGit(gitCommand(ctx, repoPath, nil, args...), args)
}

// GitWithSSHKey executes a git command with a custom SSH key.
func GitWithSSHKey(ctx context.Context, repoPath, sshKeyPath string, args ...string) (string, error) {
	var env []string
	if sshKeyPath != "" {
		env = []string{gitSSHCommandEnv(sshKeyPath)}
	}
	out, err := runGit(gitCommand(ctx, repoPath, env, args...), args)
	return strings.TrimSpace(string(out)), err
}

// gitWithEnv executes a git command with env appended to the process
// environment and returns stdout trimmed of surrounding whitespace.
func gitWithEnv(ctx context.Context, repoPath string, env []string, args ...string) (string, error) {
	out, err := runGit(gitCommand(ctx, repoPath, env, args...), args)
	return strings.TrimSpace(string(out)), err
}

// gitWithStdin is gitWithEnv with stdin fed to the command.
func gitWithStdin(ctx context.Context, repoPath string, env []string, stdin []byte, args ...string) (string, error) {
	cmd := gitCommand(ctx, repoPath, env, args...)
	cmd.Stdin = bytes.NewReader(stdin)
	out, err := runGit(cmd, args)
	return strings.TrimSpace(string(out)), err
}

// GitExitCode runs a git command and returns just the exit code.
func GitExitCode(ctx context.Context, repoPath string, args ...string) int {
	cmd := gitCommand(ctx, repoPath, nil, args...)
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
		shellutil.Quote(sshKeyPath),
	)
}
