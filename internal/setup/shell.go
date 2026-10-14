package setup

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/ashtonian/git-mgr/internal/pathutil"
)

// ShellRC is the startup file of the user's shell, where setup adds the line
// that loads git-mgr's shell integration (`git-mgr shell-init`): `git-mgr cd`
// changing directory, and tab completion.
type ShellRC struct {
	Shell string // bash, zsh or fish
	Path  string
}

// The lines setup writes are kept between these markers, so running setup
// again replaces them rather than adding more.
const (
	shellBlockStart = "# >>> git-mgr shell integration (git-mgr setup) >>>"
	shellBlockEnd   = "# <<< git-mgr shell integration <<<"
)

// DetectShellRC finds the user's shell from $SHELL and its startup file:
// ~/.zshrc (under $ZDOTDIR when set), ~/.bashrc -- ~/.bash_profile on macOS,
// whose terminals start login shells -- or fish's config.fish. ok is false
// for any other shell.
func DetectShellRC(home, goos string, getenv func(string) string) (rc ShellRC, ok bool) {
	shell := filepath.Base(getenv("SHELL"))
	switch shell {
	case "zsh":
		dir := home
		if z := getenv("ZDOTDIR"); filepath.IsAbs(z) {
			dir = z
		}
		return ShellRC{Shell: shell, Path: filepath.Join(dir, ".zshrc")}, true
	case "bash":
		name := ".bashrc"
		if goos == "darwin" {
			name = ".bash_profile"
		}
		return ShellRC{Shell: shell, Path: filepath.Join(home, name)}, true
	case "fish":
		dir := filepath.Join(home, ".config")
		if x := getenv("XDG_CONFIG_HOME"); filepath.IsAbs(x) {
			dir = x
		}
		return ShellRC{Shell: shell, Path: filepath.Join(dir, "fish", "config.fish")}, true
	}
	return ShellRC{}, false
}

// Line is what the startup file runs: git-mgr's shell integration, when
// git-mgr is on PATH by then.
func (rc ShellRC) Line() string {
	if rc.Shell == "fish" {
		return "command -q git-mgr; and git-mgr shell-init fish | source"
	}
	return fmt.Sprintf(`command -v git-mgr >/dev/null 2>&1 && eval "$(git-mgr shell-init %s)"`, rc.Shell)
}

// Integrated reports whether the startup file loads git-mgr's shell
// integration already: setup's lines, or a shell-init line of your own.
func (rc ShellRC) Integrated() (bool, error) {
	data, err := os.ReadFile(rc.Path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return strings.Contains(string(data), "git-mgr shell-init"), nil
}

// Install adds the integration to the startup file, or replaces what setup
// added before, and creates the file if there is none. A startup file that
// is a symlink -- into a dotfiles repository, say -- is changed where it
// points, keeping its mode.
func (rc ShellRC) Install() error {
	path := rc.Path
	if target, err := filepath.EvalSymlinks(path); err == nil {
		path = target
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	mode := fs.FileMode(0o644)
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if info, err := os.Stat(path); err == nil {
			mode = info.Mode().Perm()
		}
	case errors.Is(err, fs.ErrNotExist):
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
	default:
		return err
	}
	block := shellBlockStart + "\n" + rc.Line() + "\n" + shellBlockEnd + "\n"
	return pathutil.WriteFileAtomic(path, []byte(withBlock(string(data), block)), mode)
}

// withBlock is text with block in place of an earlier one, or appended.
func withBlock(text, block string) string {
	start := strings.Index(text, shellBlockStart)
	if start >= 0 {
		if end := strings.Index(text[start:], shellBlockEnd); end >= 0 {
			end += start + len(shellBlockEnd)
			if end < len(text) && text[end] == '\n' {
				end++
			}
			return text[:start] + block + text[end:]
		}
	}
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	if text != "" {
		text += "\n"
	}
	return text + block
}
