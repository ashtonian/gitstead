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
