// Package activity reads what agents' terminals show: when a tmux pane last
// printed, or the state Muxy reports for an agent. `gitten top` shows it,
// and `ws run` watches its own pane with it to tell when its agent is
// waiting for you.
package activity

import (
	"context"
	"crypto/sha256"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	agents "github.com/ashtonian/gitten/internal/agent"
)

// Activity is what an agent's terminal shows.
type Activity struct {
	Text string
	Idle time.Duration // how long since it printed; 0 when unknown
}

// Key identifies a run's terminal across launchers.
func Key(h agents.Holder) string {
	return h.Launcher + "\x00" + h.Socket + "\x00" + h.Session
}

// activityTimeout bounds each tmux or Muxy query.
const activityTimeout = 5 * time.Second

// Tracker reads agents' terminal activity, remembering what it saw between
// looks. tmux keeps the time of a window's last output, not a pane's, so an
// agent alone in its window gets that time, and one sharing a window gets
// the time its pane's screen was last seen to change: unknown on the first
// look, then exact to the interval between looks. Muxy reports each agent's
// own state.
type Tracker struct {
	mu   sync.Mutex
	seen map[string]paneSeen
	now  func() time.Time
	// tmux and muxy run the commands; nil runs them for real.
	tmux func(ctx context.Context, args ...string) ([]byte, error)
	muxy func(ctx context.Context) ([]byte, error)
}

type paneSeen struct {
	hash    [32]byte
	since   time.Time // when the screen last changed, or was first seen
	changed bool      // a change was seen, so since is when it printed
}

// NewTracker returns a Tracker with nothing seen yet.
func NewTracker() *Tracker {
	return &Tracker{seen: map[string]paneSeen{}, now: time.Now}
}

// Read returns each holder's activity, by Key.
func (tr *Tracker) Read(ctx context.Context, holders []agents.Holder) map[string]Activity {
	out := map[string]Activity{}
	panes := map[string][]string{} // socket -> the agents' panes on it
	muxy := false
	for _, h := range holders {
		switch h.Launcher {
		case "tmux":
			panes[h.Socket] = append(panes[h.Socket], h.Session)
		case "muxy":
			muxy = true
		}
	}
	now := tr.now()
	for socket, wanted := range panes {
		base := []string{}
		if socket != "" {
			base = []string{"-S", socket}
		}
		data, err := tr.runTmux(ctx, append(base, "list-panes", "-a", "-F", "#{pane_id}\t#{window_activity}\t#{window_panes}")...)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			f := strings.Split(line, "\t")
			if len(f) != 3 || !slices.Contains(wanted, f[0]) {
				continue
			}
			key := Key(agents.Holder{Launcher: "tmux", Socket: socket, Session: f[0]})
			if f[2] == "1" {
				if sec, err := strconv.ParseInt(f[1], 10, 64); err == nil && sec > 0 {
					idle := now.Sub(time.Unix(sec, 0)).Truncate(time.Second)
					out[key] = Activity{Text: "printed " + Ago(idle), Idle: idle}
				}
				continue
			}
			screen, err := tr.runTmux(ctx, append(base, "capture-pane", "-p", "-t", f[0])...)
			if err != nil {
				continue
			}
			out[key] = tr.observe(key, sha256.Sum256(screen), now)
		}
	}
	if muxy {
		run := tr.muxy
		if run == nil {
			run = muxyActivityList
		}
		if data, err := run(ctx); err == nil {
			for session, st := range ParseMuxyActivity(data) {
				out[Key(agents.Holder{Launcher: "muxy", Session: session})] = Activity{Text: st}
			}
		}
	}
	return out
}

// observe records a pane's screen and says how long it has been quiet.
func (tr *Tracker) observe(key string, hash [32]byte, now time.Time) Activity {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	prev, ok := tr.seen[key]
	switch {
	case !ok:
		tr.seen[key] = paneSeen{hash: hash, since: now}
		return Activity{}
	case prev.hash != hash:
		tr.seen[key] = paneSeen{hash: hash, since: now, changed: true}
		return Activity{Text: "printed " + Ago(0), Idle: 0}
	}
	idle := now.Sub(prev.since).Truncate(time.Second)
	if prev.changed {
		return Activity{Text: "printed " + Ago(idle), Idle: idle}
	}
	return Activity{Text: "quiet ≥ " + strings.TrimSuffix(Ago(idle), " ago"), Idle: idle}
}

func (tr *Tracker) runTmux(ctx context.Context, args ...string) ([]byte, error) {
	if tr.tmux != nil {
		return tr.tmux(ctx, args...)
	}
	ctx, cancel := context.WithTimeout(ctx, activityTimeout)
	defer cancel()
	return exec.CommandContext(ctx, "tmux", args...).Output()
}

// muxyActivityList runs `muxy activity list`.
func muxyActivityList(ctx context.Context) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, activityTimeout)
	defer cancel()
	bin := "muxy"
	if _, err := exec.LookPath(bin); err != nil {
		bin = "/Applications/Muxy.app/Contents/MacOS/muxy"
	}
	return exec.CommandContext(ctx, bin, "activity", "list").Output()
}

// ParseMuxyActivity reads `muxy activity list`'s text form: tab-separated
// session-id, project-id, provider, state, event-id, event.
func ParseMuxyActivity(data []byte) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Split(line, "\t")
		if len(f) < 4 || f[0] == "" {
			continue
		}
		text := f[3]
		if len(f) >= 6 && f[5] != "" {
			text += ": " + f[5]
		}
		out[f[0]] = text
	}
	return out
}

// Ago says how long ago something was, coarsely: 12s, 4m, 3h, 2d.
func Ago(d time.Duration) string {
	switch {
	case d < 0:
		return "now"
	case d < time.Minute:
		return strconv.Itoa(int(d.Seconds())) + "s ago"
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes())) + "m ago"
	case d < 48*time.Hour:
		return strconv.Itoa(int(d.Hours())) + "h ago"
	default:
		return strconv.Itoa(int(d.Hours()/24)) + "d ago"
	}
}
