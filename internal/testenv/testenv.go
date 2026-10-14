// Package testenv isolates a package's tests from the machine they run on:
// the user's git config, any repository the temporary directory sits inside,
// and the real home directory, where caches and lock files would otherwise
// land. Temporary directories and the home directory tests get are made
// under one directory, deleted once they have run.
package testenv

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Main isolates the environment, runs the tests and returns their exit code;
// call it from TestMain as os.Exit(testenv.Main(m)).
func Main(m *testing.M) int {
	root, err := os.MkdirTemp("", "gitten-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "testenv:", err)
		return 1
	}
	defer os.RemoveAll(root)
	home, tmp := filepath.Join(root, "home"), filepath.Join(root, "tmp")
	for _, dir := range []string{home, tmp} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			fmt.Fprintln(os.Stderr, "testenv:", err)
			return 1
		}
	}

	// Every test's temporary directory is made under tmp, and git stops
	// looking for a repository there, so a test's directory never turns out
	// to be inside a repository -- the checkout running it, or one the
	// system's temporary directory happens to be in.
	ceilings := []string{tmp}
	if real, err := filepath.EvalSymlinks(tmp); err == nil && real != tmp {
		ceilings = append(ceilings, real)
	}
	for k, v := range map[string]string{
		"TMPDIR":                  tmp,
		"GIT_CONFIG_GLOBAL":       os.DevNull,
		"GIT_CONFIG_NOSYSTEM":     "1",
		"GIT_CEILING_DIRECTORIES": strings.Join(ceilings, string(os.PathListSeparator)),
		"GIT_AUTHOR_NAME":         "t",
		"GIT_AUTHOR_EMAIL":        "t@t",
		"GIT_COMMITTER_NAME":      "t",
		"GIT_COMMITTER_EMAIL":     "t@t",
		"HOME":                    home,
		"XDG_CACHE_HOME":          filepath.Join(home, ".cache"),
		"XDG_CONFIG_HOME":         filepath.Join(home, ".config"),
		"XDG_DATA_HOME":           filepath.Join(home, ".local", "share"),
	} {
		if err := os.Setenv(k, v); err != nil {
			fmt.Fprintln(os.Stderr, "testenv:", err)
			return 1
		}
	}
	return m.Run()
}
