// Package logfile bounds the plain-text logs gitstead keeps: log.file, which
// gitstead appends to itself, and a background sync's output, which launchd
// or cron appends to on gitstead's behalf.
package logfile

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// MaxBytes is how large a log grows before Rotate moves it aside. With the
// one older generation kept beside it, a log takes about twice this at most:
// years of a quiet 15-minute sync, or about two weeks of one failing every
// run on a few dozen repos.
const MaxBytes = 8 << 20

// Rotated is where Rotate moves path: its one older generation.
func Rotated(path string) string { return path + ".1" }

// Rotate moves path to Rotated(path), replacing the generation there, once
// path is larger than maxBytes, and reports whether it did. A missing path is
// nothing to rotate.
//
// A rename loses no line: a writer that has path open -- launchd's or cron's
// for the run in progress, another gitstead's log.file -- goes on appending
// to the rotated file, and the next open of path starts a new one. Rotations
// take turns under path.lock, so one that judged the size before another
// rotated can't move the new, small file over the older generation.
func Rotate(path string, maxBytes int64) (bool, error) {
	if !over(path, maxBytes) {
		return false, nil
	}
	unlock, err := tryLock(path + ".lock")
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return false, nil // another process is rotating it
	}
	if err != nil {
		return false, fmt.Errorf("rotating %s: %w", path, err)
	}
	defer unlock()
	if !over(path, maxBytes) {
		return false, nil
	}
	if err := os.Rename(path, Rotated(path)); err != nil {
		return false, fmt.Errorf("rotating %s: %w", path, err)
	}
	return true, nil
}

// over reports whether path is a regular file larger than maxBytes.
func over(path string, maxBytes int64) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Size() > maxBytes
}

// tryLock takes the exclusive lock on path without waiting; EWOULDBLOCK
// means another process holds it.
func tryLock(path string) (unlock func(), err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, err
	}
	return func() { f.Close() }, nil
}
