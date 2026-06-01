package cmd

import (
	"path/filepath"
	"testing"

	"github.com/ashtonian/git-mgr/internal/config"
	"github.com/ashtonian/git-mgr/internal/gitops"
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
	id := &gitops.RepoIdentity{Host: "github.com", Owner: "a", Name: "b", FullName: "a/b"}

	if got := manifestDest(cfg, manifestRepo{Path: "x/y"}, id); got != filepath.Join("/b", "x/y") {
		t.Errorf("relative path: got %q", got)
	}
	if got := manifestDest(cfg, manifestRepo{Path: "/abs/p"}, id); got != "/abs/p" {
		t.Errorf("absolute path: got %q", got)
	}
	if got := manifestDest(cfg, manifestRepo{}, id); got != filepath.Join("/b", "github.com/a/b") {
		t.Errorf("template path: got %q", got)
	}
}
