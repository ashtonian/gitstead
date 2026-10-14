package cmd

import (
	"testing"

	"github.com/alecthomas/kong"

	"github.com/ashtonian/gitten/internal/setup"
)

// `gitten init` is setup: the same questions, the same --yes.
func TestInitIsSetup(t *testing.T) {
	parser, err := kong.New(&CLI{}, kong.Vars{"version": "test"})
	if err != nil {
		t.Fatal(err)
	}
	kctx, err := parser.Parse([]string{"init", "--yes", "--dry-run"})
	if err != nil {
		t.Fatal(err)
	}
	if got := kctx.Selected().Name; got != "setup" {
		t.Errorf("init runs %q, want setup", got)
	}
}

func TestTrackLabel(t *testing.T) {
	for _, tt := range []struct {
		track setup.Track
		want  string
	}{
		{setup.Track{Owner: "me", Kind: "self"}, "me (your own repos)"},
		{setup.Track{Owner: "acme", Kind: "org", Names: []string{"*billing*", "*payments*"}}, "acme, named *billing* or *payments*"},
		{setup.Track{Owner: "acme", Kind: "org", Languages: []string{"Go"}, Topics: []string{"service", "api"}, NamePattern: "^svc-"},
			"acme, matching ^svc-, in Go, with topics service and api"},
	} {
		if got := trackLabel(tt.track); got != tt.want {
			t.Errorf("trackLabel = %q, want %q", got, tt.want)
		}
	}
}

func TestSample(t *testing.T) {
	names := []string{"a", "b", "c", "d"}
	if got := sample(names, 4); got != "a, b, c, d" {
		t.Errorf("sample = %q", got)
	}
	if got := sample(names, 2); got != "a, b, and 2 more" {
		t.Errorf("sample = %q", got)
	}
}

// The clone question counts the repos only when every account's were
// looked up.
func TestFoundTotal(t *testing.T) {
	accounts := []setup.Account{{Name: "work"}, {Name: "personal"}}
	if n, ok := foundTotal(accounts, map[string]int{"work": 12, "personal": 3}); !ok || n != 15 {
		t.Errorf("foundTotal = %d, %v", n, ok)
	}
	if _, ok := foundTotal(accounts, map[string]int{"work": 12}); ok {
		t.Error("an account not looked up makes the total unknown")
	}
}
