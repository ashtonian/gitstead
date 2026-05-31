// Package state persists git-mgr's tracked repositories and workgroups to a
// YAML file. Writes are atomic (temp file + rename) and guarded by a flock so
// concurrent runs cannot corrupt it.
package state
