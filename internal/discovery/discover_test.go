package discovery

import (
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
