package gitops_test

import (
	"testing"

	"github.com/ashtonian/git-mgr/internal/gitops"
)

func TestParseURL_SSH(t *testing.T) {
	tests := []struct {
		url      string
		host     string
		owner    string
		name     string
		fullName string
	}{
		{"git@github.com:ashtonian/git-mgr.git", "github.com", "ashtonian", "git-mgr", "ashtonian/git-mgr"},
		{"git@ghes.company.com:org/repo.git", "ghes.company.com", "org", "repo", "org/repo"},
		{"git@gitlab.com:group/subgroup/project.git", "gitlab.com", "group", "subgroup/project", "group/subgroup/project"},
	}

	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			id, err := gitops.ParseURL(tt.url)
			if err != nil {
				t.Fatalf("ParseURL(%q) failed: %v", tt.url, err)
			}
			if id.Host != tt.host {
				t.Errorf("host: got %q, want %q", id.Host, tt.host)
			}
			if id.Owner != tt.owner {
				t.Errorf("owner: got %q, want %q", id.Owner, tt.owner)
			}
			if id.Name != tt.name {
				t.Errorf("name: got %q, want %q", id.Name, tt.name)
			}
		})
	}
}

func TestParseURL_HTTPS(t *testing.T) {
	tests := []struct {
		url  string
		host string
		name string
	}{
		{"https://github.com/ashtonian/git-mgr.git", "github.com", "git-mgr"},
		{"https://github.com/ashtonian/git-mgr", "github.com", "git-mgr"},
		{"https://ghes.company.com/org/repo.git", "ghes.company.com", "repo"},
	}

	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			id, err := gitops.ParseURL(tt.url)
			if err != nil {
				t.Fatalf("ParseURL(%q) failed: %v", tt.url, err)
			}
			if id.Host != tt.host {
				t.Errorf("host: got %q, want %q", id.Host, tt.host)
			}
			if id.Name != tt.name {
				t.Errorf("name: got %q, want %q", id.Name, tt.name)
			}
			if id.SSHURL == "" {
				t.Error("expected SSH URL to be generated")
			}
		})
	}
}

func TestParseURL_Invalid(t *testing.T) {
	invalids := []string{
		"not-a-url",
		"ftp://example.com/repo",
		"",
	}
	for _, url := range invalids {
		_, err := gitops.ParseURL(url)
		if err == nil {
			t.Errorf("expected error for %q", url)
		}
	}
}
