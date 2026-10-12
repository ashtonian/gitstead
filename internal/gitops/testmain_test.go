package gitops_test

import (
	"os"
	"testing"
)

// TestMain isolates the tests from the developer's git configuration: a global
// commit.gpgsign, hooks or init template otherwise changes what the fixtures
// create (a signing prompt left them with no commits during review).
func TestMain(m *testing.M) {
	os.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	os.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	os.Exit(m.Run())
}
