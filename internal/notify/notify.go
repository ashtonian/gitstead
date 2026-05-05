package notify

import (
	"fmt"
	"os/exec"
	"runtime"
)

// Send sends a native OS notification. Silent on unsupported platforms.
func Send(title, msg string) {
	switch runtime.GOOS {
	case "darwin":
		exec.Command("osascript", "-e",
			fmt.Sprintf(`display notification %q with title %q`, msg, title)).Run()
	case "linux":
		exec.Command("notify-send", title, msg).Run()
	}
}
