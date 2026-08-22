package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/openzot/openzot/agent"
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
	appName  string
	task     string
	title    string // shown instead of task when set - see tui.Meta.Title
	model    string
	provider string
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
	follow           bool     // auto-scroll to the newest activity
	truncated        bool     // oldest lines have been dropped to bound memory
	maxEntries       int      // scrollback cap (DefaultMaxScrollback unless overridden)
	stats            []string // header fields to show, in order (DefaultStats when empty)

	// Task progress, read off the agent's own plan and progress calls: how many
	// steps it laid out, and how many it has since reported finished. The model
	// is the only thing that knows what "done" means for its plan, so this is
	// its claim rather than a measurement - which is why it is shown as its own
	// stat and never mixed into a limit-style budget.
	planSteps int
	stepsDone int

	// Where this run sits in a batch (order 2 of 5), for a stat that says how
	// much of the queue is left rather than how much of one order is.
	batchIndex int
	batchSize  int

	status     status
	iteration  int
	toolCount  int
	fileEdits  int
	exitCode   int
	exitReason string
	exitMsg    string
	err        error

	// quitOnDone closes the viewer as soon as the run ends (see
	// Meta.QuitOnDone); the default holds the final screen for review.
	quitOnDone bool

	// Provider-reported cumulative token usage (not a local estimate).
	inputTokens  int
	outputTokens int

	// Configured limits, for the "5/1000" progress display. Zero means the limit
	// is unbounded (or the caller chose not to show it), so no denominator shows.
	maxIterations int
	maxCalls      int
	maxDuration   time.Duration

	startedAt time.Time
	elapsed   time.Duration
}

func newModel(appName, task, modelName, provider, workdir string, showDiff bool) model {
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(colYellow)

	return model{
		appName:    appName,
		task:       task,
		model:      modelName,
		provider:   provider,
		workdir:    workdir,
		showDiff:   showDiff,
		spinner:    sp,
		status:     statusRunning,
		follow:     true,
		startedAt:  time.Now(),
		maxEntries: DefaultMaxScrollback,
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
		return m, m.maybeQuit()

	case agentErrMsg:
		// The terminal error arrives after the exit event, which has already
		// flipped the status - it must still be kept and shown, because it is
		// usually the run's only diagnostic: "the provider failed" on screen
		// with the actual 404 dropped on the floor was how that got lost. A
		// run that ended in success is the exception - a late error must not
		// resurrect a finished run as a failure.
		if m.err == nil && msg.err != nil && m.status != statusDone {
			m.err = msg.err
			m.flushPending()
			m.appendEntry(errStyle.Render("✗ " + msg.err.Error()))
		}
		if m.status == statusRunning {
			m.status = statusFailed
		}
		return m, m.maybeQuit()

	case agentDoneMsg:
		// The stream ended without an explicit exit (e.g. iteration cap reached).
		if m.status == statusRunning {
			m.status = statusFailed
			m.err = fmt.Errorf("agent stream ended without an exit")
			m.flushPending()
			m.appendEntry(dividerStyle.Render("- stream ended -"))
		}
		return m, m.maybeQuit()
	}

	return m, nil
}

// maybeQuit ends the program once the run has ended, when the caller asked for
// that instead of a held final screen.
func (m model) maybeQuit() tea.Cmd {
	if m.quitOnDone && m.status != statusRunning {
		return tea.Quit
	}

	return nil
}

// handleEvent folds one agent event into the UI state.
func (m *model) handleEvent(ev agent.AgentEvent) {
	switch e := ev.(type) {
	case agent.IterationEvent:
		m.iteration = e.Iteration
		m.flushPending()
		// A fixed short rule: one that fills the width would wrap at a narrow
		// terminal and smear the divider across two rows.
		m.appendEntry(dividerStyle.Render(fmt.Sprintf("─── iteration %d ───", e.Iteration)))

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
		m.toolCount++
		if e.Name == "write" || e.Name == "edit" {
			m.fileEdits++
		}

		m.trackProgress(e.Name, e.Args)
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

	case agent.NoticeEvent:
		// a corrective nudge - an empty turn, a truncation continuation, a
		// settle reminder; without this line the recovery renders as bare
		// iteration dividers, indistinguishable from a hang
		m.flushPending()
		m.appendEntry(statusRunningStyle.Render("⚠ ") + metaStyle.Render(e.Text))

	case agent.RetryEvent:
		// a retried provider failure spends a continuation and then waits out a
		// backoff; without this line the wait renders as empty iterations
		// stacking up - a run that is surviving looks like one that is hanging
		m.flushPending()
		m.appendEntry(statusRunningStyle.Render("↻ retrying") + "  " + metaStyle.Render(e.Error))

	case agent.CompactionEvent:
		// compaction rewrites the conversation and spends a model call; show it so a
		// long run's context management is visible rather than silent
		m.flushPending()
		m.appendEntry(dividerStyle.Render("⤿ " + e.Detail))

	case agent.UsageEvent:
		// provider-reported cumulative token usage, shown in the meta bar
		m.inputTokens = e.InputTokens
		m.outputTokens = e.OutputTokens

	case agent.AgentExitEvent:
		m.exitCode = e.Code
		m.exitReason = e.Reason
		m.exitMsg = e.Message
		m.flushPending()
		switch {
		case e.Code == 0:
			m.status = statusDone
			m.appendEntry("\n" + okStyle.Render("✓ done") + "  " + taskStyle.Render(e.Message))

		case e.Reason == agent.ReasonFailed:
			// the model reached a conclusion and the conclusion is "no" - an
			// outcome, not a malfunction, so it does not get a process exit code
			m.status = statusFailed
			m.appendEntry("\n" + errStyle.Render("✗ failed") + "  " + taskStyle.Render(e.Message))

		default:
			m.status = statusFailed
			m.appendEntry("\n" + errStyle.Render(fmt.Sprintf("✗ exited (code %d)", e.Code)) + "  " + taskStyle.Render(e.Message))
		}
	}
}

// --- viewport content management --------------------------------------------

// DefaultMaxScrollback is the on-screen log cap used when a caller does not set
// its own (Meta.MaxScrollback). An autonomous run can emit an unbounded number of
// events (millions of iterations, unbounded tool calls), so keeping every line
// would grow the viewer's memory without limit; once the cap is reached the
// oldest lines are dropped, and the full untrimmed run is always in the session
// log on disk. A caller that wants to keep more on screen raises the cap.
const DefaultMaxScrollback = 5000

func (m *model) appendEntry(s string) {
	m.entries = append(m.entries, s)

	// The buffer may grow a quarter past the cap before trimming, so the (linear)
	// re-wrap a trim costs is amortised over many appends rather than paid on every
	// append once the cap is reached.
	slack := m.maxEntries / 4

	if len(m.entries) > m.maxEntries+slack {
		// Keep the most recent m.maxEntries, copied into a fresh slice so the old
		// backing array is released rather than retained behind a reslice, then
		// rebuild the wrapped cache from the trimmed set.
		kept := make([]string, m.maxEntries)
		copy(kept, m.entries[len(m.entries)-m.maxEntries:])
		m.entries = kept
		m.truncated = true
		m.rewrap()

		return
	}

	// Append only the newly wrapped entry to the cache. Wrapping each entry to the
	// viewport width is equivalent to wrapping the whole joined buffer - the wrap
	// is per line - so per-append cost stays independent of how long the run has
	// been, instead of re-wrapping the entire history on every line.
	wrapped := m.wrap(s)
	if m.committedWrapped == "" {
		m.committedWrapped = wrapped
	} else {
		m.committedWrapped += "\n" + wrapped
	}

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
	if m.truncated {
		marker := m.wrap(dividerStyle.Render("  ⋮ earlier activity trimmed — the full run is in the session log"))
		if body != "" {
			body = marker + "\n" + body
		} else {
			body = marker
		}
	}
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

// --- view -------------------------------------------------------------------

func (m model) View() string {
	if !m.ready {
		return "starting " + m.appName + "…"
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
	left := titleStyle.Render("✦ "+m.appName) + " " + m.badge()
	room := m.width - lipgloss.Width(left) - 2
	if room < 8 {
		return left
	}
	// A title is what the header wants: the task is the whole order rendered
	// for the model, so a one-line header of it is a paragraph cut mid-word.
	label := m.title
	if label == "" {
		label = m.task
	}

	return left + " " + taskStyle.Render(truncate(label, room))
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

// trackProgress reads task progress out of the agent's reflective tools. A new
// plan replaces the old one whole - a replanned run is a different task, and
// carrying the previous step count over would show progress against a plan that
// no longer exists - and it resets what is done, because the completed steps
// were steps of the plan being abandoned.
//
// The completed list is a set of names, so its length is the count; a progress
// call that lists more done than were planned means the model outgrew its own
// plan, and the count follows it rather than pretending the plan was right.
func (m *model) trackProgress(name string, args map[string]any) {
	switch name {
	case "plan":
		if steps := strList(args, "steps"); len(steps) > 0 {
			m.planSteps = len(steps)
			m.stepsDone = 0
		}

	case "progress":
		m.stepsDone = len(strList(args, "completed"))

		if m.stepsDone > m.planSteps {
			m.planSteps = m.stepsDone
		}
	}
}

// tokensPerSecond is the run's output throughput: generated tokens over wall
// time. Output rather than total, because that is the number a provider's
// throughput actually varies in and the one a watcher recognises as fast or
// slow. Zero until there is enough of a run to divide by.
func (m model) tokensPerSecond() float64 {
	if m.elapsed <= 0 || m.outputTokens <= 0 {
		return 0
	}

	return float64(m.outputTokens) / m.elapsed.Seconds()
}

// perIteration is the average wall time of one agentic round. Where tps says
// how fast the model writes, this says how long a whole think-act-observe cycle
// takes - the number that actually predicts when a long run will finish, since
// tool calls and not tokens are usually what a slow round is made of.
func (m model) perIteration() time.Duration {
	if m.iteration <= 0 || m.elapsed <= 0 {
		return 0
	}

	return m.elapsed / time.Duration(m.iteration)
}

// KnownStats is every field the header meta bar can show. A caller's stat list
// (Meta.Stats / ui.stats) is validated against it, and new stats are added here
// as they arrive.
var KnownStats = []string{
	"provider", "model", "dir", "iter", "tools", "edits", "elapsed", "tokens",
	"tps", "pace", "task", "order",
}

// DefaultStats is the field set and order used when no stats are configured.
//
// Not everything renderable belongs here. The bar is one line and drops what
// does not fit, so each default costs the ones after it: a stat earns its place
// by telling the watcher something they would act on. "tools" is a cumulative
// count of calls - it climbs on every run and says nothing about whether this
// one is going well - so it is available but off. The rate stats are the
// opposite of cumulative and answer "is this run healthy", so they are on.
//
// Order is load-bearing for the same reason: the bar keeps the segments that
// fit and drops the rest, so what is listed first is what survives a narrow
// terminal. "dir" is last despite being useful because it never changes: a
// static path is not worth the live stats it would push off the end.
var DefaultStats = []string{
	"provider", "model", "task", "order", "iter", "edits", "elapsed", "tps", "pace", "tokens", "dir",
}

// IsKnownStat reports whether name is a renderable meta-bar field.
func IsKnownStat(name string) bool {
	for _, k := range KnownStats {
		if k == name {
			return true
		}
	}

	return false
}

func (m model) metaBar() string {
	seg := func(k, v string, value lipgloss.Style) string {
		return metaKey.Render(k+" ") + value.Render(v)
	}

	// counted renders "n" or "n/max" when a limit is set, so progress against a
	// configured budget is visible.
	counted := func(n, max int) string {
		if max > 0 {
			return fmt.Sprintf("%d/%d", n, max)
		}

		return fmt.Sprintf("%d", n)
	}

	elapsed := fmtDuration(m.elapsed)
	if m.maxDuration > 0 {
		elapsed += "/" + fmtDuration(m.maxDuration)
	}

	// Every renderable field, keyed by its stat name. The keys must match
	// KnownStats (a test guards this).
	segments := map[string]string{
		"provider": seg("provider", m.provider, metaProvider),
		"model":    seg("model", m.model, metaModel),
		"dir":      seg("dir", shortPath(m.workdir, 28), metaStyle),
		"iter":     seg("iter", counted(m.iteration, m.maxIterations), metaCount),
		"tools":    seg("tools", counted(m.toolCount, m.maxCalls), metaTools),
		"edits":    seg("edits", fmt.Sprintf("%d", m.fileEdits), metaEdits),
		"elapsed":  seg("elapsed", elapsed, metaStyle),
		"tokens":   seg("tokens", fmt.Sprintf("↑%s ↓%s", fmtTokens(m.inputTokens), fmtTokens(m.outputTokens)), metaModel),
		"tps":      seg("tps", fmtRate(m.tokensPerSecond()), metaModel),
		"pace":     seg("pace", fmtPace(m.perIteration()), metaCount),
		"task":     seg("task", fmtProgress(m.stepsDone, m.planSteps), metaCount),
		"order":    seg("order", fmtProgress(m.batchIndex, m.batchSize), metaCount),
	}

	fields := m.stats
	if len(fields) == 0 {
		fields = DefaultStats
	}

	// A segment is shown whole or not at all. Clipping the line to the terminal
	// width left whichever segment straddled the edge half-rendered - "elap",
	// "tok" - which reads as a broken UI rather than a narrow one, and a
	// half-written number is worse than no number: it can be misread. So the
	// bar takes segments in the configured order for as long as they fit and
	// stops at the first that does not, giving a prefix that grows and shrinks
	// predictably as the terminal is resized.
	separator := metaStyle.Render("  ·  ")
	separatorWidth := lipgloss.Width(separator)

	parts := make([]string, 0, len(fields))
	used := 0

	for _, name := range fields {
		segment, ok := segments[name]
		if !ok {
			continue
		}

		needed := lipgloss.Width(segment)
		if len(parts) > 0 {
			needed += separatorWidth
		}

		// width is zero until the first WindowSizeMsg arrives; there is no
		// terminal to fit yet, so nothing is dropped for not fitting it.
		if m.width > 0 && used+needed > m.width {
			break
		}

		parts = append(parts, segment)
		used += needed
	}

	line := strings.Join(parts, separator)

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

// fmtTokens renders a token count compactly: 532, 45.2k, 1.2M.
func fmtTokens(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

func fmtDuration(d time.Duration) string {
	d = d.Round(time.Second)
	return fmt.Sprintf("%02d:%02d", int(d.Minutes()), int(d.Seconds())%60)
}

// fmtRate renders a tokens-per-second figure. A rate nobody can compute yet -
// no elapsed time, no tokens - shows as "-" rather than "0.0", because zero is
// a measurement and this is the absence of one.
func fmtRate(perSecond float64) string {
	if perSecond <= 0 {
		return "-"
	}

	if perSecond >= 100 {
		return fmt.Sprintf("%.0f/s", perSecond)
	}

	return fmt.Sprintf("%.1f/s", perSecond)
}

// fmtPace renders an average time per iteration. Sub-minute rounds are the
// normal case and read better in seconds than as 00:07, and here too an
// unmeasured pace is "-" rather than a confident zero.
func fmtPace(d time.Duration) string {
	switch {
	case d <= 0:
		return "-"
	case d < time.Minute:
		return fmt.Sprintf("%.1fs", d.Seconds())
	default:
		return fmtDuration(d)
	}
}

// fmtProgress renders "done/total", or "-" when there is no total to be a
// fraction of. A run whose model has not planned yet, and a single order that
// is not part of a batch, both have nothing to report - and "0/0" reads as a
// measurement of nothing rather than the absence of one.
func fmtProgress(done, total int) string {
	if total <= 0 {
		return "-"
	}

	return fmt.Sprintf("%d/%d", done, total)
}
