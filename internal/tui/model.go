package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/chatbotkit/go-sdk/agent"
)

type status int

const (
	statusRunning status = iota
	statusDone
	statusFailed
)

// reserved counts the non-viewport rows: title + meta + a blank gap + footer.
const reserved = 4

// tickMsg drives the elapsed-time clock once a second while the agent runs.
type tickMsg struct{}

// model is the entire read-only UI. It holds no input field by design: the user
// watches, they do not type. Everything it shows is derived from the agent's
// event stream plus a couple of counters.
type model struct {
	task     string
	model    string
	backend  string
	workdir  string
	showDiff bool

	spinner spinner.Model
	vp      viewport.Model
	ready   bool
	width   int
	height  int

	// Activity log. entries are the committed, logical lines; committedWrapped
	// caches them word-wrapped to the current width so per-token redraws stay
	// cheap. pending holds the assistant's in-flight narration.
	entries          []string
	committedWrapped string
	pending          string
	follow           bool // auto-scroll to the newest activity

	status    status
	iteration int
	toolCount int
	fileEdits int
	exitCode  int
	exitMsg   string
	err       error

	startedAt time.Time
	elapsed   time.Duration
}

func newModel(task, modelName, backend, workdir string, showDiff bool) model {
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(colYellow)

	return model{
		task:      task,
		model:     modelName,
		backend:   backend,
		workdir:   workdir,
		showDiff:  showDiff,
		spinner:   sp,
		status:    statusRunning,
		follow:    true,
		startedAt: time.Now(),
	}
}

func (m model) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, tickCmd())
}

func tickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return tickMsg{} })
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		vpHeight := msg.Height - reserved
		if vpHeight < 1 {
			vpHeight = 1
		}
		if !m.ready {
			m.vp = viewport.New(msg.Width, vpHeight)
			m.ready = true
		} else {
			m.vp.Width = msg.Width
			m.vp.Height = vpHeight
		}
		m.rewrap()
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q":
			return m, tea.Quit
		case "g", "home":
			m.vp.GotoTop()
			m.follow = false
			return m, nil
		case "G", "end":
			m.vp.GotoBottom()
			m.follow = true
			return m, nil
		}
		var cmd tea.Cmd
		m.vp, cmd = m.vp.Update(msg)
		m.follow = m.vp.AtBottom()
		return m, cmd

	case spinner.TickMsg:
		if m.status != statusRunning {
			return m, nil
		}
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case tickMsg:
		if m.status != statusRunning {
			return m, nil
		}
		m.elapsed = time.Since(m.startedAt)
		return m, tickCmd()

	case agentEventMsg:
		m.handleEvent(msg.ev)
		return m, nil

	case agentErrMsg:
		if m.status == statusRunning {
			m.status = statusFailed
			m.err = msg.err
			m.flushPending()
			m.appendEntry(errStyle.Render("✗ agent error: " + msg.err.Error()))
		}
		return m, nil

	case agentDoneMsg:
		// The stream ended without an explicit exit (e.g. iteration cap reached).
		if m.status == statusRunning {
			m.status = statusDone
			m.flushPending()
			m.appendEntry(dividerStyle.Render("- stream ended -"))
		}
		return m, nil
	}

	return m, nil
}

// handleEvent folds one agent event into the UI state.
func (m *model) handleEvent(ev agent.AgentEvent) {
	switch e := ev.(type) {
	case agent.IterationEvent:
		m.iteration = e.Iteration
		m.flushPending()
		m.appendEntry(m.fillRule(fmt.Sprintf("── iteration %d ", e.Iteration)))

	case agent.TokenAgentEvent:
		m.pending += e.Token
		m.render()

	case agent.ResultAgentEvent:
		m.flushPending()

	case agent.MessageAgentEvent:
		// Server-side history bookkeeping; the content already surfaced via
		// tokens, so nothing to draw.

	case agent.ToolCallStartEvent:
		m.flushPending()
		if e.Name == "exit" {
			return // The outcome is shown by AgentExitEvent instead.
		}
		m.toolCount++
		if e.Name == "write" || e.Name == "edit" {
			m.fileEdits++
		}
		m.appendEntry(renderToolStart(e.Name, e.Args))
		if m.showDiff {
			if d := diffForTool(e.Name, e.Args, m.vp.Width); d != "" {
				m.appendEntry(d)
			}
		}

	case agent.ToolCallEndEvent:
		if s := renderToolEnd(e.Name, e.Result); s != "" {
			m.appendEntry(s)
		}

	case agent.ToolCallErrorEvent:
		m.appendEntry(errStyle.Render("    ✗ " + e.Name + ": " + e.Error))

	case agent.AgentExitEvent:
		m.exitCode = e.Code
		m.exitMsg = e.Message
		m.flushPending()
		if e.Code == 0 {
			m.status = statusDone
			m.appendEntry("\n" + okStyle.Render("✓ done") + "  " + taskStyle.Render(e.Message))
		} else {
			m.status = statusFailed
			m.appendEntry("\n" + errStyle.Render(fmt.Sprintf("✗ exited (code %d)", e.Code)) + "  " + taskStyle.Render(e.Message))
		}
	}
}

// --- viewport content management --------------------------------------------

func (m *model) appendEntry(s string) {
	m.entries = append(m.entries, s)
	m.committedWrapped = m.wrap(strings.Join(m.entries, "\n"))
	m.render()
}

// flushPending commits any streamed assistant narration as a dim thought block.
func (m *model) flushPending() {
	text := strings.TrimSpace(m.pending)
	m.pending = ""
	if text == "" {
		return
	}
	m.appendEntry(thoughtStyle.Render("  ◆ " + text))
}

// rewrap recomputes the cached content for a new width.
func (m *model) rewrap() {
	m.committedWrapped = m.wrap(strings.Join(m.entries, "\n"))
	m.render()
}

// render pushes the current committed log plus any in-flight narration into the
// viewport, keeping the latest activity in view when following.
func (m *model) render() {
	if !m.ready {
		return
	}
	body := m.committedWrapped
	if p := strings.TrimSpace(m.pending); p != "" {
		if body != "" {
			body += "\n"
		}
		body += m.wrap(thoughtStyle.Render("  ◆ " + p))
	}
	m.vp.SetContent(body)
	if m.follow {
		m.vp.GotoBottom()
	}
}

func (m *model) wrap(s string) string {
	if s == "" || m.vp.Width <= 0 {
		return s
	}
	return lipgloss.NewStyle().Width(m.vp.Width).Render(s)
}

func (m *model) fillRule(label string) string {
	w := m.vp.Width
	if w <= 0 {
		return dividerStyle.Render(label)
	}
	if pad := w - lipgloss.Width(label); pad > 0 {
		label += strings.Repeat("─", pad)
	}
	return dividerStyle.Render(label)
}

// --- view -------------------------------------------------------------------

func (m model) View() string {
	if !m.ready {
		return "starting zot…"
	}
	return strings.Join([]string{
		m.titleBar(),
		m.metaBar(),
		"",
		m.vp.View(),
		m.footer(),
	}, "\n")
}

func (m model) titleBar() string {
	left := titleStyle.Render("✦ zot") + " " + m.badge()
	room := m.width - lipgloss.Width(left) - 2
	if room < 8 {
		return left
	}
	return left + " " + taskStyle.Render(truncate(m.task, room))
}

func (m model) badge() string {
	switch m.status {
	case statusDone:
		return statusDoneStyle.Render("✓ done")
	case statusFailed:
		return statusFailStyle.Render("✗ failed")
	default:
		// Keep the spinner and label as separate same-colour pieces: nesting the
		// spinner's own ANSI inside another style breaks the run of colour.
		return m.spinner.View() + statusRunningStyle.Render("working")
	}
}

func (m model) metaBar() string {
	seg := func(k, v string) string { return metaKey.Render(k+" ") + metaAccent.Render(v) }
	parts := []string{
		seg("backend", m.backend),
		seg("model", m.model),
		seg("dir", truncate(m.workdir, 36)),
		seg("iter", fmt.Sprintf("%d", m.iteration)),
		seg("tools", fmt.Sprintf("%d", m.toolCount)),
		seg("edits", fmt.Sprintf("%d", m.fileEdits)),
		seg("elapsed", fmtDuration(m.elapsed)),
	}
	line := strings.Join(parts, metaStyle.Render("  ·  "))
	return lipgloss.NewStyle().MaxWidth(m.width).Render(line)
}

func (m model) footer() string {
	hints := footerStyle.Render(
		keyHint.Render("↑/↓") + " scroll  " +
			keyHint.Render("g/G") + " top/bottom  " +
			keyHint.Render("q") + " quit",
	)
	if m.status == statusRunning {
		return hints
	}
	tail := footerStyle.Render("  ·  press " + keyHint.Render("q") + " to exit")
	return hints + tail
}

func fmtDuration(d time.Duration) string {
	d = d.Round(time.Second)
	return fmt.Sprintf("%02d:%02d", int(d.Minutes()), int(d.Seconds())%60)
}
