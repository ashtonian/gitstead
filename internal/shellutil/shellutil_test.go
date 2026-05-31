package shellutil_test

import (
	"testing"

	"github.com/ashtonian/git-mgr/internal/shellutil"
)

func TestQuote(t *testing.T) {
	cases := map[string]string{
		"/home/u/.ssh/id":   "'/home/u/.ssh/id'",
		"/home/u/my key/id": "'/home/u/my key/id'",
		"/it's/weird":       `'/it'\''s/weird'`,
	}
	for in, want := range cases {
		if got := shellutil.Quote(in); got != want {
			t.Errorf("Quote(%q) = %q, want %q", in, got, want)
		}
	}
}
