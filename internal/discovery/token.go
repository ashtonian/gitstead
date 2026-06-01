package discovery

import (
	"os"
	"os/exec"
	"strings"

	"github.com/ashtonian/git-mgr/internal/config"
)

// ResolveToken reports the token git-mgr would use for a provider's API
// (explicit value, env var, or token_command output), or "" if none resolves.
// Exported for diagnostics (git-mgr doctor).
func ResolveToken(cfg config.Provider) string {
	return resolveToken(cfg)
}

// resolveToken resolves a provider token from, in order of precedence:
// the explicit value, the configured env var, any of the given default env
// vars, then the output of token_command. Returns "" if none yield a token.
func resolveToken(cfg config.Provider, defaultEnvs ...string) string {
	if cfg.Token != "" {
		return cfg.Token
	}
	if cfg.TokenEnv != "" {
		if v := os.Getenv(cfg.TokenEnv); v != "" {
			return v
		}
	}
	for _, env := range defaultEnvs {
		if v := os.Getenv(env); v != "" {
			return v
		}
	}
	if cfg.TokenCommand != "" {
		out, err := exec.Command("sh", "-c", cfg.TokenCommand).Output()
		if err == nil {
			return strings.TrimSpace(string(out))
		}
	}
	return ""
}
