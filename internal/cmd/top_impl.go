package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"golang.org/x/term"

	"github.com/ashtonian/git-mgr/internal/config"
	"github.com/ashtonian/git-mgr/internal/state"
	"github.com/ashtonian/git-mgr/internal/view"
)

// TopCmd is a live view of everything git-mgr manages: each workspace copy
// with its agent's state and terminal activity, and each repo that needs a
// look -- dirty, ahead, behind, or failing to fetch or back up.
type TopCmd struct {
	Every time.Duration `help:"How often to look again." default:"10s"`
	All   bool          `help:"Show every repo, not only those needing attention."`
	Once  bool          `help:"Print one view and exit, instead of refreshing."`
	JSON  bool          `help:"Print one view as JSON and exit."`
	Repos string        `help:"Only repos matching this glob." predictor:"repo"`
	Tag   string        `help:"Only repos with this tag." predictor:"tag"`
	Set   string        `help:"Only repos in this named set." predictor:"set"`
}

func (c *TopCmd) Run(ctx *Context) error {
	cfg, err := ctx.LoadConfig()
	if err != nil {
		return err
	}
	pass, err := selection{c.Repos, c.Tag, c.Set}.predicate(cfg)
	if err != nil {
		return err
	}
	if c.Every < time.Second {
		return fmt.Errorf("--every %s is too often; give at least 1s", c.Every)
	}
	// The tracker remembers each agent pane's screen between looks.
	tracker := view.NewTracker()
	collect := func(goCtx context.Context) (view.Snapshot, error) {
		// Read afresh each time, without the lock: runs start and stop, and
		// syncs record, while the view is open.
		st, err := state.Load(ctx.State)
		if err != nil {
			return view.Snapshot{}, err
		}
		return view.Collect(goCtx, cfg, st, view.Options{Repos: pass, Concurrency: cfg.Concurrency, Activity: tracker.Read}), nil
	}

	if c.JSON {
		snap, err := collect(ctx.GoCtx())
		if err != nil {
			return err
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(snap)
	}
	tty := term.IsTerminal(int(os.Stdout.Fd()))
	if c.Once || !tty {
		snap, err := collect(ctx.GoCtx())
		if err != nil {
			return err
		}
		width := 0
		if tty {
			width, _, _ = term.GetSize(int(os.Stdout.Fd()))
		}
		fmt.Print(view.Render(snap, view.RenderOptions{All: c.All, Width: width, Color: tty}))
		return nil
	}

	m := &topModel{cfg: cfg, every: c.Every, all: c.All, collect: collect, ctx: ctx.GoCtx(), loading: true}
	_, err = tea.NewProgram(m, tea.WithContext(ctx.GoCtx())).Run()
	if err != nil && ctx.GoCtx().Err() == nil {
		return err
	}
	return m.err
}

type snapMsg struct {
	snap view.Snapshot
	err  error
}

type tickMsg struct{}

// topModel is the refreshing view. A new look starts only once the last one
// is in, so a slow one never piles up behind itself.
type topModel struct {
	cfg     *config.Config
	ctx     context.Context
	every   time.Duration
	all     bool
	collect func(context.Context) (view.Snapshot, error)

	snap    view.Snapshot
	have    bool
	loading bool
	err     error
	width   int
	height  int
}

func (m *topModel) look() tea.Cmd {
	return func() tea.Msg {
		snap, err := m.collect(m.ctx)
		return snapMsg{snap, err}
	}
}

func (m *topModel) Init() tea.Cmd { return m.look() }

func (m *topModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.String() {
		case "q", "ctrl+c", "esc":
			return m, tea.Quit
		case "a":
			m.all = !m.all
		case "r":
			if !m.loading {
				m.loading = true
				return m, m.look()
			}
		}
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case snapMsg:
		m.loading = false
		if msg.err != nil {
			m.err = msg.err
			return m, tea.Quit
		}
		m.snap, m.have = msg.snap, true
		return m, tea.Tick(m.every, func(time.Time) tea.Msg { return tickMsg{} })
	case tickMsg:
		if !m.loading {
			m.loading = true
			return m, m.look()
		}
	}
	return m, nil
}

func (m *topModel) View() tea.View {
	v := tea.NewView("")
	v.AltScreen = true
	v.WindowTitle = "git-mgr top"
	if !m.have {
		v.SetContent("Looking at every repo and workspace…")
		return v
	}
	body := view.Render(m.snap, view.RenderOptions{All: m.all, Width: m.width, Color: true})
	lines := strings.Split(strings.TrimRight(body, "\n"), "\n")
	room := m.height - 2
	if room > 0 && len(lines) > room {
		hidden := len(lines) - room + 1
		lines = append(lines[:room-1], fmt.Sprintf("… %d more lines; make the window taller, or use git-mgr top --once", hidden))
	}
	toggle := "a show every repo"
	if m.all {
		toggle = "a show only repos needing attention"
	}
	footer := fmt.Sprintf("q quit · r look again · %s · every %s", toggle, m.every)
	if m.loading {
		footer += " · looking…"
	}
	v.SetContent(strings.Join(lines, "\n") + "\n\n" + footer)
	return v
}
