package gitops

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// copyIDFile holds a working copy's ID inside its private git directory:
// .git for a clone, .git/worktrees/<name> for a linked worktree.
const copyIDFile = "gitten-copy-id"

// CopyID returns the ID of the working copy at repoPath, creating one on first
// use. Clones and linked worktrees of one repository each get their own, so
// their backups never mix, and the ID moves with the copy because it lives in
// the copy's git directory.
func CopyID(ctx context.Context, repoPath string) (string, error) {
	path := filepath.Join(ResolveGitDirs(ctx, repoPath).GitDir, copyIDFile)
	if id, err := readCopyID(path); err == nil || !errors.Is(err, fs.ErrNotExist) {
		return id, err
	}

	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generating copy ID: %w", err)
	}
	id := hex.EncodeToString(buf)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return readCopyID(path) // another gitten process won the race
	}
	if err != nil {
		return "", fmt.Errorf("creating copy ID: %w", err)
	}
	if _, err := f.WriteString(id + "\n"); err != nil {
		f.Close()
		os.Remove(path)
		return "", fmt.Errorf("writing copy ID: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		return "", fmt.Errorf("writing copy ID: %w", err)
	}
	return id, nil
}

// ReadCopyID returns the copy's ID without creating one; "" if it has none.
func ReadCopyID(ctx context.Context, repoPath string) string {
	id, _ := readCopyID(filepath.Join(ResolveGitDirs(ctx, repoPath).GitDir, copyIDFile))
	return id
}

func readCopyID(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(string(data))
	if !validCopyID(id) {
		return "", fmt.Errorf("corrupt copy ID in %s", path)
	}
	return id, nil
}

func validCopyID(id string) bool {
	if len(id) != 16 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}
