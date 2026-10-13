package activity

import (
	"context"
	"time"

	agents "github.com/ashtonian/git-mgr/internal/agent"
)

// Watch watches one agent's terminal and says when the agent is waiting for
// you: it printed steadily for at least Busy -- it was working -- and then
// went quiet for Quiet. An agent that has just started prints a screen and
// stops; that isn't waiting for an answer, so it doesn't count.
type Watch struct {
	Tracker *Tracker
	Holder  agents.Holder
	// Every is how often to look.
	Every time.Duration
	Quiet time.Duration
	Busy  time.Duration
	// Waiting is called when the agent goes quiet after working, with how
	// long it has been quiet; Working when it prints again after that.
	Waiting func(quiet time.Duration)
	Working func()

	busySince time.Time
	worked    bool
	waiting   bool
}

// Run watches until ctx ends.
func (w *Watch) Run(ctx context.Context) {
	tick := time.NewTicker(w.Every)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		a, ok := w.Tracker.Read(ctx, []agents.Holder{w.Holder})[Key(w.Holder)]
		if ok && a.Text != "" {
			w.step(a, w.Tracker.now())
		}
	}
}

// step takes one look's activity a at now.
func (w *Watch) step(a Activity, now time.Time) {
	// Printed since the last look, give or take the second tmux rounds to.
	if a.Idle <= w.Every+time.Second {
		if w.busySince.IsZero() {
			w.busySince = now
		}
		if now.Sub(w.busySince) >= w.Busy {
			w.worked = true
		}
		if w.waiting {
			w.waiting = false
			w.Working()
		}
		return
	}
	w.busySince = time.Time{}
	if w.worked && !w.waiting && a.Idle >= w.Quiet {
		w.waiting, w.worked = true, false
		w.Waiting(a.Idle)
	}
}
