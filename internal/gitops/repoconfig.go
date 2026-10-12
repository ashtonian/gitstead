package gitops

import (
	"bytes"
	"context"
	"fmt"
)

// LocalConfig returns the repository's own git config (not global or system),
// keyed by git's lower-case names such as core.sshcommand and
// remote.origin.url. For a key set more than once the last value wins, as it
// does for git.
func LocalConfig(ctx context.Context, repoPath string) (map[string]string, error) {
	out, err := GitOutput(ctx, repoPath, "config", "--local", "-z", "--list")
	if err != nil {
		return nil, err
	}
	cfg := map[string]string{}
	for _, entry := range bytes.Split(bytes.TrimSuffix(out, []byte{0}), []byte{0}) {
		if len(entry) == 0 {
			continue
		}
		key, value, _ := bytes.Cut(entry, []byte{'\n'})
		cfg[string(key)] = string(value)
	}
	return cfg, nil
}

// SetLocalConfig sets key to value in the repository's own git config.
func SetLocalConfig(ctx context.Context, repoPath, key, value string) error {
	if _, err := Git(ctx, repoPath, "config", "--local", key, value); err != nil {
		return fmt.Errorf("setting %s in %s: %w", key, repoPath, err)
	}
	return nil
}

// UnsetLocalConfig removes key from the repository's own git config.
func UnsetLocalConfig(ctx context.Context, repoPath, key string) error {
	if _, err := Git(ctx, repoPath, "config", "--local", "--unset-all", key); err != nil {
		return fmt.Errorf("unsetting %s in %s: %w", key, repoPath, err)
	}
	return nil
}

// SetOriginURL points the origin remote at url.
func SetOriginURL(ctx context.Context, repoPath, url string) error {
	if _, err := Git(ctx, repoPath, "remote", "set-url", "origin", url); err != nil {
		return fmt.Errorf("setting origin of %s: %w", repoPath, err)
	}
	return nil
}
