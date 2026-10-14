package shellutil_test

import (
	"testing"

	"github.com/ashtonian/gitstead/internal/shellutil"
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

func TestWord(t *testing.T) {
	for in, want := range map[string]string{
		"gitstead": "gitstead", "/a/b.yaml": "/a/b.yaml", "--profile": "--profile", "me@host": "me@host",
		"": "''", "=session": "'=session'", "a b": "'a b'", "it's": `'it'\''s'`, "$HOME": "'$HOME'", "a;b": "'a;b'", "~/x": "'~/x'",
	} {
		if got := shellutil.Word(in); got != want {
			t.Errorf("Word(%q) = %s, want %s", in, got, want)
		}
	}
}
