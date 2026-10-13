// Package notify sends desktop notifications.
package notify

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"time"
)

// Send sends a native OS notification, waiting at most timeout (0: no
// limit) for the notifier, which can hang without a desktop session. It is
// best effort: failures are ignored, and other platforms get nothing.
func Send(ctx context.Context, timeout time.Duration, title, msg string) {
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	switch runtime.GOOS {
	case "darwin":
		_ = exec.CommandContext(ctx, "osascript", "-e",
			fmt.Sprintf(`display notification %q with title %q`, msg, title)).Run()
	case "linux":
		_ = exec.CommandContext(ctx, "notify-send", title, msg).Run()
	}
}
