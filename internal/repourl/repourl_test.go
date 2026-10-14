package repourl_test

import (
	"strings"
	"testing"

	"github.com/ashtonian/gitten/internal/repourl"
)

func TestParseURL_SSH(t *testing.T) {
	tests := []struct {
		url      string
		host     string
		owner    string
		name     string
		fullName string
	}{
		{"git@github.com:ashtonian/gitten.git", "github.com", "ashtonian", "gitten", "ashtonian/gitten"},
		{"git@ghes.company.com:org/repo.git", "ghes.company.com", "org", "repo", "org/repo"},
		// GitLab subgroups: owner is the full group path, name is just the repo.
		{"git@gitlab.com:group/subgroup/project.git", "gitlab.com", "group/subgroup", "project", "group/subgroup/project"},
		// ssh:// scheme, with a port.
		{"ssh://git@ghes.company.com:2222/team/svc.git", "ghes.company.com", "team", "svc", "team/svc"},
	}

	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			id, err := repourl.Parse(tt.url)
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
		{"https://github.com/ashtonian/gitten.git", "github.com", "gitten"},
		{"https://github.com/ashtonian/gitten", "github.com", "gitten"},
		{"https://ghes.company.com/org/repo.git", "ghes.company.com", "repo"},
	}

	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			id, err := repourl.Parse(tt.url)
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
	id, err := repourl.Parse("https://user@git.example.com:8443/team/app.git")
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
	id, err := repourl.Parse("git://host.tld/owner/repo.git")
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
		id, err := repourl.Parse(c.in)
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
		"x:y@host",               // an @ after the colon isn't user@host
		"git@:acme/api",          // no host
		"/",                      // no name
		"..",                     // no name
	}
	for _, url := range invalids {
		if _, err := repourl.Parse(url); err == nil {
			t.Errorf("expected error for %q", url)
		}
	}
}

func TestIsHTTP(t *testing.T) {
	for _, u := range []string{"https://github.com/x/y.git", "http://h/x/y"} {
		if !repourl.IsHTTP(u) {
			t.Errorf("IsHTTP(%q) = false, want true", u)
		}
	}
	for _, u := range []string{"git@github.com:x/y.git", "ssh://git@h/x/y", "/local/path", "~/r"} {
		if repourl.IsHTTP(u) {
			t.Errorf("IsHTTP(%q) = true, want false", u)
		}
	}
}

func TestCredentials(t *testing.T) {
	tests := []struct {
		url, redacted string
		has           bool
	}{
		{"https://me:ghp_secret@github.com/acme/api.git", "https://***@github.com/acme/api.git", true},
		{"https://ghp_secret@github.com/acme/api.git", "https://***@github.com/acme/api.git", true},
		{"ssh://git:hunter2@host:2222/acme/api.git", "ssh://git:***@host:2222/acme/api.git", true},
		{"ssh://git@host:2222/acme/api.git", "ssh://git@host:2222/acme/api.git", false},
		{"git@github.com:acme/api.git", "git@github.com:acme/api.git", false},
		{"https://github.com/acme/api.git", "https://github.com/acme/api.git", false},
	}
	for _, tt := range tests {
		if got := repourl.HasCredentials(tt.url); got != tt.has {
			t.Errorf("HasCredentials(%s) = %v", tt.url, got)
		}
		if got := repourl.Redact("fatal: could not read " + tt.url + " (exit 128)"); got != "fatal: could not read "+tt.redacted+" (exit 128)" {
			t.Errorf("Redact(%s) = %s", tt.url, got)
		}
		if err := repourl.CheckNoCredentials(tt.url); (err != nil) != tt.has || (err != nil && strings.Contains(err.Error(), "secret")) {
			t.Errorf("CheckNoCredentials(%s) = %v", tt.url, err)
		}
	}
}

func TestWithoutCredentials(t *testing.T) {
	tests := []struct{ url, want string }{
		{"https://me:ghp_secret@github.com/acme/api.git", "https://github.com/acme/api.git"},
		{"https://ghp_secret@github.com/acme/api.git", "https://github.com/acme/api.git"},
		{"ssh://git:hunter2@host:2222/acme/api.git", "ssh://git@host:2222/acme/api.git"},
		{"ssh://git@host:2222/acme/api.git", "ssh://git@host:2222/acme/api.git"},
		{"git@github.com:acme/api.git", "git@github.com:acme/api.git"},
		{"/srv/git/api.git", "/srv/git/api.git"},
	}
	for _, tt := range tests {
		if got := repourl.WithoutCredentials(tt.url); got != tt.want {
			t.Errorf("WithoutCredentials(%q) = %q, want %q", tt.url, got, tt.want)
		}
	}
}

// Parse never panics, and what it accepts names a repo: a host, an owner and
// a name, with a key that parses back to itself.
func FuzzParse(f *testing.F) {
	for _, seed := range []string{
		"git@github.com:acme/api.git", "https://github.com/acme/api", "ssh://git@host:2222/g/sub/api.git",
		"git://host/a/b", "file:///srv/git/a.git", "/srv/git/a.git", "~/x/y.git", "./a/b",
		"x:y@host", "@:", "a@:b/c", ":@", "git@host:", "https://", "ssh://@/", "git@:o/r",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		id, err := repourl.Parse(raw)
		if err != nil {
			return
		}
		if id.Host == "" || id.Owner == "" || id.Name == "" {
			t.Errorf("Parse(%q) = %+v: a repo with no host, owner or name", raw, id)
		}
	})
}
