package discovery

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/ashtonian/gitten/internal/config"
)

// tokenCommandTimeout bounds token_command, which may prompt or hang.
const tokenCommandTimeout = 30 * time.Second

// ResolveToken returns the token gitten would use for a provider's API, or
// "" for none. Exported for diagnostics (gitten doctor).
func ResolveToken(cfg config.Provider) (string, error) {
	return resolveToken(cfg, defaultTokenEnvs(cfg.Type)...)
}

func defaultTokenEnvs(providerType string) []string {
	switch config.NormalizeProviderType(providerType) {
	case "github":
		return []string{"GITHUB_TOKEN"}
	case "gitlab":
		return []string{"GITLAB_TOKEN"}
	case "gitea":
		return []string{"GITEA_TOKEN", "FORGEJO_TOKEN"}
	}
	return nil
}

// resolveToken returns a provider's token from the one source it configures:
// token, token_env or token_command. A configured source that yields nothing
// is an error -- never a reason to try another source, which would send one
// account's token to another account's API. Only a provider configuring none
// falls back to the type's default env vars, and then to no token, which is
// enough for public repos.
func resolveToken(cfg config.Provider, defaultEnvs ...string) (string, error) {
	switch {
	case cfg.Token != "":
		return cfg.Token, nil
	case cfg.TokenEnv != "":
		if v := os.Getenv(cfg.TokenEnv); v != "" {
			return v, nil
		}
		return "", fmt.Errorf("provider %q: token_env %s is not set", cfg.Name, cfg.TokenEnv)
	case cfg.TokenCommand != "":
		return runTokenCommand(cfg)
	}
	for _, env := range defaultEnvs {
		if v := os.Getenv(env); v != "" {
			return v, nil
		}
	}
	return "", nil
}

func runTokenCommand(cfg config.Provider) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), tokenCommandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", cfg.TokenCommand)
	// Killing sh leaves what it started; don't wait on it for the output.
	cmd.WaitDelay = 2 * time.Second
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if i := strings.IndexByte(msg, '\n'); i >= 0 {
			msg = msg[:i]
		}
		return "", fmt.Errorf("provider %q: token_command %q failed: %w: %s", cfg.Name, cfg.TokenCommand, err, msg)
	}
	tok := strings.TrimSpace(string(out))
	if tok == "" {
		return "", fmt.Errorf("provider %q: token_command %q printed nothing", cfg.Name, cfg.TokenCommand)
	}
	return tok, nil
}
