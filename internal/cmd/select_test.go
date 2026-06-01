package cmd

import (
	"reflect"
	"testing"

	"github.com/ashtonian/git-mgr/internal/config"
	"github.com/ashtonian/git-mgr/internal/state"
)

func TestSelectRepos(t *testing.T) {
	st := &state.State{Repos: map[string]*state.RepoState{
		"github.com/acme/api": {Tags: []string{"backend"}},
		"github.com/acme/web": {Tags: []string{"frontend"}},
		"github.com/acme/old": {Tags: []string{"backend"}, IsArchived: true},
		"gitlab.com/x/tool":   {Tags: []string{"backend"}},
	}}
	cfg := &config.Config{Sets: []config.RepoSet{
		{Name: "acme", Repos: []string{"github.com/acme/*"}},
		{Name: "backend", Tags: []string{"backend"}},
		{Name: "active-backend", Tags: []string{"backend"}, ExcludeArchived: true},
		{Name: "mix", Repos: []string{"gitlab.com/*/*"}, Tags: []string{"frontend"}}, // glob is per-segment (filepath.Match)
	}}

	cases := []struct {
		name string
		sel  selection
		want []string
	}{
		{"glob", selection{repos: "github.com/acme/*"}, []string{"github.com/acme/api", "github.com/acme/old", "github.com/acme/web"}},
		{"tag", selection{tag: "backend"}, []string{"github.com/acme/api", "github.com/acme/old", "gitlab.com/x/tool"}},
		{"set-glob", selection{set: "acme"}, []string{"github.com/acme/api", "github.com/acme/old", "github.com/acme/web"}},
		{"set-tag", selection{set: "backend"}, []string{"github.com/acme/api", "github.com/acme/old", "gitlab.com/x/tool"}},
		{"set-exclude-archived", selection{set: "active-backend"}, []string{"github.com/acme/api", "gitlab.com/x/tool"}},
		{"set-union", selection{set: "mix"}, []string{"github.com/acme/web", "gitlab.com/x/tool"}},
		{"set-AND-inline-tag", selection{set: "acme", tag: "frontend"}, []string{"github.com/acme/web"}},
	}
	for _, c := range cases {
		got, err := selectRepos(cfg, st, c.sel)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}

	if _, err := selectRepos(cfg, st, selection{set: "nope"}); err == nil {
		t.Error("expected error for unknown set")
	}
}
