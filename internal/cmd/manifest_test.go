package cmd

import (
	"path/filepath"
	"testing"

	"github.com/ashtonian/git-mgr/internal/config"
	"github.com/ashtonian/git-mgr/internal/repourl"
)

func TestRelToBase(t *testing.T) {
	base := "/home/u/repos"
	if got := relToBase("/home/u/repos/github.com/a/b", base); got != "github.com/a/b" {
		t.Errorf("under base: got %q, want github.com/a/b", got)
	}
	if got := relToBase("/elsewhere/x", base); got != "/elsewhere/x" {
		t.Errorf("outside base: got %q, want /elsewhere/x", got)
	}
}

func TestManifestDest(t *testing.T) {
	cfg := &config.Config{BaseDir: "/b", PathTemplate: "{{.Host}}/{{.Owner}}/{{.Name}}"}
	id := &repourl.Identity{Host: "github.com", Owner: "a", Name: "b", FullName: "a/b"}

	for _, tt := range []struct {
		path, want string
	}{
		{"x/y", filepath.Join("/b", "x/y")},
		{"/abs/p", "/abs/p"},
		{"", filepath.Join("/b", "github.com/a/b")},
	} {
		got, err := manifestDest(cfg, manifestRepo{Path: tt.path}, id)
		if err != nil || got != tt.want {
			t.Errorf("manifestDest(path %q) = %q, %v; want %q", tt.path, got, err, tt.want)
		}
	}
}
