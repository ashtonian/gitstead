package gitops

import "testing"

// A key path with spaces must stay a single -i argument once the shell
// (which git uses to run GIT_SSH_COMMAND) re-parses the command.
func TestGitSSHCommandEnv_QuotesPath(t *testing.T) {
	got := gitSSHCommandEnv("/home/u/my key/id_rsa")
	want := "GIT_SSH_COMMAND=ssh -i '/home/u/my key/id_rsa' -o IdentitiesOnly=yes -o StrictHostKeyChecking=accept-new"
	if got != want {
		t.Errorf("gitSSHCommandEnv = %q, want %q", got, want)
	}
}
