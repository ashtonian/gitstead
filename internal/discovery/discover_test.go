package discovery

import "testing"

func TestParseGitURL(t *testing.T) {
	cases := []struct {
		url, host, owner, name string
	}{
		{"git@github.com:owner/repo.git", "github.com", "owner", "repo"},
		{"git@github.com:owner/repo", "github.com", "owner", "repo"},
		{"ssh://git@ghes.example.com:2222/team/svc.git", "ghes.example.com", "team", "svc"},
		{"ssh://git@gitlab.com/group/subgroup/proj.git", "gitlab.com", "group/subgroup", "proj"},
	}
	for _, c := range cases {
		p := parseGitURL(c.url)
		if p == nil {
			t.Errorf("%s: got nil", c.url)
			continue
		}
		if p.host != c.host || p.owner != c.owner || p.name != c.name {
			t.Errorf("%s: got %s/%s/%s, want %s/%s/%s",
				c.url, p.host, p.owner, p.name, c.host, c.owner, c.name)
		}
	}
	for _, bad := range []string{"https://github.com/owner/repo.git", "not-a-url", "git@host:noslash"} {
		if p := parseGitURL(bad); p != nil {
			t.Errorf("%s: expected nil, got %+v", bad, p)
		}
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
