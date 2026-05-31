package discovery

import (
	"testing"

	"github.com/ashtonian/git-mgr/internal/config"
)

func TestResolveTokenPrecedence(t *testing.T) {
	if got := resolveToken(config.Provider{Token: "explicit"}, "GITMGR_TEST_UNSET"); got != "explicit" {
		t.Errorf("explicit token: got %q", got)
	}

	t.Setenv("GITMGR_TEST_ENV", "envval")
	if got := resolveToken(config.Provider{TokenEnv: "GITMGR_TEST_ENV"}, "GITMGR_TEST_DEFAULT"); got != "envval" {
		t.Errorf("token_env: got %q", got)
	}

	t.Setenv("GITMGR_TEST_DEFAULT", "defval")
	if got := resolveToken(config.Provider{}, "GITMGR_TEST_DEFAULT"); got != "defval" {
		t.Errorf("default env: got %q", got)
	}

	if got := resolveToken(config.Provider{TokenCommand: "echo cmdval"}, "GITMGR_TEST_UNSET"); got != "cmdval" {
		t.Errorf("token_command: got %q", got)
	}

	if got := resolveToken(config.Provider{}, "GITMGR_TEST_UNSET"); got != "" {
		t.Errorf("no token: got %q", got)
	}
}
