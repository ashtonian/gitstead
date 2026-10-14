package discovery

import (
	"strings"
	"testing"

	"github.com/ashtonian/gitten/internal/config"
)

func TestResolveToken(t *testing.T) {
	t.Setenv("GITMGR_TEST_ENV", "envval")
	t.Setenv("GITMGR_TEST_DEFAULT", "defval")
	t.Setenv("GITMGR_TEST_EMPTY", "")

	tests := []struct {
		name    string
		cfg     config.Provider
		want    string
		wantErr string
	}{
		{"explicit token", config.Provider{Token: "explicit"}, "explicit", ""},
		{"token_env", config.Provider{TokenEnv: "GITMGR_TEST_ENV"}, "envval", ""},
		{"token_command", config.Provider{TokenCommand: "echo cmdval"}, "cmdval", ""},
		{"nothing configured falls back to the default env", config.Provider{}, "defval", ""},
		// A configured source that yields nothing must never fall through to the
		// default env, which may hold another account's token.
		{"unset token_env fails closed", config.Provider{Name: "work", TokenEnv: "GITMGR_TEST_EMPTY"}, "", "token_env GITMGR_TEST_EMPTY is not set"},
		{"failing token_command fails closed", config.Provider{Name: "work", TokenCommand: "echo nope >&2; exit 3"}, "", "failed: exit status 3: nope"},
		{"silent token_command fails closed", config.Provider{Name: "work", TokenCommand: "true"}, "", "printed nothing"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveToken(tt.cfg, "GITMGR_TEST_DEFAULT")
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("got %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}

func TestResolveToken_NoSourceNoDefaultIsAnonymous(t *testing.T) {
	got, err := resolveToken(config.Provider{}, "GITMGR_TEST_UNSET_DEFAULT")
	if got != "" || err != nil {
		t.Fatalf("got %q, %v; want no token and no error", got, err)
	}
}
