package discovery

import (
	"strings"
	"testing"

	"github.com/ashtonian/gitstead/internal/config"
)

func TestParseManualRepo(t *testing.T) {
	// SSH/scp form -> recorded as SSHURL, cloned over SSH.
	ssh, err := parseManualRepo(config.ManualRepo{URL: "git@github.com:owner/repo.git", Tags: []string{"x"}})
	if err != nil {
		t.Fatal(err)
	}
	if ssh.Host != "github.com" || ssh.FullName != "owner/repo" {
		t.Errorf("ssh identity wrong: %+v", ssh)
	}
	if ssh.SSHURL != "git@github.com:owner/repo.git" || ssh.HTTPURL != "" {
		t.Errorf("ssh url fields: ssh=%q http=%q", ssh.SSHURL, ssh.HTTPURL)
	}

	// HTTPS form -> recorded as HTTPURL and honored (was rejected before).
	https, err := parseManualRepo(config.ManualRepo{URL: "https://gitlab.com/group/sub/proj.git"})
	if err != nil {
		t.Fatal(err)
	}
	if https.Host != "gitlab.com" || https.FullName != "group/sub/proj" {
		t.Errorf("https identity wrong: %+v", https)
	}
	if https.HTTPURL != "https://gitlab.com/group/sub/proj.git" || https.SSHURL != "" {
		t.Errorf("https url fields: ssh=%q http=%q", https.SSHURL, https.HTTPURL)
	}
}

func TestMergeTags(t *testing.T) {
	got := mergeTags([]string{"a", "b"}, []string{"b", "c", ""})
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

// A repos: entry by key gets both conventional URLs, for clone_protocol to
// choose from; one by URL keeps that URL's transport alone.
func TestParseManualRepo_ByKey(t *testing.T) {
	r, err := parseManualRepo(config.ManualRepo{Repo: "github.com/acme/CamelCase", Tags: []string{"x"}})
	if err != nil {
		t.Fatal(err)
	}
	if r.Key() != "github.com/acme/CamelCase" || r.SSHURL != "git@github.com:acme/CamelCase.git" || r.HTTPURL != "https://github.com/acme/CamelCase.git" ||
		r.DiscoveredBy != ManualRule || len(r.Tags) != 1 {
		t.Errorf("by key: %+v", r)
	}
}

// An entry for a repo a rule found dedupes with it the same way whether it
// names the repo by URL or by key: one repo, the rule still its discoverer,
// both tag sets.
func TestAddManualRepos_DedupesWithDiscovered(t *testing.T) {
	found := Repo{
		Host: "github.com", Owner: "acme", Name: "api", FullName: "acme/api", ProviderID: "42",
		SSHURL: "git@github.com:acme/api.git", HTTPURL: "https://github.com/acme/api.git",
		Tags: []string{"work"}, DiscoveredBy: "acme",
	}
	tests := []struct {
		name      string
		entry     config.ManualRepo
		ssh, http string
	}{
		{"by url", config.ManualRepo{URL: "git@github.com:acme/api.git", Tags: []string{"pinned"}}, "git@github.com:acme/api.git", ""},
		{"by key", config.ManualRepo{Repo: "github.com/acme/api", Tags: []string{"pinned"}}, "git@github.com:acme/api.git", "https://github.com/acme/api.git"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			discovered := map[string]Repo{found.Key(): found}
			if err := addManualRepos([]config.ManualRepo{tt.entry}, "", discovered); err != nil {
				t.Fatal(err)
			}
			got := discovered["github.com/acme/api"]
			if len(discovered) != 1 || got.DiscoveredBy != "acme" || strings.Join(got.Tags, ",") != "work,pinned" ||
				got.SSHURL != tt.ssh || got.HTTPURL != tt.http {
				t.Errorf("discovered = %+v", discovered)
			}
		})
	}
}
