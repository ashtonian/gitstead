package jump

import (
	"strings"
	"testing"

	"github.com/ashtonian/gitten/internal/state"
)

func fixture() *state.State {
	return &state.State{
		Repos: map[string]*state.RepoState{
			"github.com/me/app":      {Name: "app", FullName: "me/app", LocalPath: "/src/me/app"},
			"github.com/me/webhooks": {Name: "webhooks", FullName: "me/webhooks", LocalPath: "/src/me/webhooks"},
			"github.com/acme/api":    {Name: "api", FullName: "acme/api", LocalPath: "/src/acme/api"},
			"gitlab.com/other/api":   {Name: "api", FullName: "other/api", LocalPath: "/src/other/api"},
			"github.com/me/worker":   {Name: "worker", FullName: "me/worker", LocalPath: "/src/me/worker"},
		},
		Workspaces: map[string]*state.WorkspaceState{
			"app": {Name: "app", Dir: "/ws/app", Replicas: []state.ReplicaState{
				{Agent: "agent-1", Path: "/ws/app/agent-1", Copies: []state.CopyState{{Repo: "github.com/me/app", Path: "/ws/app/agent-1"}}},
				{Agent: "agent-2", Path: "/ws/app/agent-2", Copies: []state.CopyState{{Repo: "github.com/me/app", Path: "/ws/app/agent-2"}}},
			}},
			"platform": {Name: "platform", Dir: "/ws/platform", Replicas: []state.ReplicaState{
				{Agent: "agent-1", Path: "/ws/platform/agent-1", Copies: []state.CopyState{
					{Repo: "github.com/acme/api", Path: "/ws/platform/agent-1/api"},
					{Repo: "github.com/me/worker", Path: "/ws/platform/agent-1/worker"},
				}},
			}},
		},
	}
}

func best(t *testing.T, query string) []string {
	t.Helper()
	var labels []string
	for _, m := range Best(Find(Targets(fixture()), query)) {
		labels = append(labels, m.Label)
	}
	return labels
}

func TestFind(t *testing.T) {
	for _, tt := range []struct{ query, want string }{
		{"app", "github.com/me/app"},                        // a repo before the workspace of its name
		{"acme/api", "github.com/acme/api"},                 // a path suffix
		{"api", "github.com/acme/api gitlab.com/other/api"}, // equally good: the caller asks
		{"app agent-2", "app/agent-2"},                      // every word must fit
		{"app 2", "app/agent-2"},
		{"platform worker", "platform/agent-1/worker"},
		{"wkr", "github.com/me/worker"},    // letters in order
		{"WEBH", "github.com/me/webhooks"}, // case doesn't matter
		{"nothing-like-it", ""},
	} {
		if got := strings.Join(best(t, tt.query), " "); got != tt.want {
			t.Errorf("Find(%q) best = %q, want %q", tt.query, got, tt.want)
		}
	}
}

func TestTargets(t *testing.T) {
	kinds := map[string]string{}
	for _, tg := range Targets(fixture()) {
		kinds[tg.Label] = tg.Kind + " " + tg.Path + " " + tg.Repo
	}
	for label, want := range map[string]string{
		"app":                  "workspace /ws/app ",
		"app/agent-1":          "agent /ws/app/agent-1 github.com/me/app",
		"platform/agent-1":     "agent /ws/platform/agent-1 ",
		"platform/agent-1/api": "copy /ws/platform/agent-1/api github.com/acme/api",
		"github.com/acme/api":  "repo /src/acme/api github.com/acme/api",
	} {
		if kinds[label] != want {
			t.Errorf("%s = %q, want %q", label, kinds[label], want)
		}
	}
}

func TestOfKind(t *testing.T) {
	m := Best(Find(OfKind(Targets(fixture()), KindWorkspace), "app"))
	if len(m) != 1 || m[0].Label != "app" || m[0].Path != "/ws/app" {
		t.Errorf("the workspace app = %+v", m)
	}
}
