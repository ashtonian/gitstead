package config

import (
	"path/filepath"
	"testing"
)

// Every example config in examples/ loads and validates, so none drifts from
// what gitstead accepts: each file, and each directory's config.yaml with its
// fragments, on every machine its sections name.
func TestExamplesLoad(t *testing.T) {
	examples := filepath.Join("..", "..", "examples")
	files, err := filepath.Glob(filepath.Join(examples, "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	dirs, err := filepath.Glob(filepath.Join(examples, "*", "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 || len(dirs) == 0 {
		t.Fatalf("examples found: %v, %v", files, dirs)
	}
	for _, path := range append(files, dirs...) {
		rel, _ := filepath.Rel(examples, path)
		t.Run(rel, func(t *testing.T) {
			if _, err := Load(t.Context(), path); err != nil {
				t.Error(err)
			}
		})
	}
	t.Run("fragments", func(t *testing.T) {
		cfg, err := Load(t.Context(), filepath.Join(examples, "fragments", "config.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		if len(cfg.Files()) != 3 || len(cfg.Accounts) != 2 || len(cfg.Workspaces) != 2 {
			t.Errorf("files %v, accounts %d, workspaces %d: want the base and both fragments", cfg.Files(), len(cfg.Accounts), len(cfg.Workspaces))
		}
	})
}
