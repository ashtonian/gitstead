package config_test

import (
	"os"
	"testing"

	"github.com/ashtonian/gitstead/internal/testenv"
)

func TestMain(m *testing.M) {
	os.Exit(testenv.Main(m))
}
