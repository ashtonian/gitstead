// Package procutil runs commands that must neither prompt nor outlive the
// context they were started with.
package procutil

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// Detach makes cmd, made with exec.CommandContext, run in a session of its
// own, with no controlling terminal to prompt on -- so ssh fails instead of
// asking for a passphrase -- and makes its context ending kill the whole
// session, whatever it started included. Wait then gives their output at
// most grace to close.
func Detach(cmd *exec.Cmd, grace time.Duration) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = grace
}
