package cmd

import (
	"errors"
	"fmt"
	"slices"

	"github.com/ashtonian/gitten/internal/gitops"
	"github.com/ashtonian/gitten/internal/state"
)

// Exit codes. kong adds 80 for usage errors; see the README for the table.
const (
	exitFailed   = 1  // the command failed
	exitProblems = 2  // a check ran and found problems: doctor, ws check
	exitPartial  = 3  // some targets failed, the others succeeded
	exitBusy     = 75 // another gitten run holds the lock (EX_TEMPFAIL): try again later
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
	if busy(err) {
		return exitBusy
	}
	return exitFailed
}

// busy reports whether err is another gitten command holding a lock that
// was needed: the same command later can succeed.
func busy(err error) bool {
	return errors.Is(err, state.ErrLocked) || errors.Is(err, gitops.ErrRepoInUse)
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

// outcomeOf is outcome for total targets, of which those that failed failed
// with errs. When every target failed only because another gitten command
// held a lock, the command is busy rather than failed: it can be run again
// later.
func outcomeOf(errs []error, total int, format string, args ...any) error {
	err := outcome(len(errs), total, format, args...)
	var coded *exitError
	if errors.As(err, &coded) && coded.code == exitFailed && !slices.ContainsFunc(errs, func(e error) bool { return !busy(e) }) {
		coded.code = exitBusy
	}
	return err
}
