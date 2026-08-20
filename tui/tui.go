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
	// Provider is the name of the model provider the run targets.
	Provider string
	// Workdir is the directory the agent's tools operate in.
	Workdir string
	// ShowDiff renders a syntax-highlighted diff panel under each edit/write.
	ShowDiff bool
	// Plain forces the unstyled streaming renderer even in a terminal. Without a
	// TTY Zot also streams, with styling controlled independently by Color.
	Plain bool
	// Color controls ANSI styling for a non-interactive stream: auto, always, or
	// never. It does not make the stream interactive or start the full-screen UI.
	Color string

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

	// QuitOnDone closes the full-screen viewer as soon as the run ends, instead
	// of holding the final screen until the user quits. For a run of record the
	// held screen IS the report, so it stays; for a run whose deliverable is
	// collected by the caller - a draft run - holding the screen blocks the
	// step that consumes the outcome. The streaming renderers already end with
	// the run, so this only affects the viewer.
	QuitOnDone bool

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
//
// Along with any error it returns the run's recorded Outcome, so a caller whose
// deliverable is the outcome itself - a draft run - gets it without scraping
// the screen.
func Run(ctx context.Context, client *agent.Client, meta Meta, opts agent.ExecuteWithToolsOptions) (Outcome, error) {
	// Set the brand colours before anything renders. An empty Theme falls back to
	// zot's neutral default (see applyTheme).
	applyTheme(meta.Theme)

	// A bare caller (or zot itself) leaves AppName empty; default it so the badge
	// and headers still read correctly.
	if meta.AppName == "" {
		meta.AppName = "zot"
	}

	// --plain is an explicit request for an unstyled transcript. Without a
	// terminal, stream too, but keep ANSI styling when the consumer declared
	// that it supports color (a browser terminal is the main example).
	if meta.Plain {
		return runPlain(ctx, client, meta, opts)
	}

	if !isInteractive() {
		return runStream(ctx, client, meta, opts, streamColorEnabled(meta.Color))
	}

	m := newModel(meta.AppName, meta.Task, meta.Model, meta.Provider, meta.Workdir, meta.ShowDiff)
	if meta.MaxScrollback > 0 {
		m.maxEntries = meta.MaxScrollback
	}
	m.stats = meta.Stats
	m.maxIterations = meta.MaxIterations
	m.maxCalls = meta.MaxCalls
	m.maxDuration = meta.MaxDuration
	m.quitOnDone = meta.QuitOnDone

	return runViewer(ctx, m, client, opts, func(p *tea.Program) (tea.Model, error) { return p.Run() })
}

// runViewer owns the viewer's lifetime: it starts the agent, hands the program
// to start, and shuts the agent down once start returns. start is a seam for
// tests, which cannot open a terminal - Run passes (*tea.Program).Run.
func runViewer(
	ctx context.Context,
	m model,
	client *agent.Client,
	opts agent.ExecuteWithToolsOptions,
	start func(*tea.Program) (tea.Model, error),
) (Outcome, error) {
	// Quitting the viewer stops the agent rather than merely stopping watching
	// it. The agent has shell and file-write access, so an embedding process
	// that returned from here with the run still going would leave something
	// editing the working tree with nothing on screen reporting what it does.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	p := tea.NewProgram(m, tea.WithAltScreen())

	go runAgent(ctx, p, client, opts)

	final, err := start(p)
	if err != nil {
		return Outcome{}, err
	}
	switch m := final.(type) {
	case model:
		return m.outcome(), m.runError()
	case *model:
		return m.outcome(), m.runError()
	default:
		return Outcome{}, nil
	}
}
