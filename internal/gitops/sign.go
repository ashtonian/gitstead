package gitops

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"time"
)

// signTestTimeout bounds a test signature: a signer that would prompt must
// fail, not hang.
const signTestTimeout = 20 * time.Second

// TestSign makes a throwaway signed commit with settings (git config keys and
// values) in a temporary repository, with no terminal, as an agent would. It
// returns why signing failed, or nil if it works unattended.
func TestSign(ctx context.Context, settings map[string]string) error {
	dir, err := os.MkdirTemp("", "git-mgr-signtest-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	ctx, cancel := context.WithTimeout(ctx, signTestTimeout)
	defer cancel()

	if _, err := Git(ctx, dir, "init", "-q"); err != nil {
		return err
	}
	tree, err := gitWithStdin(ctx, dir, nil, nil, "mktree")
	if err != nil {
		return err
	}
	// A fallback identity first; the account's own, if any, overrides it.
	args := []string{"-c", "user.name=git-mgr", "-c", "user.email=git-mgr@localhost"}
	for _, k := range slices.Sorted(maps.Keys(settings)) {
		args = append(args, "-c", k+"="+settings[k])
	}
	args = append(args, "commit-tree", "-S", tree, "-m", "git-mgr signing test")
	cmd := gitCommand(ctx, dir, []string{"GIT_TERMINAL_PROMPT=0"}, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("signing did not finish within %s; it probably needs a terminal or a passphrase", signTestTimeout)
		}
		reason := strings.TrimSpace(stderr.String())
		if reason == "" {
			reason = err.Error()
		}
		return errors.New(strings.ReplaceAll(reason, "\n", "; "))
	}
	return nil
}
