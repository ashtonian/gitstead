package launch

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// muxyStateVersion is the desktop state format open: state edits; any other
// version is refused, since Muxy may have changed what it means.
const muxyStateVersion = 3

const (
	muxyBundleID     = "com.muxy.app"
	muxyAppWait      = 60 * time.Second
	muxySessionsWait = 90 * time.Second
)

// desktopApp is the Muxy desktop app, which open: state quits, edits the
// saved state of and relaunches.
type desktopApp interface {
	Running(ctx context.Context) (bool, error)
	Quit(ctx context.Context) error
	Launch(ctx context.Context) error
	// StatePath is the app's saved state file.
	StatePath() string
}

// openState adds a tab per agent to each project that has none in the
// desktop app's saved state, then relaunches the app, which starts each
// tab's command as it connects -- what Apply Layout would store. The app
// must be quit while its state is edited; Opts.Restart decides whether it
// may be.
func (m *Muxy) openState(ctx context.Context, p Plan, app desktopApp, muxy func(...string) ([]byte, error)) error {
	path := app.StatePath()
	ids := map[string]bool{}
	for _, id := range m.Projects {
		ids[id] = true
	}
	// A state git-mgr can't read is refused now, not after waiting for it.
	if _, err := loadMuxyState(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	listed := func() bool {
		st, err := loadMuxyState(path)
		return err == nil && hasProjects(st, ids)
	}
	if !listed() {
		// The app lists a project only once it syncs the server's catalog.
		if running, err := app.Running(ctx); err != nil {
			return err
		} else if !running {
			if err := app.Launch(ctx); err != nil {
				return err
			}
		}
		if err := waitFor(ctx, muxyAppWait, "the Muxy app to list the fleet's projects", listed); err != nil {
			return err
		}
	}
	st, err := loadMuxyState(path)
	if err != nil {
		return err
	}
	var empty []Workspace
	for _, ws := range p.Workspaces {
		if len(tabsOf(st, m.Projects[ws.Name])) == 0 {
			empty = append(empty, ws)
		}
	}
	if len(empty) == 0 {
		fmt.Fprintln(m.Env.Stdout, "Every project already has tabs; the Muxy app was left alone.")
		return nil
	}

	running, err := app.Running(ctx)
	if err != nil {
		return err
	}
	if running {
		switch m.Opts.Restart {
		case "never":
			return errors.New("the Muxy app is running and launchers.muxy.restart is never: quit it and open again, or use open: layout")
		case "ask":
			if !m.Env.Confirm(fmt.Sprintf("Quit and relaunch the Muxy app to open tabs in %d project(s)?", len(empty))) {
				return errors.New("left the Muxy app alone; nothing was opened")
			}
		}
		fmt.Fprintln(m.Env.Stdout, "Restarting the Muxy app to open the agents' tabs")
		if err := app.Quit(ctx); err != nil {
			return err
		}
		quit := func() bool { r, err := app.Running(ctx); return err == nil && !r }
		if err := waitFor(ctx, muxyAppWait, "the Muxy app to quit (is a confirmation dialog open?)", quit); err != nil {
			return err
		}
	}

	if st, err = loadMuxyState(path); err != nil {
		return err
	}
	for _, ws := range empty {
		if err := addMuxyTabs(st, m.Projects[ws.Name], ws.Agents); err != nil {
			return err
		}
	}
	if running, err := app.Running(ctx); err != nil || running {
		return errors.New("the Muxy app started again before its state was written; nothing was changed")
	}
	backup, err := writeMuxyState(path, st)
	if err != nil {
		return err
	}
	if err := app.Launch(ctx); err != nil {
		return err
	}
	for _, ws := range empty {
		id, want := m.Projects[ws.Name], len(ws.Agents)
		live := func() bool { return liveSessions(muxy, id) >= want }
		if err := waitFor(ctx, muxySessionsWait, fmt.Sprintf("%d sessions in %s", want, ws.Name), live); err != nil {
			return err
		}
		fmt.Fprintf(m.Env.Stdout, "%-12s %d tabs running\n", ws.Name, want)
	}
	fmt.Fprintf(m.Env.Stdout, "The app's previous state is saved in %s\n", backup)
	return nil
}

func loadMuxyState(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var st map[string]any
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	if v, _ := st["version"].(float64); int(v) != muxyStateVersion {
		return nil, fmt.Errorf("%s is version %v, not %d: Muxy changed its state format, so use launchers.muxy.open: layout instead", path, st["version"], muxyStateVersion)
	}
	return st, nil
}

func projectsOf(st map[string]any) []map[string]any {
	list, _ := st["projects"].([]any)
	var out []map[string]any
	for _, p := range list {
		if m, ok := p.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func hasProjects(st map[string]any, ids map[string]bool) bool {
	found := 0
	for _, p := range projectsOf(st) {
		if id, _ := p["id"].(string); ids[id] {
			found++
		}
	}
	return found == len(ids)
}

func tabsOf(st map[string]any, id string) []any {
	for _, p := range projectsOf(st) {
		if pid, _ := p["id"].(string); pid == id {
			tabs, _ := p["tabs"].([]any)
			return tabs
		}
	}
	return nil
}

// addMuxyTabs gives the project a terminal tab per agent, as Apply Layout
// stores them: each pane without a session yet, and its command queued in
// startup_commands, which the app types when the pane connects.
func addMuxyTabs(st map[string]any, id string, agents []Agent) error {
	var project map[string]any
	for _, p := range projectsOf(st) {
		if pid, _ := p["id"].(string); pid == id {
			project = p
		}
	}
	if project == nil {
		return fmt.Errorf("project %s isn't in the Muxy app's state", id)
	}
	commands, _ := st["startup_commands"].(map[string]any)
	if commands == nil {
		commands = map[string]any{}
		st["startup_commands"] = commands
	}
	var tabs []any
	for _, a := range agents {
		pane := newMuxyID()
		tabs = append(tabs, map[string]any{
			"id":           newMuxyID(),
			"custom_title": a.Title,
			"color":        nil,
			"pinned":       false,
			"panes": []any{map[string]any{
				"id": pane, "title": a.Title,
				"content": map[string]any{"type": "terminal", "session": nil},
			}},
			"layout": map[string]any{"Leaf": pane},
			"zoomed": nil,
		})
		commands[pane] = a.Command
	}
	project["tabs"] = tabs
	window, _ := st["window"].(map[string]any)
	if window == nil {
		window = map[string]any{}
		st["window"] = window
	}
	selected, _ := window["selected_tab"].(map[string]any)
	if selected == nil {
		selected = map[string]any{}
		window["selected_tab"] = selected
	}
	if len(tabs) > 0 {
		selected[id] = tabs[0].(map[string]any)["id"]
	}
	return nil
}

func newMuxyID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// writeMuxyState saves st over the state file, keeping the previous one
// beside it, and returns where.
func writeMuxyState(path string, st map[string]any) (string, error) {
	backup := path + ".git-mgr-backup"
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(backup, data, 0o600); err != nil {
		return "", err
	}
	out, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return "", err
	}
	tmp := path + ".git-mgr-tmp"
	if err := os.WriteFile(tmp, append(out, '\n'), 0o600); err != nil {
		return "", err
	}
	return backup, os.Rename(tmp, path)
}

// liveSessions counts a project's live sessions; 0 when it can't tell.
func liveSessions(muxy func(...string) ([]byte, error), id string) int {
	out, err := muxy("session", "list", "--project", id)
	if err != nil {
		return 0
	}
	n := 0
	for _, row := range strings.Split(string(out), "\n") {
		if f := strings.Split(row, "\t"); len(f) > 2 && f[2] == "Live" {
			n++
		}
	}
	return n
}

func waitFor(ctx context.Context, timeout time.Duration, what string, done func() bool) error {
	deadline := time.Now().Add(timeout)
	for !done() {
		if time.Now().After(deadline) {
			return fmt.Errorf("gave up after %s waiting for %s", timeout, what)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return nil
}

// realApp is the Muxy desktop app on this Mac.
type realApp struct{ env Env }

func (realApp) StatePath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "Application Support", "Muxy 2", "desktop-state.json")
}

func (realApp) Running(ctx context.Context) (bool, error) {
	out, err := osascript(ctx, fmt.Sprintf(`application id %q is running`, muxyBundleID))
	return out == "true", err
}

func (realApp) Quit(ctx context.Context) error {
	_, err := osascript(ctx, fmt.Sprintf(`tell application id %q to quit`, muxyBundleID))
	return err
}

func (realApp) Launch(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, muxyAppWait)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "open", "-b", muxyBundleID).CombinedOutput(); err != nil {
		return fmt.Errorf("launching Muxy: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func osascript(ctx context.Context, script string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, muxyAppWait)
	defer cancel()
	out, err := exec.CommandContext(ctx, "osascript", "-e", script).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("osascript: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}
