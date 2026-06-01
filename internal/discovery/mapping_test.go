package discovery

import (
	"testing"

	"github.com/google/go-github/v88/github"
	gitlab "gitlab.com/gitlab-org/api/client-go"
)

func TestGHCoreToRepoVisibilityInternal(t *testing.T) {
	c := &ghCore{host: "github.example.com", inlineTopics: true}
	r := c.toRepo(&github.Repository{
		Owner:      &github.User{Login: github.Ptr("acme")},
		Name:       github.Ptr("svc"),
		Visibility: github.Ptr("internal"),
		Private:    github.Ptr(true),
		Topics:     []string{"backend"},
		Language:   github.Ptr("Go"),
		Fork:       github.Ptr(false),
		Archived:   github.Ptr(false),
	})
	if r.Visibility != "internal" {
		t.Errorf("visibility = %q, want internal (real field, not the private bool)", r.Visibility)
	}
	if r.FullName != "acme/svc" {
		t.Errorf("fullname = %q", r.FullName)
	}
	if r.Language != "Go" {
		t.Errorf("language = %q", r.Language)
	}
	if len(r.Topics) != 1 || r.Topics[0] != "backend" {
		t.Errorf("inline topics = %v", r.Topics)
	}
}

func TestGHCoreToRepoVisibilityFallback(t *testing.T) {
	// No visibility field set; fall back to the private bool. Gitea-style core
	// (inlineTopics=false) must not populate topics inline.
	c := &ghCore{host: "gitea.example.com", inlineTopics: false}
	r := c.toRepo(&github.Repository{
		Owner:   &github.User{Login: github.Ptr("o")},
		Name:    github.Ptr("x"),
		Private: github.Ptr(true),
		Topics:  []string{"should-be-ignored"},
	})
	if r.Visibility != "private" {
		t.Errorf("fallback visibility = %q, want private", r.Visibility)
	}
	if r.Topics != nil {
		t.Errorf("non-inline core should not set topics, got %v", r.Topics)
	}
}

func TestGitLabToRepo(t *testing.T) {
	g := &GitLabProvider{host: "gitlab.example.com"}
	r := g.toRepo(&gitlab.Project{
		Path:         "proj",
		Namespace:    &gitlab.ProjectNamespace{FullPath: "grp/sub"},
		Visibility:   gitlab.InternalVisibility,
		Topics:       []string{"a"},
		Archived:     true,
		SSHURLToRepo: "git@gitlab.example.com:grp/sub/proj.git",
	})
	if r.Visibility != "internal" {
		t.Errorf("visibility = %q, want internal", r.Visibility)
	}
	if r.Owner != "grp/sub" || r.FullName != "grp/sub/proj" {
		t.Errorf("owner/fullname = %q / %q", r.Owner, r.FullName)
	}
	if !r.IsArchived {
		t.Error("expected archived")
	}
	if len(r.Topics) != 1 || r.Topics[0] != "a" {
		t.Errorf("topics = %v", r.Topics)
	}
}
