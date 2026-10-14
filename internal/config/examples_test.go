package config

import (
	"path/filepath"
	"testing"
)

// Every example config in examples/ loads and validates, so none drifts from
// what gitstead accepts.
func TestExamplesLoad(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("..", "..", "examples", "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("no examples found")
	}
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			if _, err := Load(t.Context(), path); err != nil {
				t.Error(err)
			}
		})
	}
}
