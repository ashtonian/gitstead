// Package state persists gitstead's tracked repositories and workspaces to a
// YAML file. Writes are atomic (temp file + rename) and guarded by a flock so
// concurrent runs cannot corrupt it.
package state
