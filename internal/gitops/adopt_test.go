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
		// GitLab subgroups: owner is the full group path, name is just the repo.
		{"git@gitlab.com:group/subgroup/project.git", "gitlab.com", "group/subgroup", "project", "group/subgroup/project"},
		// ssh:// scheme, with a port.
		{"ssh://git@ghes.company.com:2222/team/svc.git", "ghes.company.com", "team", "svc", "team/svc"},
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

func TestParseURL_HTTPSPort(t *testing.T) {
	// A port and userinfo must be stripped from the host.
	id, err := gitops.ParseURL("https://user@git.example.com:8443/team/app.git")
	if err != nil {
		t.Fatalf("ParseURL: %v", err)
	}
	if id.Host != "git.example.com" {
		t.Errorf("host: got %q, want git.example.com (port/userinfo stripped)", id.Host)
	}
	if id.FullName != "team/app" {
		t.Errorf("fullname: got %q, want team/app", id.FullName)
	}
}

func TestParseURL_GitProtocol(t *testing.T) {
	id, err := gitops.ParseURL("git://host.tld/owner/repo.git")
	if err != nil {
		t.Fatalf("ParseURL: %v", err)
	}
	if id.Host != "host.tld" || id.FullName != "owner/repo" {
		t.Errorf("git://: got %s %s", id.Host, id.FullName)
	}
}

func TestParseURL_LocalPath(t *testing.T) {
	cases := []struct{ in, owner, name string }{
		{"/srv/git/origin.git", "git", "origin"},
		{"/tmp/wgr/origin.git", "wgr", "origin"},
		{"~/repos/myapp", "repos", "myapp"},
		{"file:///srv/mirrors/app.git", "mirrors", "app"},
	}
	for _, c := range cases {
		id, err := gitops.ParseURL(c.in)
		if err != nil {
			t.Fatalf("ParseURL(%q): %v", c.in, err)
		}
		if id.Host != "local" || id.Owner != c.owner || id.Name != c.name {
			t.Errorf("ParseURL(%q) = local/%s/%s, want local/%s/%s", c.in, id.Owner, id.Name, c.owner, c.name)
		}
		if id.SSHURL == "" {
			t.Errorf("ParseURL(%q): empty SSHURL (clone source)", c.in)
		}
	}
}

func TestParseURL_Invalid(t *testing.T) {
	invalids := []string{
		"not-a-url",              // bare token, not path-like
		"ftp://example.com/repo", // unsupported scheme
		"",                       // empty
		"https://github.com",     // no owner/repo path
		"git@github.com:",        // no path
	}
	for _, url := range invalids {
		if _, err := gitops.ParseURL(url); err == nil {
			t.Errorf("expected error for %q", url)
		}
	}
}
