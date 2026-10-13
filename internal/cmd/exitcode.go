package cmd

import (
	"errors"
	"fmt"

	"github.com/ashtonian/git-mgr/internal/gitops"
	"github.com/ashtonian/git-mgr/internal/state"
)

// Exit codes. kong adds 80 for usage errors; see the README for the table.
const (
	exitFailed   = 1  // the command failed
	exitProblems = 2  // a check ran and found problems: doctor, ws check
	exitPartial  = 3  // some targets failed, the others succeeded
	exitBusy     = 75 // another git-mgr run holds the lock (EX_TEMPFAIL): try again later
)

// problems reports a check that ran and found n problems.
func problems(n int, format string, args ...any) error {
	if n == 0 {
		return nil
	}
	return &exitError{exitProblems, fmt.Errorf(format, args...)}
}

// exitError gives an error the exit code main uses for it.
type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string { return e.err.Error() }
func (e *exitError) Unwrap() error { return e.err }
func (e *exitError) ExitCode() int { return e.code }

// ExitCode is the process exit code for an error a command returned.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	var coded interface{ ExitCode() int }
	if errors.As(err, &coded) {
		return coded.ExitCode()
	}
	if errors.Is(err, state.ErrLocked) || errors.Is(err, gitops.ErrRepoInUse) {
		return exitBusy
	}
	return exitFailed
}

// outcome reports a command over total targets of which failed failed: nil
// when none did, a partial failure when some succeeded, a failure when none
// did.
func outcome(failed, total int, format string, args ...any) error {
	if failed == 0 {
		return nil
	}
	err := fmt.Errorf("%d of %d %s", failed, total, fmt.Sprintf(format, args...))
	if failed < total {
		return &exitError{exitPartial, err}
	}
	return &exitError{exitFailed, err}
}
