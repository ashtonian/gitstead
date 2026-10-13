// Package testenv isolates a package's tests from the machine they run on:
// the user's git config, any repository the temporary directory sits inside,
// and the real home directory, where caches and lock files would otherwise
// land.
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
	home, err := os.MkdirTemp("", "git-mgr-test-home-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "testenv:", err)
		return 1
	}
	defer os.RemoveAll(home)

	// git stops looking for a repository at the temporary directory, so a
	// test's directory never turns out to be inside the checkout running it.
	ceilings := []string{os.TempDir()}
	if real, err := filepath.EvalSymlinks(os.TempDir()); err == nil && real != os.TempDir() {
		ceilings = append(ceilings, real)
	}
	for k, v := range map[string]string{
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
