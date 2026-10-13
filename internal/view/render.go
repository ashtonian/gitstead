package view

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
)

// RenderOptions shape a rendering.
type RenderOptions struct {
	// All shows every repo; otherwise only those needing attention.
	All bool
	// Width cuts each line to this many columns; 0 doesn't.
	Width int
	// Color styles states and headers.
	Color bool
}

var (
	bad   = lipgloss.NewStyle().Foreground(lipgloss.Red)
	warn  = lipgloss.NewStyle().Foreground(lipgloss.Yellow)
	good  = lipgloss.NewStyle().Foreground(lipgloss.Green)
	faint = lipgloss.NewStyle().Foreground(lipgloss.BrightBlack)
	bold  = lipgloss.NewStyle().Bold(true)
)

// Render lays the snapshot out: a summary line, then the workspace copies,
// then the repos.
func Render(s Snapshot, o RenderOptions) string {
	var b strings.Builder
	running, attention := 0, 0
	for _, c := range s.Copies {
		if c.AgentState == "running" || c.AgentState == "stopping" {
			running++
		}
	}
	var repos []RepoRow
	for _, r := range s.Repos {
		if r.Attention() {
			attention++
		}
		if o.All || r.Attention() {
			repos = append(repos, r)
		}
	}
	line := fmt.Sprintf("git-mgr top  %s  ·  %s, %s running  ·  %s, %d need attention",
		s.Taken.Local().Format("15:04:05"), count(len(s.Copies), "copy", "copies"), count(running, "agent", "agents"),
		count(len(s.Repos), "repo", "repos"), attention)
	if o.Width > 0 {
		line = cut(line, o.Width)
	}
	b.WriteString(o.style(bold, line) + "\n\n")

	if len(s.Copies) > 0 {
		t := table{header: []string{"COPY", "AGENT", "ACTIVITY", "BRANCH", "STATE", "AHEAD/BEHIND", "BACKUP", "NOTES"}}
		for _, c := range s.Copies {
			notes := strings.Join(c.Issues, "; ")
			if c.PR != "" {
				notes = strings.TrimPrefix(notes+"; "+c.PR, "; ")
			}
			t.rows = append(t.rows, []cell{
				{text: c.Label},
				{text: dash(c.AgentState), style: agentStyle(c.AgentState)},
				{text: dash(c.Activity), style: idleStyle(c)},
				{text: dash(c.Branch)},
				{text: c.State, style: stateStyle(c.State)},
				{text: fmt.Sprintf("%d/%d", c.Ahead, c.Behind), style: aheadStyle(c.Ahead, c.Behind)},
				{text: age(s.Taken, c.LastBackup)},
				{text: notes},
			})
		}
		b.WriteString(t.render(o))
		b.WriteString("\n")
	}

	t := table{header: []string{"REPO", "STATE", "BRANCH", "AHEAD/BEHIND", "FETCHED", "BACKUP", "NOTES"}}
	for _, r := range repos {
		t.rows = append(t.rows, []cell{
			{text: r.Key},
			{text: r.State, style: stateStyle(r.State)},
			{text: dash(r.Branch)},
			{text: fmt.Sprintf("%d/%d", r.Ahead, r.Behind), style: aheadStyle(r.Ahead, r.Behind)},
			{text: age(s.Taken, r.LastFetch)},
			{text: age(s.Taken, r.LastBackup)},
			{text: r.Problem, style: &bad},
		})
	}
	switch {
	case len(t.rows) > 0:
		b.WriteString(t.render(o))
	case len(s.Repos) > 0:
		b.WriteString(o.style(faint, "Every repo is clean and up to date.") + "\n")
	}
	return b.String()
}

func (o RenderOptions) style(s lipgloss.Style, text string) string {
	if !o.Color {
		return text
	}
	return s.Render(text)
}

type cell struct {
	text  string
	style *lipgloss.Style
}

type table struct {
	header []string
	rows   [][]cell
}

// render pads each column to its widest cell, two spaces apart, cuts each
// line at o.Width, and styles cells after padding, so escape codes never
// count towards a column's width.
func (t table) render(o RenderOptions) string {
	widths := make([]int, len(t.header))
	for i, h := range t.header {
		widths[i] = utf8.RuneCountInString(h)
	}
	for _, r := range t.rows {
		for i, c := range r {
			widths[i] = max(widths[i], utf8.RuneCountInString(c.text))
		}
	}
	var b strings.Builder
	header := make([]cell, len(t.header))
	for i, h := range t.header {
		header[i] = cell{text: h, style: &faint}
	}
	for _, r := range append([][]cell{header}, t.rows...) {
		used := 0
		var parts []string
		for i, c := range r {
			text := c.text
			if i < len(r)-1 {
				text += strings.Repeat(" ", widths[i]-utf8.RuneCountInString(text)) + "  "
			}
			if o.Width > 0 {
				if used >= o.Width {
					break
				}
				text = cut(text, o.Width-used)
			}
			used += utf8.RuneCountInString(text)
			if c.style != nil {
				// Style the cell's text, not its padding.
				trimmed := strings.TrimRight(text, " ")
				text = o.style(*c.style, trimmed) + text[len(trimmed):]
			}
			parts = append(parts, text)
		}
		b.WriteString(strings.TrimRight(strings.Join(parts, ""), " ") + "\n")
	}
	return b.String()
}

// cut shortens s to width runes, marking a cut with an ellipsis.
func cut(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= width {
		return s
	}
	r := []rune(s)
	return string(r[:width-1]) + "…"
}

func count(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func age(now, t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return Ago(now.Sub(t))
}

func stateStyle(state string) *lipgloss.Style {
	switch state {
	case "clean":
		return nil
	case "dirty", "untracked":
		return &warn
	default:
		return &bad
	}
}

func agentStyle(state string) *lipgloss.Style {
	switch state {
	case "running":
		return &good
	case "stopping", "retiring":
		return &warn
	case "missing":
		return &bad
	default:
		return &faint
	}
}

// idleStyle marks an agent's pane that hasn't printed for a while: it is
// likely waiting for you.
func idleStyle(c CopyRow) *lipgloss.Style {
	if c.Idle >= 2*time.Minute {
		return &warn
	}
	return nil
}

func aheadStyle(ahead, behind int) *lipgloss.Style {
	if behind > 0 {
		return &warn
	}
	return nil
}
