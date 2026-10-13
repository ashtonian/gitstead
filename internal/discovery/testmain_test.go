package discovery

import (
	"os"
	"testing"

	"github.com/ashtonian/git-mgr/internal/testenv"
)

func TestMain(m *testing.M) {
	os.Exit(testenv.Main(m))
}
