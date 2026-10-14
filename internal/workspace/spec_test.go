package workspace

import (
	"errors"
	"reflect"
	"testing"

	"github.com/ashtonian/gitstead/internal/config"
	"github.com/ashtonian/gitstead/internal/state"
)

func TestCopyDirs_UseAsLittleOfTheKeyAsTellsThemApart(t *testing.T) {
	got := copyDirs([]string{"github.com/acme/api", "github.com/other/api", "github.com/acme/web", "gitlab.com/acme/web"})
	want := map[string]string{
		"github.com/acme/api":  "acme/api",
		"github.com/other/api": "other/api",
		"github.com/acme/web":  "github.com/acme/web",
		"gitlab.com/acme/web":  "gitlab.com/acme/web",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("copyDirs = %v, want %v", got, want)
	}
	if got := copyDirs([]string{"github.com/a/api", "github.com/a/web"}); got["github.com/a/api"] != "api" {
		t.Errorf("distinct names should be used as they are: %v", got)
	}
}

func TestParseRef(t *testing.T) {
	for entry, want := range map[string]RefKind{
		"github.com/me/app":            RefKey,
		"github.com/me/*":              RefGlob,
		"/src/app":                     RefPath,
		"~/src/app":                    RefPath,
		"./app":                        RefPath,
		"git@github.com:me/app.git":    RefURL,
		"https://github.com/me/app":    RefURL,
		"ssh://git@host:22/me/app.git": RefURL,
	} {
		if got := ParseRef(entry).Kind; got != want {
			t.Errorf("ParseRef(%q) = %v, want %v", entry, got, want)
		}
	}
	if ref := ParseRef("git@github.com:me/app.git"); ref.Key != "github.com/me/app" {
		t.Errorf("key = %q", ref.Key)
	}
}

func TestRepoKeys(t *testing.T) {
	st := &state.State{Repos: map[string]*state.RepoState{
		"github.com/me/api": {LocalPath: "/src/api", Tags: []string{"backend"}},
		"github.com/me/web": {LocalPath: "/src/web"},
		"github.com/me/old": {LocalPath: "/src/old", Tags: []string{"backend"}, IsArchived: true},
	}}
	cfg := &config.Config{Sets: []config.RepoSet{{Name: "be", Tags: []string{"backend"}, ExcludeArchived: true}}}

	tests := []struct {
		decl config.Workspace
		want []string
		err  error
	}{
		{config.Workspace{Repos: []string{"github.com/me/web", "github.com/me/api"}}, []string{"github.com/me/web", "github.com/me/api"}, nil},
		{config.Workspace{Repos: []string{"github.com/me/*"}}, []string{"github.com/me/api", "github.com/me/old", "github.com/me/web"}, nil},
		{config.Workspace{Repos: []string{"/src/web", "git@github.com:me/web.git"}}, []string{"github.com/me/web"}, nil},
		{config.Workspace{Set: "be"}, []string{"github.com/me/api"}, nil},
		{config.Workspace{Repos: []string{"gitlab.com/*"}}, nil, ErrNoRepos},
	}
	for _, tt := range tests {
		got, err := RepoKeys(cfg, st, tt.decl)
		errOK := errors.Is(err, tt.err) || tt.err == nil && err == nil
		if !reflect.DeepEqual(got, tt.want) || !errOK {
			t.Errorf("RepoKeys(%+v) = %v, %v; want %v, %v", tt.decl, got, err, tt.want, tt.err)
		}
	}
	if _, err := RepoKeys(cfg, st, config.Workspace{Repos: []string{"github.com/me/nope"}}); err == nil {
		t.Error("an untracked key resolved")
	}
}
