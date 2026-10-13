package gitops_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ashtonian/git-mgr/internal/gitops"
)

// shaped is a remote whose repo has files at the top and in a/ and b/, a
// submodule in each of a/ and b/, and, with git-lfs installed, a Git LFS
// file in a/.
type shaped struct {
	url     string // the remote, over file://
	root    string
	lfs     bool
	bBlob   string // b/b.txt's object
	content []byte // a/big.bin's content
}

// shapedRemote builds it. Submodules over file:// need protocol.file.allow,
// and LFS its filters, both given through the environment.
func shapedRemote(t *testing.T) shaped {
	t.Helper()
	cfg := map[string]string{"protocol.file.allow": "always"}
	_, lfsErr := exec.LookPath("git-lfs")
	f := shaped{root: t.TempDir(), lfs: lfsErr == nil}
	if f.lfs {
		cfg["filter.lfs.clean"] = "git-lfs clean -- %f"
		cfg["filter.lfs.smudge"] = "git-lfs smudge -- %f"
		cfg["filter.lfs.process"] = "git-lfs filter-process"
		cfg["filter.lfs.required"] = "true"
	}
	i := 0
	for k, v := range cfg {
		t.Setenv("GIT_CONFIG_KEY_"+strconv.Itoa(i), k)
		t.Setenv("GIT_CONFIG_VALUE_"+strconv.Itoa(i), v)
		i++
	}
	t.Setenv("GIT_CONFIG_COUNT", strconv.Itoa(i))

	bare := func(name string, files map[string]string) string {
		work := filepath.Join(f.root, "work-"+name)
		mustGit(t, f.root, "init", "-q", "-b", "main", work)
		for p, content := range files {
			writeFile(t, filepath.Join(work, p), []byte(content))
		}
		mustGit(t, work, "add", ".")
		mustGit(t, work, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", name)
		remote := filepath.Join(f.root, name+".git")
		mustGit(t, f.root, "clone", "-q", "--bare", work, remote)
		return "file://" + remote
	}
	subA := bare("sub-a", map[string]string{"s.txt": "a's submodule\n"})
	subB := bare("sub-b", map[string]string{"s.txt": "b's submodule\n"})

	work := filepath.Join(f.root, "work")
	mustGit(t, f.root, "init", "-q", "-b", "main", work)
	for _, d := range []string{"a", "b"} {
		if err := os.MkdirAll(filepath.Join(work, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(work, "root.txt"), []byte("top\n"))
	writeFile(t, filepath.Join(work, "a", "a.txt"), []byte("in a\n"))
	writeFile(t, filepath.Join(work, "b", "b.txt"), []byte("in b\n"))
	mustGit(t, work, "submodule", "add", "-q", subA, "a/mod")
	mustGit(t, work, "submodule", "add", "-q", subB, "b/mod")
	if f.lfs {
		f.content = []byte(strings.Repeat("large file\n", 100))
		writeFile(t, filepath.Join(work, ".gitattributes"), []byte("*.bin filter=lfs diff=lfs merge=lfs -text\n"))
		writeFile(t, filepath.Join(work, "a", "big.bin"), f.content)
	}
	mustGit(t, work, "add", ".")
	mustGit(t, work, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "init")
	f.bBlob = mustGit(t, work, "rev-parse", "HEAD:b/b.txt")

	remote := filepath.Join(f.root, "remote.git")
	mustGit(t, f.root, "init", "-q", "--bare", "-b", "main", remote)
	mustGit(t, remote, "config", "uploadpack.allowFilter", "true")
	f.url = "file://" + remote
	mustGit(t, work, "push", "-q", f.url, "main")
	if f.lfs {
		mustGit(t, work, "lfs", "push", "--all", f.url)
	}
	return f
}

func exists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Lstat(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return err == nil
}

// checkSparse checks that the checkout at dir holds the top and a/ and
// nothing of b/, and that git sees it clean.
func checkSparse(t *testing.T, dir string) {
	t.Helper()
	for p, want := range map[string]bool{"root.txt": true, "a/a.txt": true, "b/b.txt": false, "b": false} {
		if got := exists(t, filepath.Join(dir, p)); got != want {
			t.Errorf("%s: exists = %v, want %v", p, got, want)
		}
	}
	if st := mustGit(t, dir, "status", "--porcelain"); st != "" {
		t.Errorf("a fresh sparse checkout should be clean:\n%s", st)
	}
}

// A sparse partial clone downloads nothing outside its cone.
func TestClone_Sparse(t *testing.T) {
	f := shapedRemote(t)
	dest := filepath.Join(t.TempDir(), "clone")
	err := gitops.Clone(context.Background(), f.url, dest, gitops.CloneOptions{Filter: "blob:none", Shape: gitops.Shape{Sparse: []string{"a"}}})
	if err != nil {
		t.Fatal(err)
	}
	checkSparse(t, dest)
	missing := mustGit(t, dest, "rev-list", "--objects", "--missing=print", "--all")
	if !strings.Contains(missing, "?"+f.bBlob) {
		t.Errorf("b/b.txt's blob was downloaded; missing objects:\n%s", missing)
	}
}

func TestClone_Submodules(t *testing.T) {
	f := shapedRemote(t)
	dest := filepath.Join(t.TempDir(), "clone")
	if err := gitops.Clone(context.Background(), f.url, dest, gitops.CloneOptions{Shape: gitops.Shape{Submodules: true}}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"a/mod/s.txt", "b/mod/s.txt"} {
		if !exists(t, filepath.Join(dest, p)) {
			t.Errorf("submodule file %s missing", p)
		}
	}
}

// Only the submodules in the cone are cloned; git's own update would clone
// them all.
func TestClone_SparseSubmodules(t *testing.T) {
	f := shapedRemote(t)
	dest := filepath.Join(t.TempDir(), "clone")
	opts := gitops.CloneOptions{Depth: 1, Shape: gitops.Shape{Sparse: []string{"a"}, Submodules: true}}
	if err := gitops.Clone(context.Background(), f.url, dest, opts); err != nil {
		t.Fatal(err)
	}
	checkSparse(t, dest)
	if !exists(t, filepath.Join(dest, "a", "mod", "s.txt")) {
		t.Error("a/mod, in the cone, wasn't checked out")
	}
	if exists(t, filepath.Join(dest, ".git", "modules", "b", "mod")) {
		t.Error("b/mod, outside the cone, was cloned")
	}
	if got := mustGit(t, filepath.Join(dest, "a", "mod"), "rev-parse", "--is-shallow-repository"); got != "true" {
		t.Errorf("a/mod should be cloned with depth 1, shallow = %s", got)
	}
}

// A submodule that can't be cloned leaves the clone in place, with a
// SubmoduleError saying so.
func TestClone_SubmoduleFailureKeepsTheClone(t *testing.T) {
	f := shapedRemote(t)
	if err := os.RemoveAll(filepath.Join(f.root, "sub-b.git")); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "clone")
	err := gitops.Clone(context.Background(), f.url, dest, gitops.CloneOptions{Shape: gitops.Shape{Submodules: true}})
	var sub *gitops.SubmoduleError
	if !errors.As(err, &sub) {
		t.Fatalf("Clone = %v, want a SubmoduleError", err)
	}
	if !exists(t, filepath.Join(dest, "root.txt")) {
		t.Error("the clone should stay")
	}
}

func lfsFile(t *testing.T, f shaped, path string) (pointer bool) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	switch {
	case strings.HasPrefix(string(data), "version https://git-lfs.github.com/spec/v1"):
		return true
	case string(data) == string(f.content):
		return false
	}
	t.Fatalf("%s is neither the LFS content nor its pointer: %q", path, data)
	return false
}

func TestClone_LFS(t *testing.T) {
	f := shapedRemote(t)
	if !f.lfs {
		t.Skip("git-lfs is not installed")
	}
	ctx := context.Background()
	full := filepath.Join(t.TempDir(), "full")
	if err := gitops.Clone(ctx, f.url, full, gitops.CloneOptions{}); err != nil {
		t.Fatal(err)
	}
	if lfsFile(t, f, filepath.Join(full, "a", "big.bin")) {
		t.Fatal("without skip, the LFS content should be downloaded")
	}

	skipped := filepath.Join(t.TempDir(), "skipped")
	if err := gitops.Clone(ctx, f.url, skipped, gitops.CloneOptions{Shape: gitops.Shape{SkipLFS: true}}); err != nil {
		t.Fatal(err)
	}
	if !lfsFile(t, f, filepath.Join(skipped, "a", "big.bin")) {
		t.Error("with skip, the LFS file should stay a pointer")
	}
	if got := mustGit(t, skipped, "config", gitops.LFSFetchExclude); got != "*" {
		t.Errorf("%s = %q, want *", gitops.LFSFetchExclude, got)
	}
	// Later checkouts skip it too.
	wt := filepath.Join(t.TempDir(), "wt")
	mustGit(t, skipped, "worktree", "add", "-q", "-b", "later", wt)
	if !lfsFile(t, f, filepath.Join(wt, "a", "big.bin")) {
		t.Error("a later checkout downloaded the LFS content")
	}
}

// A worktree takes its own shape, whatever its repo's checkout is.
func TestAddWorktree_Shape(t *testing.T) {
	f := shapedRemote(t)
	ctx := context.Background()
	source := filepath.Join(t.TempDir(), "source")
	if err := gitops.Clone(ctx, f.url, source, gitops.CloneOptions{}); err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(t.TempDir(), "agent-1")
	opts := gitops.CloneOptions{Shape: gitops.Shape{Sparse: []string{"a"}, Submodules: true}}
	if err := gitops.AddWorktree(ctx, source, wt, "agents/ws/agent-1", "", opts); err != nil {
		t.Fatal(err)
	}
	checkSparse(t, wt)
	if !exists(t, filepath.Join(wt, "a", "mod", "s.txt")) {
		t.Error("a/mod wasn't checked out in the worktree")
	}
	if !exists(t, filepath.Join(source, "b", "b.txt")) || mustGitOK(source, "config", "core.sparseCheckout") == "true" {
		t.Error("the repo's own checkout must stay whole")
	}
}

// A clone made from the repo's clone takes its shape, and its LFS content
// from the source, downloading none.
func TestCloneFromSource_Shape(t *testing.T) {
	f := shapedRemote(t)
	ctx := context.Background()
	source := filepath.Join(t.TempDir(), "source")
	if err := gitops.Clone(ctx, f.url, source, gitops.CloneOptions{}); err != nil {
		t.Fatal(err)
	}
	if f.lfs {
		// Nothing left upstream to download from.
		if err := os.RemoveAll(filepath.Join(f.root, "remote.git", "lfs")); err != nil {
			t.Fatal(err)
		}
	}
	dest := filepath.Join(t.TempDir(), "agent-1")
	opts := gitops.CloneOptions{Shape: gitops.Shape{Sparse: []string{"a"}, Submodules: true}}
	if err := gitops.CloneFromSource(ctx, source, dest, f.url, "agents/ws/agent-1", "", false, opts); err != nil {
		t.Fatal(err)
	}
	checkSparse(t, dest)
	if !exists(t, filepath.Join(dest, "a", "mod", "s.txt")) {
		t.Error("a/mod wasn't checked out in the clone")
	}
	if f.lfs && lfsFile(t, f, filepath.Join(dest, "a", "big.bin")) {
		t.Error("the LFS content should come from the source")
	}
}

// mustGitOK runs git, returning "" on failure: for a config key that may be
// unset.
func mustGitOK(dir string, args ...string) string {
	out, _ := gitops.Git(context.Background(), dir, args...)
	return out
}
