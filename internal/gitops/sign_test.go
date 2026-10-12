package gitops_test

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/ashtonian/git-mgr/internal/gitops"
)

func TestTestSign(t *testing.T) {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen not installed")
	}
	key := filepath.Join(t.TempDir(), "id_sign")
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", key).CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen: %v\n%s", err, out)
	}
	ctx := context.Background()

	if err := gitops.TestSign(ctx, map[string]string{"gpg.format": "ssh", "user.signingkey": key}); err != nil {
		t.Fatalf("a passphrase-less key should sign unattended: %v", err)
	}
	if err := gitops.TestSign(ctx, map[string]string{"gpg.format": "ssh", "user.signingkey": key + ".missing"}); err == nil {
		t.Fatal("a missing key must fail the test signature")
	}
}
