package launch

import (
	"context"
	"encoding/base64"
	"fmt"
	"math"
	"os/exec"
	"strings"

	"github.com/ashtonian/gitten/internal/config"
	"github.com/ashtonian/gitten/internal/shellutil"
)

// ITerm opens a fleet in iTerm2: a window, a tab per workspace (or agent),
// a split per agent, spilling into another tab past MaxPanes. It drives
// iTerm with AppleScript, so it needs macOS and the Automation permission
// for the terminal gitten runs in.
type ITerm struct {
	Opts config.ITermConfig
	Env  Env
	// run runs an AppleScript; nil runs it with osascript.
	run func(ctx context.Context, script string) error
}

func (t *ITerm) Name() string { return "iterm" }

func (t *ITerm) Open(ctx context.Context, p Plan) error {
	script, err := t.Script(p)
	if err != nil {
		return err
	}
	if t.run != nil {
		return t.run(ctx, script)
	}
	out, err := exec.CommandContext(ctx, "osascript", "-e", script).CombinedOutput()
	if err != nil {
		return fmt.Errorf("osascript: %w: %s (does the terminal gitten runs in have Automation permission for iTerm?)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Print is the AppleScript that opens the plan.
func (t *ITerm) Print(p Plan) (string, error) { return t.Script(p) }

// Script is the AppleScript that opens the plan.
func (t *ITerm) Script(p Plan) (string, error) {
	var b strings.Builder
	b.WriteString("tell application \"iTerm2\"\n")
	if t.Opts.Focus {
		b.WriteString("\tactivate\n")
	}
	profile := asString(t.Opts.Profile)
	if t.Opts.Window == "current" {
		b.WriteString("\tif (count of windows) is 0 then\n")
		fmt.Fprintf(&b, "\t\tset w to (create window with profile %s)\n", profile)
		b.WriteString("\t\tset t to current tab of w\n")
		b.WriteString("\telse\n\t\tset w to current window\n")
		fmt.Fprintf(&b, "\t\ttell w to set t to (create tab with profile %s)\n", profile)
		b.WriteString("\tend if\n")
	} else {
		fmt.Fprintf(&b, "\tset w to (create window with profile %s)\n", profile)
		b.WriteString("\tset t to current tab of w\n")
	}

	first := true
	for _, tab := range t.tabs(p) {
		if !first {
			fmt.Fprintf(&b, "\ttell w to set t to (create tab with profile %s)\n", profile)
		}
		first = false
		b.WriteString("\tset s1 to current session of t\n")
		for i, split := range gridSplits(len(tab), t.Opts.Split) {
			fmt.Fprintf(&b, "\ttell s%d to set s%d to (split %s with profile %s)\n", split.from, i+2, split.direction, profile)
		}
		for i, ta := range tab {
			line, err := t.line(p, ta)
			if err != nil {
				return "", err
			}
			fmt.Fprintf(&b, "\ttell s%d\n", i+1)
			fmt.Fprintf(&b, "\t\tset name to %s\n", asString(ta.Title))
			if t.Opts.TypeDelay > 0 {
				fmt.Fprintf(&b, "\t\tdelay %.3f\n", t.Opts.TypeDelay.Seconds())
			}
			fmt.Fprintf(&b, "\t\twrite text %s\n", asString(line))
			b.WriteString("\tend tell\n")
		}
	}
	b.WriteString("end tell\n")
	return b.String(), nil
}

// tabAgent is an agent in a tab, with its workspace.
type tabAgent struct {
	Agent
	workspace string
}

// tabs groups the plan's agents into tabs: each workspace's, in chunks of
// MaxPanes, or one tab per agent.
func (t *ITerm) tabs(p Plan) [][]tabAgent {
	var tabs [][]tabAgent
	for _, ws := range p.Workspaces {
		var all []tabAgent
		for _, a := range ws.Agents {
			all = append(all, tabAgent{a, ws.Name})
		}
		size := t.Opts.MaxPanes
		if t.Opts.TabPer == "agent" {
			size = 1
		}
		for start := 0; start < len(all); start += size {
			tabs = append(tabs, all[start:min(start+size, len(all))])
		}
	}
	return tabs
}

// line is what is typed into an agent's split: its badge, then its command,
// over ssh for a remote host.
func (t *ITerm) line(p Plan, a tabAgent) (string, error) {
	cmd := a.Command
	if p.Host != "" {
		cmd = "ssh -t " + shellutil.Word(p.Host) + " " + shellutil.Word(cmd)
	}
	if t.Opts.Badge == "" {
		return cmd, nil
	}
	badge, err := config.RenderLaunch(t.Opts.Badge, config.LaunchNames{Fleet: p.Fleet, Workspace: a.workspace, Agent: a.Name, Profile: a.Profile})
	if err != nil {
		return "", err
	}
	// iTerm's badge escape sequence takes the text base64-encoded.
	set := fmt.Sprintf(`printf '\033]1337;SetBadgeFormat=%%s\007' %s`, base64.StdEncoding.EncodeToString([]byte(badge)))
	return set + "; " + cmd, nil
}

// split makes session number n+2 from session number from.
type split struct {
	from      int
	direction string // vertically (side by side) or horizontally (stacked)
}

// gridSplits lays out n sessions, numbered from 1 in reading order: in
// columns side by side, in rows stacked, or in a grid of ceil(sqrt(n))
// columns.
func gridSplits(n int, layout string) []split {
	var out []split
	switch layout {
	case "columns":
		for i := 1; i < n; i++ {
			out = append(out, split{i, "vertically"})
		}
		return out
	case "rows":
		for i := 1; i < n; i++ {
			out = append(out, split{i, "horizontally"})
		}
		return out
	}
	cols := int(math.Ceil(math.Sqrt(float64(n))))
	if cols == 0 {
		return nil
	}
	rows := int(math.Ceil(float64(n) / float64(cols)))
	// The column tops are split off first, then each column is split down;
	// numbering them in that order reads left to right, top to bottom.
	top := make([]int, cols)
	top[0] = 1
	next := 2
	for c := 1; c < cols && next <= n; c++ {
		out = append(out, split{top[c-1], "vertically"})
		top[c] = next
		next++
	}
	for c := 0; c < cols; c++ {
		above := top[c]
		for r := 1; r < rows && next <= n; r++ {
			out = append(out, split{above, "horizontally"})
			above = next
			next++
		}
	}
	return out
}

// asString quotes s as an AppleScript string.
func asString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}
