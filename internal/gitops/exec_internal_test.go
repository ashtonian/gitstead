package gitops

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A key path with spaces must stay a single -i argument once the shell
// (which git uses to run GIT_SSH_COMMAND) re-parses the command.
func TestGitSSHCommandEnv_QuotesPath(t *testing.T) {
	got := gitSSHCommandEnv("/home/u/my key/id_rsa")
	want := "GIT_SSH_COMMAND=ssh -i '/home/u/my key/id_rsa' -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=accept-new"
	if got != want {
		t.Errorf("gitSSHCommandEnv = %q, want %q", got, want)
	}
}

// A remote that asks for a password fails at once instead of prompting, even
// when the test runs in a terminal git could open.
func TestGit_NeverPrompts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	_, err := Git(ctx, t.TempDir(), "-c", "credential.helper=", "ls-remote", srv.URL+"/repo.git")
	if err == nil || !strings.Contains(err.Error(), "terminal prompts disabled") {
		t.Fatalf("ls-remote of a password-protected remote = %v, want it to fail without prompting", err)
	}
}

// Cancelling a git command also kills what it started, so a helper holding
// the output pipes open can't keep the caller waiting.
func TestGit_CancelKillsHelpers(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := Git(ctx, t.TempDir(), "-c", "alias.hang=!sleep 600 | cat", "hang")
		done <- err
	}()
	time.Sleep(200 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a cancelled command reported success")
		}
	case <-time.After(killGrace - time.Second):
		t.Fatal("git was cancelled but its children kept the call waiting")
	}
}
