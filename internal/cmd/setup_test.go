package cmd

import (
	"testing"

	"github.com/alecthomas/kong"

	"github.com/ashtonian/gitstead/internal/setup"
)

// `gitstead init` is setup: the same questions, the same --yes.
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
		{setup.Track{Owner: "acme", Kind: "org", Names: []string{"billing", "payments"}, Skip: []string{"billing-old"}}, "acme, named billing or payments, skipping 1 by name"},
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

// Choosing an owner again keeps every rule it had, filters and all; a new
// owner gets one new rule.
func TestTracksFor(t *testing.T) {
	previous := []setup.Track{
		{Owner: "acme", Kind: "org", Rule: "work/acme", Names: []string{"billing"}},
		{Owner: "me", Kind: "self", Rule: "work"},
		{Owner: "acme", Kind: "org", Rule: "acme-ledger", Names: []string{"ledger"}, Skip: []string{"ledger-old"}},
	}
	got := tracksFor("ACME", "org", previous)
	if len(got) != 2 || got[0].Rule != "work/acme" || got[1].Rule != "acme-ledger" || got[1].Skip[0] != "ledger-old" {
		t.Errorf("tracksFor(acme) = %+v", got)
	}
	if got := tracksFor("acme-labs", "org", previous); len(got) != 1 || got[0].Rule != "" || got[0].Kind != "org" {
		t.Errorf("a new owner = %+v", got)
	}
}

// The preview counts what each name pattern matches, so one that matches
// far more than meant stands out.
func TestMatchCounts(t *testing.T) {
	names := []string{"billing-api", "billing-worker", "payments", "rebilling", "ops-tools"}
	if got := matchCounts([]string{"billing", "payments", "*bill*"}, names); got != "billing 2, payments 1, *bill* 3" {
		t.Errorf("matchCounts = %q", got)
	}
}
