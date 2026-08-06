// Package tui renders the read-only terminal view of an autonomous agent run.
//
// The UI deliberately has no text input: the user watches the agent work, they
// do not drive it. Everything on screen is derived from the event stream that
// agent.ExecuteWithTools emits - tool calls, iterations, token narration, and
// the final exit.
package tui

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/openzot/openzot/agent"
)

// Meta is the header display information shown above the activity log.
type Meta struct {
	// AppName is the embedding application's name, shown in the title badge
	// ("✦ rook"), the startup line, and the plain-mode header. Empty defaults to
	// "zot", so a bare caller still reads correctly.
	AppName string
	// Task is the one-line instruction the agent is working on.
	Task string
	// Model is the model name driving the agent.
	Model string
	// Backend is the name of the backend the run targets.
	Backend string
	// Workdir is the directory the agent's tools operate in.
	Workdir string
	// ShowDiff renders a syntax-highlighted diff panel under each edit/write.
	ShowDiff bool
	// Plain forces the unstyled streaming renderer even in a terminal. Plain mode
	// is also selected automatically when stdout is not a TTY.
	Plain bool

	// Theme is the colour identity for the view. The zero value is zot's neutral
	// DefaultTheme; an embedding application passes its own accent (see Theme).
	Theme Theme

	// MaxScrollback caps how many log lines the viewer keeps on screen. Zero uses
	// DefaultMaxScrollback; a larger value keeps more history (at more memory).
	// The full run is always in the session log regardless.
	MaxScrollback int

	// Stats selects which header fields to show, and in what order (see
	// KnownStats). Empty uses DefaultStats. Unknown names are ignored.
	Stats []string

	// MaxIterations, MaxCalls and MaxDuration are the configured run limits, shown
	// as "5/1000" progress in the meta bar. Zero means unbounded (or not worth
	// showing, e.g. the default iteration backstop), so no denominator appears.
	MaxIterations int
	MaxCalls      int
	MaxDuration   time.Duration
}

// Run renders the read-only TUI while the autonomous agent executes. It owns the
// Bubble Tea program lifecycle and blocks until the user quits or the program
// errors. The agent runs in the background and communicates with the UI solely
// through tea messages.
func Run(ctx context.Context, client *agent.Client, meta Meta, opts agent.ExecuteWithToolsOptions) error {
	// Set the brand colours before anything renders. An empty Theme falls back to
	// zot's neutral default (see applyTheme).
	applyTheme(meta.Theme)

	// A bare caller (or zot itself) leaves AppName empty; default it so the badge
	// and headers still read correctly.
	if meta.AppName == "" {
		meta.AppName = "zot"
	}

	// Without a usable terminal (or when forced), stream plain text instead of
	// trying to start an alt-screen program that would fail or garble.
	if meta.Plain || !isInteractive() {
		return runPlain(ctx, client, meta, opts)
	}

	m := newModel(meta.AppName, meta.Task, meta.Model, meta.Backend, meta.Workdir, meta.ShowDiff)
	if meta.MaxScrollback > 0 {
		m.maxEntries = meta.MaxScrollback
	}
	m.stats = meta.Stats
	m.maxIterations = meta.MaxIterations
	m.maxCalls = meta.MaxCalls
	m.maxDuration = meta.MaxDuration
	p := tea.NewProgram(m, tea.WithAltScreen())

	go runAgent(ctx, p, client, opts)

	final, err := p.Run()
	if err != nil {
		return err
	}
	switch m := final.(type) {
	case model:
		return m.runError()
	case *model:
		return m.runError()
	default:
		return nil
	}
}
