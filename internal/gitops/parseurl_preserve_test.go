package gitops_test

import (
	"testing"

	"github.com/ashtonian/git-mgr/internal/gitops"
)

// TestParseURL_PreservesOriginal verifies ParseURL keeps the exact input URL
// (scheme, port, host form) so callers store the real remote rather than a
// regenerated canonical git@ form -- which would drop ssh:// ports and flip
// https repos to ssh.
func TestParseURL_PreservesOriginal(t *testing.T) {
	for _, raw := range []string{
		"ssh://git@ghes.company.com:2222/team/svc.git", // port must survive
		"https://user@git.example.com:8443/team/app.git",
		"git@github.com:owner/repo.git",
		"/srv/git/origin.git",
	} {
		id, err := gitops.ParseURL(raw)
		if err != nil {
			t.Fatalf("ParseURL(%q): %v", raw, err)
		}
		if id.URL != raw {
			t.Errorf("URL not preserved: got %q, want %q", id.URL, raw)
		}
	}
}
