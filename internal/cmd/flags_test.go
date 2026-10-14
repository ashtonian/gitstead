package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alecthomas/kong"
	"github.com/ashtonian/pkg/config/kongprovider"
)

// --concurrency sets config's concurrency as the loader's highest source:
// over the file, validated with it, and masking nothing when left out.
func TestConfigFlags_Concurrency(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("concurrency: 2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	load := func(args ...string) (int, error) {
		parser, err := kong.New(&CLI{}, kong.Vars{"version": "test"})
		if err != nil {
			t.Fatal(err)
		}
		kctx, err := parser.Parse(args)
		if err != nil {
			t.Fatal(err)
		}
		ctx := &Context{Config: path, Ctx: t.Context(), Flags: kongprovider.New(kctx, ConfigFlags)}
		cfg, err := ctx.LoadConfig()
		if err != nil {
			return 0, err
		}
		return cfg.Concurrency, nil
	}

	if n, err := load("version"); err != nil || n != 2 {
		t.Errorf("without the flag: %d, %v; want the file's 2", n, err)
	}
	if n, err := load("-j", "3", "version"); err != nil || n != 3 {
		t.Errorf("-j 3: %d, %v; want 3", n, err)
	}
	if n, err := load("version", "--concurrency=5"); err != nil || n != 5 {
		t.Errorf("--concurrency=5: %d, %v; want 5", n, err)
	}
	if _, err := load("-j", "0", "version"); err == nil || !strings.Contains(err.Error(), "concurrency must be >= 1") {
		t.Errorf("-j 0: %v, want it refused", err)
	}
}
