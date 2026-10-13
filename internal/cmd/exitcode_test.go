package cmd

import (
	"errors"
	"fmt"
	"testing"

	"github.com/ashtonian/git-mgr/internal/state"
)

func TestExitCodes(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"success", nil, 0},
		{"nothing failed", outcome(0, 5, "repos failed"), 0},
		{"some failed", outcome(2, 5, "repos failed"), exitPartial},
		{"all failed", outcome(5, 5, "repos failed"), exitFailed},
		{"a plain error", errors.New("config invalid"), exitFailed},
		{"another run holds the lock", fmt.Errorf("loading: %w", state.ErrLocked), exitBusy},
		{"a wrapped outcome keeps its code", fmt.Errorf("sync: %w", outcome(1, 3, "x")), exitPartial},
		{"a check that found nothing", problems(0, "x"), 0},
		{"a check that found problems", problems(2, "2 problems"), exitProblems},
	}
	for _, tt := range tests {
		if got := ExitCode(tt.err); got != tt.want {
			t.Errorf("%s: ExitCode = %d, want %d", tt.name, got, tt.want)
		}
	}
	if err := outcome(2, 5, "repos failed"); err.Error() != "2 of 5 repos failed" {
		t.Errorf("message = %q", err)
	}
}
