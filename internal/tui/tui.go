// Package tui renders the read-only terminal view of an autonomous agent run.
//
// The UI deliberately has no text input: the user watches the agent work, they
// do not drive it. Everything on screen is derived from the event stream that
// agent.ExecuteWithTools emits — tool calls, iterations, token narration, and
// the final exit.
package tui

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/chatbotkit/go-sdk/agent"
	"github.com/chatbotkit/go-sdk/sdk"
)

// Meta is the header display information shown above the activity log.
type Meta struct {
	// Task is the one-line instruction the agent is working on.
	Task string
	// Model is the ChatBotKit model alias driving the agent.
	Model string
	// Workdir is the directory the agent's tools operate in.
	Workdir string
	// ShowDiff renders a syntax-highlighted diff panel under each edit/write.
	ShowDiff bool
	// Plain forces the unstyled streaming renderer even in a terminal. Plain mode
	// is also selected automatically when stdout is not a TTY.
	Plain bool
}

// Run renders the read-only TUI while the autonomous agent executes. It owns the
// Bubble Tea program lifecycle and blocks until the user quits or the program
// errors. The agent runs in the background and communicates with the UI solely
// through tea messages.
func Run(ctx context.Context, client *sdk.Client, meta Meta, opts agent.ExecuteWithToolsOptions) error {
	// Without a usable terminal (or when forced), stream plain text instead of
	// trying to start an alt-screen program that would fail or garble.
	if meta.Plain || !isInteractive() {
		return runPlain(ctx, client, meta, opts)
	}

	m := newModel(meta.Task, meta.Model, meta.Workdir, meta.ShowDiff)
	p := tea.NewProgram(m, tea.WithAltScreen())

	go runAgent(ctx, p, client, opts)

	_, err := p.Run()
	return err
}
