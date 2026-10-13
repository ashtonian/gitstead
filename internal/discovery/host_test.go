package discovery

import (
	"testing"

	"github.com/ashtonian/git-mgr/internal/repourl"
)

// A repo's key is the same discovered as adopted from a clone of it, over
// SSH or HTTPS, whatever ports they use.
func TestHostFromURL_KeysMatchAdoptedClones(t *testing.T) {
	tests := []struct{ provider, clone string }{
		{"https://git.example.com:3000", "ssh://git@git.example.com:2222/acme/app.git"},
		{"https://git.example.com:3000/", "https://git.example.com:3000/acme/app.git"},
		{"http://localhost:32839", "http://localhost:32839/acme/app.git"},
		{"https://gitlab.example.com", "git@gitlab.example.com:acme/app.git"},
	}
	for _, tt := range tests {
		t.Run(tt.provider, func(t *testing.T) {
			id, err := repourl.Parse(tt.clone)
			if err != nil {
				t.Fatal(err)
			}
			if got := hostFromURL(tt.provider, "") + "/acme/app"; got != id.Key() {
				t.Errorf("discovered key %s, adopted key %s", got, id.Key())
			}
		})
	}
	if got := hostFromURL("", "github.com"); got != "github.com" {
		t.Errorf("no URL gives the fallback, got %s", got)
	}
}
