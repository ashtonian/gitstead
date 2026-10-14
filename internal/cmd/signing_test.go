package cmd

import (
	"strings"
	"testing"

	"github.com/ashtonian/gitstead/internal/config"
	"github.com/ashtonian/gitstead/internal/state"
)

// An agent isn't launched on copies of an account that requires signing when
// a test signature fails: it would stall on its first commit.
func TestCheckSigning(t *testing.T) {
	cfg := &config.Config{Accounts: []config.Account{
		{Name: "work", Hosts: []string{"github.com"}, Owners: []string{"acme"},
			Signing: config.Signing{Mode: "required", Format: "ssh", Key: "/nonexistent/id_signing.pub"}},
		{Name: "me", Hosts: []string{"github.com"}, Owners: []string{"me"}},
	}}
	st := &state.State{Repos: map[string]*state.RepoState{"github.com/acme/api": {}, "github.com/me/notes": {}}}
	ctx := &Context{}

	err := checkSigning(ctx, cfg, st, []state.CopyState{{Repo: "github.com/acme/api"}})
	if err == nil || !strings.Contains(err.Error(), `account "work" requires signing`) {
		t.Errorf("a copy of the work account: %v", err)
	}
	if err := checkSigning(ctx, cfg, st, []state.CopyState{{Repo: "github.com/me/notes"}}); err != nil {
		t.Errorf("an account that doesn't require signing: %v", err)
	}
}
