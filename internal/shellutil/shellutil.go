// Package shellutil provides helpers for safely building shell command strings.
package shellutil

import "strings"

// Quote wraps s in single quotes for safe use as a single shell word, escaping
// any embedded single quotes. Used when a path or argument is interpolated into
// a command that a shell will later re-parse (e.g. GIT_SSH_COMMAND, tmux
// send-keys, generated scripts).
func Quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Word is s as one shell word: as it is when it holds only characters no
// shell treats specially, quoted otherwise. It keeps command lines people
// read readable. A leading '=' is quoted: zsh expands =name to a command's
// path.
func Word(s string) string {
	if s == "" || s[0] == '=' {
		return Quote(s)
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("_./=:@%+,-", r)) {
			return Quote(s)
		}
	}
	return s
}
