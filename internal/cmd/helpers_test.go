package cmd

import (
	"testing"

	"github.com/ashtonian/git-mgr/internal/discovery"
)

func TestCloneTarget(t *testing.T) {
	repo := discovery.Repo{SSHURL: "git@h:o/r.git", HTTPURL: "https://h/o/r.git"}

	if url, key := cloneTarget(repo, "ssh", "/k"); url != "git@h:o/r.git" || key != "/k" {
		t.Errorf("ssh: got (%q,%q), want (git@h:o/r.git, /k)", url, key)
	}
	// https uses the HTTP URL and drops the SSH key (auth via credential helper).
	if url, key := cloneTarget(repo, "https", "/k"); url != "https://h/o/r.git" || key != "" {
		t.Errorf("https: got (%q,%q), want (https://h/o/r.git, \"\")", url, key)
	}
	// https with no HTTP URL falls back to SSH.
	noHTTP := discovery.Repo{SSHURL: "git@h:o/r.git"}
	if url, key := cloneTarget(noHTTP, "https", "/k"); url != "git@h:o/r.git" || key != "/k" {
		t.Errorf("https fallback: got (%q,%q), want ssh", url, key)
	}
}

func TestIsHTTPURL(t *testing.T) {
	for _, u := range []string{"https://github.com/x/y.git", "http://h/x/y"} {
		if !isHTTPURL(u) {
			t.Errorf("isHTTPURL(%q) = false, want true", u)
		}
	}
	for _, u := range []string{"git@github.com:x/y.git", "ssh://git@h/x/y", "/local/path", "~/r"} {
		if isHTTPURL(u) {
			t.Errorf("isHTTPURL(%q) = true, want false", u)
		}
	}
}
