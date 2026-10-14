package gitops_test

import (
	"os"
	"testing"

	"github.com/ashtonian/gitstead/internal/testenv"
)

// TestMain isolates the tests from the developer's git configuration -- a
// global commit.gpgsign, hooks or init template otherwise changes what the
// fixtures create -- and from the repository and home they run in.
func TestMain(m *testing.M) {
	os.Exit(testenv.Main(m))
}
