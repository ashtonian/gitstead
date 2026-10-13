package activity

import (
	"context"
	"fmt"
	"testing"
	"time"

	agents "github.com/ashtonian/git-mgr/internal/agent"
)

func TestParseMuxyActivity(t *testing.T) {
	out := ParseMuxyActivity([]byte("123\tp1\tclaude\twaiting\te9\tneeds input\n456\tp1\tcodex\tworking\t\t\n\n"))
	if out["123"] != "waiting: needs input" || out["456"] != "working" {
		t.Errorf("activity = %v", out)
	}
}

func TestAgo(t *testing.T) {
	for d, want := range map[time.Duration]string{12 * time.Second: "12s ago", 4 * time.Minute: "4m ago", 3 * time.Hour: "3h ago", 72 * time.Hour: "3d ago"} {
		if got := Ago(d); got != want {
			t.Errorf("Ago(%s) = %s", d, got)
		}
	}
}

// An agent alone in its tmux window gets the window's last output time; one
// sharing a window gets the time its screen was last seen to change.
func TestTracker(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	screens := map[string]string{"%2": "a"}
	tr := NewTracker()
	tr.now = func() time.Time { return now }
	tr.tmux = func(_ context.Context, args ...string) ([]byte, error) {
		if args[2] == "list-panes" {
			return []byte(fmt.Sprintf("%%1\t%d\t1\n%%2\t%d\t2\n%%9\t1\t1\n", now.Unix()-90, now.Unix())), nil
		}
		return []byte(screens[args[len(args)-1]]), nil
	}
	hs := []agents.Holder{{Launcher: "tmux", Socket: "/s", Session: "%1"}, {Launcher: "tmux", Socket: "/s", Session: "%2"}}
	key := func(pane string) string {
		return Key(agents.Holder{Launcher: "tmux", Socket: "/s", Session: pane})
	}

	got := tr.Read(context.Background(), hs)
	if got[key("%1")].Text != "printed 1m ago" || got[key("%2")].Text != "" || len(got) != 2 {
		t.Fatalf("first look = %v", got)
	}
	now = now.Add(30 * time.Second)
	if got := tr.Read(context.Background(), hs); got[key("%2")].Text != "quiet ≥ 30s" {
		t.Errorf("an unchanged screen = %v", got[key("%2")])
	}
	screens["%2"] = "b"
	now = now.Add(10 * time.Second)
	tr.Read(context.Background(), hs)
	now = now.Add(20 * time.Second)
	if got := tr.Read(context.Background(), hs); got[key("%2")].Text != "printed 20s ago" || got[key("%2")].Idle != 20*time.Second {
		t.Errorf("after a change = %v", got[key("%2")])
	}
}

// Working, then quiet, is waiting; the screen an agent prints as it starts
// isn't; printing again is working again.
func TestWatch(t *testing.T) {
	var events []string
	w := &Watch{Every: 2 * time.Second, Quiet: 30 * time.Second, Busy: 5 * time.Second,
		Waiting: func(q time.Duration) { events = append(events, "waiting "+q.String()) },
		Working: func() { events = append(events, "working") }}
	start := time.Unix(1_000_000, 0)
	look := func(at, idle time.Duration) { w.step(Activity{Text: "x", Idle: idle}, start.Add(at)) }

	look(0, 0) // its first screen
	look(2*time.Second, 2*time.Second)
	look(40*time.Second, 40*time.Second)
	if len(events) != 0 {
		t.Fatalf("an agent that only started isn't waiting: %v", events)
	}
	for at := 50 * time.Second; at <= 60*time.Second; at += 2 * time.Second {
		look(at, time.Second) // working
	}
	look(80*time.Second, 20*time.Second)
	look(92*time.Second, 32*time.Second)
	look(94*time.Second, 34*time.Second)
	look(96*time.Second, 0)
	if fmt.Sprint(events) != "[waiting 32s working]" {
		t.Errorf("events = %v", events)
	}
}
