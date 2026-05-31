package gitops

import "testing"

func TestShellQuote(t *testing.T) {
	cases := map[string]string{
		"/home/u/.ssh/id":   "'/home/u/.ssh/id'",
		"/home/u/my key/id": "'/home/u/my key/id'",
		"/it's/weird":       `'/it'\''s/weird'`,
	}
	for in, want := range cases {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %q, want %q", in, got, want)
		}
	}
}

// A key path with spaces must stay a single -i argument once the shell
// (which git uses to run GIT_SSH_COMMAND) re-parses the command.
func TestGitSSHCommandEnv_QuotesPath(t *testing.T) {
	got := gitSSHCommandEnv("/home/u/my key/id_rsa")
	want := "GIT_SSH_COMMAND=ssh -i '/home/u/my key/id_rsa' -o IdentitiesOnly=yes -o StrictHostKeyChecking=accept-new"
	if got != want {
		t.Errorf("gitSSHCommandEnv = %q, want %q", got, want)
	}
}
