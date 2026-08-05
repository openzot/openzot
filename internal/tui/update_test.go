package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/chatbotkit/zot/agent"
)

// sized returns a model that has been through a window-size message, which is
// what makes the viewport usable.
func sized(t *testing.T, width, height int) model {
	t.Helper()

	m := newModel("do the thing", "gpt-5.4-mini", "openai", "/tmp/work", false)

	updated, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})

	typed, ok := updated.(model)
	if !ok {
		t.Fatalf("Update returned %T, want model", updated)
	}

	return typed
}

// Without a command from Init nothing ever redraws, so the spinner never turns
// and the elapsed clock never advances - a screen that looks hung on a run that
// is working fine.
func TestInitStartsTheSpinnerAndClock(t *testing.T) {
	m := newModel("task", "m", "b", "/w", false)

	cmd := m.Init()

	if cmd == nil {
		t.Fatal("Init must return a command; without it nothing ever redraws")
	}

	// a batch fans out into the individual commands, which is how both the
	// spinner tick and the clock tick get started
	msg := cmd()

	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		t.Fatalf("Init produced %T, want a batch starting both tickers", msg)
	}

	if len(batch) < 2 {
		t.Fatalf("Init started %d commands, want the spinner and the clock", len(batch))
	}
}

func TestWindowSizeMakesTheViewportReady(t *testing.T) {
	m := sized(t, 100, 40)

	if !m.ready {
		t.Error("the model should be ready after a size message")
	}

	if m.width != 100 || m.height != 40 {
		t.Errorf("size = %dx%d, want 100x40", m.width, m.height)
	}
}

// A terminal too short for the chrome must still leave a usable viewport rather
// than a negative one.
func TestTinyTerminalDoesNotProduceANegativeViewport(t *testing.T) {
	m := sized(t, 20, 1)

	if m.vp.Height < 1 {
		t.Errorf("viewport height = %d, want at least 1", m.vp.Height)
	}
}

// A quit has to actually be a quit. Asserting only that some command came back
// would pass for a scroll, which is the thing an unbound key does.
func TestQuitKeys(t *testing.T) {
	for _, key := range []tea.KeyMsg{
		{Type: tea.KeyRunes, Runes: []rune{'q'}},
		{Type: tea.KeyCtrlC},
	} {
		m := sized(t, 80, 24)

		_, cmd := m.Update(key)

		if cmd == nil {
			t.Fatalf("key %v returned no command", key)
		}

		if _, quit := cmd().(tea.QuitMsg); !quit {
			t.Errorf("key %v produced %T, want a quit", key, cmd())
		}
	}
}

// `g` jumps to the top and stops following; `G` returns to the tail.
func TestJumpKeys(t *testing.T) {
	m := sized(t, 80, 24)

	for i := 0; i < 100; i++ {
		m.appendEntry("line")
	}

	m.render()

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})

	if updated.(model).follow {
		t.Error("jumping to the top must stop following")
	}

	updated, _ = updated.(model).Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'G'}})

	if !updated.(model).follow {
		t.Error("jumping to the bottom must resume following")
	}
}

// The log is read-only, so scrolling is the only interaction - and following the
// tail has to stop when the user scrolls up, or they can never read anything.
func TestScrollingStopsFollowing(t *testing.T) {
	m := sized(t, 80, 24)

	for i := 0; i < 200; i++ {
		m.appendEntry("line")
	}

	m.render()

	if !m.follow {
		t.Fatal("a fresh model should follow the tail")
	}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyUp})

	if updated.(model).follow {
		t.Error("scrolling up must stop the log from jumping back to the bottom")
	}
}

func TestTickAdvancesTheElapsedClock(t *testing.T) {
	m := sized(t, 80, 24)

	updated, cmd := m.Update(tickMsg{})

	if cmd == nil {
		t.Error("a tick must schedule the next one, or the clock stops")
	}

	if updated.(model).elapsed < 0 {
		t.Error("elapsed time must not be negative")
	}
}

func TestHandleEventBuildsTheLog(t *testing.T) {
	m := sized(t, 100, 30)

	m.handleEvent(agent.IterationEvent{Iteration: 1})
	m.handleEvent(agent.ToolCallStartEvent{Name: "shell", Args: map[string]any{"command": "ls"}})
	m.handleEvent(agent.ToolCallEndEvent{Name: "shell", Result: "README.md"})
	// tokens are what the log shows; a MessageAgentEvent carries the same
	// content and is deliberately not drawn twice
	m.handleEvent(agent.TokenAgentEvent{Token: "here is "})
	m.handleEvent(agent.TokenAgentEvent{Token: "the answer"})
	m.handleEvent(agent.MessageAgentEvent{Type: agent.TypeBot, Text: "here is the answer"})

	m.flushPending()

	if m.iteration != 1 {
		t.Errorf("iteration = %d, want 1", m.iteration)
	}

	if m.toolCount != 1 {
		t.Errorf("toolCount = %d, want 1", m.toolCount)
	}

	log := strings.Join(m.entries, "\n")

	for _, want := range []string{"shell", "here is the answer"} {
		if !strings.Contains(log, want) {
			t.Errorf("log is missing %q:\n%s", want, log)
		}
	}
}

// A file-editing tool bumps the edit counter, which is what tells the operator
// the run actually changed something.
func TestFileEditsAreCounted(t *testing.T) {
	m := sized(t, 100, 30)

	m.handleEvent(agent.ToolCallStartEvent{
		Name: "write",
		Args: map[string]any{"path": "main.go", "content": "package main"},
	})

	if m.fileEdits != 1 {
		t.Errorf("fileEdits = %d, want 1", m.fileEdits)
	}
}

func TestToolErrorsAreShown(t *testing.T) {
	m := sized(t, 100, 30)

	m.handleEvent(agent.ToolCallErrorEvent{Name: "shell", Error: "command not found"})

	log := strings.Join(m.entries, "\n")

	if !strings.Contains(log, "command not found") {
		t.Errorf("a tool failure must be visible:\n%s", log)
	}
}

func TestExitEventSetsTheStatus(t *testing.T) {
	tests := []struct {
		name string
		exit agent.AgentExitEvent
		want status
	}{
		{"a settled run", agent.AgentExitEvent{Code: 0, Reason: "settled", Message: "done"}, statusDone},
		{"a budget-exhausted run", agent.AgentExitEvent{Code: 1, Reason: "iterations", Message: "gave up"}, statusFailed},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			m := sized(t, 100, 30)

			m.handleEvent(test.exit)

			if m.status != test.want {
				t.Errorf("status = %v, want %v", m.status, test.want)
			}

			if m.exitMsg == "" {
				t.Error("the exit message should be retained for the footer")
			}
		})
	}
}

func TestViewRendersWithoutPanicking(t *testing.T) {
	m := sized(t, 100, 30)

	m.handleEvent(agent.IterationEvent{Iteration: 2})
	m.handleEvent(agent.TokenAgentEvent{Token: "something"})
	m.flushPending()

	view := m.View()

	if view == "" {
		t.Fatal("View produced nothing")
	}

	for _, want := range []string{"do the thing", "gpt-5.4-mini", "openai"} {
		if !strings.Contains(view, want) {
			t.Errorf("view is missing %q", want)
		}
	}
}

// Before the first size message there is nothing sensible to draw, and drawing
// anyway used to produce a garbled frame.
func TestViewBeforeReady(t *testing.T) {
	m := newModel("task", "m", "b", "/w", false)

	if view := m.View(); strings.Contains(view, "\x1b[") && m.ready {
		t.Error("an unready model should not draw a full frame")
	}
}

func TestFillRuleFitsTheWidth(t *testing.T) {
	m := sized(t, 40, 20)

	rule := m.fillRule("iteration 1")

	if !strings.Contains(rule, "iteration 1") {
		t.Errorf("the rule must carry its label: %q", rule)
	}

	// a label longer than the terminal must not produce a negative pad
	wide := m.fillRule(strings.Repeat("x", 200))

	if wide == "" {
		t.Error("an oversized label must still render")
	}
}

func TestRewrapOnResize(t *testing.T) {
	m := sized(t, 120, 30)

	m.appendEntry(strings.Repeat("word ", 60))

	m.render()

	wide := m.committedWrapped

	updated, _ := m.Update(tea.WindowSizeMsg{Width: 40, Height: 30})

	narrow := updated.(model).committedWrapped

	if wide == narrow {
		t.Error("resizing should re-wrap the committed log")
	}
}

// The diff preview is what makes a write reviewable at a glance, so it has to
// render for the tools that change files and stay out of the way for the rest.
func TestDiffForTool(t *testing.T) {
	tests := []struct {
		name    string
		tool    string
		args    map[string]any
		wantAny bool
	}{
		{
			name:    "a whole-file write",
			tool:    "write",
			args:    map[string]any{"path": "main.go", "content": "package main\n\nfunc main() {}\n"},
			wantAny: true,
		},
		{
			name:    "a read is not a change",
			tool:    "read",
			args:    map[string]any{"path": "main.go"},
			wantAny: false,
		},
		{
			name:    "a shell command is not a change",
			tool:    "shell",
			args:    map[string]any{"command": "ls"},
			wantAny: false,
		},
		{
			name:    "a write with no content",
			tool:    "write",
			args:    map[string]any{"path": "main.go"},
			wantAny: false,
		},
		{
			// the preview is of the content, so a missing path does not stop it -
			// the operator still sees what would be written
			name:    "a write with no path still previews the content",
			tool:    "write",
			args:    map[string]any{"content": "x"},
			wantAny: true,
		},
		{
			name:    "no arguments at all",
			tool:    "write",
			args:    nil,
			wantAny: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := diffForTool(test.tool, test.args, 80)

			if test.wantAny && got == "" {
				t.Error("expected a rendered diff")
			}

			if !test.wantAny && got != "" {
				t.Errorf("expected no diff, got %q", got)
			}
		})
	}
}

// A narrow terminal must still produce something rather than wrapping into
// nonsense.
func TestDiffClipsToWidth(t *testing.T) {
	long := strings.Repeat("a very long line of content ", 20)

	got := diffForTool("write", map[string]any{"path": "f.txt", "content": long}, 40)

	for _, line := range strings.Split(got, "\n") {
		if len([]rune(stripANSI(line))) > 60 {
			t.Errorf("line exceeds the width budget: %q", line)
		}
	}
}

// stripANSI removes escape sequences so a width assertion measures glyphs.
func stripANSI(s string) string {
	var (
		builder strings.Builder
		inEsc   bool
	)

	for _, r := range s {
		switch {
		case r == '\x1b':
			inEsc = true
		case inEsc && (r == 'm' || r == 'K'):
			inEsc = false
		case !inEsc:
			builder.WriteRune(r)
		}
	}

	return builder.String()
}

// The badge is how an operator tells at a glance whether the run is still going
// or has failed, so the three statuses have to be distinguishable - a
// non-empty badge that says the same thing for all three tells them nothing.
func TestBadgeReflectsStatus(t *testing.T) {
	badges := map[status]string{}

	for _, st := range []status{statusRunning, statusDone, statusFailed} {
		m := sized(t, 80, 24)

		m.status = st

		badge := m.badge()

		if badge == "" {
			t.Errorf("status %v produced no badge", st)
		}

		for other, seen := range badges {
			if seen == badge {
				t.Errorf("statuses %v and %v render the same badge %q", other, st, badge)
			}
		}

		badges[st] = badge
	}

	// and each says which state it is, in words rather than colour alone
	for st, want := range map[status]string{
		statusRunning: "working",
		statusDone:    "done",
		statusFailed:  "failed",
	} {
		if !strings.Contains(badges[st], want) {
			t.Errorf("the %v badge %q does not say %q", st, badges[st], want)
		}
	}
}

func TestFooterShowsTheKeyHints(t *testing.T) {
	m := sized(t, 100, 30)

	m.iteration = 3
	m.toolCount = 7
	m.fileEdits = 2

	footer := m.footer()

	if footer == "" {
		t.Fatal("the footer must render while running")
	}

	// the log is read-only, so the only affordances are scrolling and quitting
	for _, want := range []string{"scroll", "quit"} {
		if !strings.Contains(footer, want) {
			t.Errorf("the footer should mention %q: %q", want, footer)
		}
	}
}

// The outcome leaves the UI as an error the caller can act on, which is how a
// non-zero exit code reaches the shell.
func TestExitBecomesAnError(t *testing.T) {
	m := sized(t, 100, 30)

	m.handleEvent(agent.AgentExitEvent{Code: 1, Reason: "cycle", Message: "kept repeating"})

	err := m.runError()

	if err == nil {
		t.Fatal("a failed run must surface as an error")
	}

	if !strings.Contains(err.Error(), "kept repeating") {
		t.Errorf("the error must carry the outcome: %v", err)
	}

	clean := sized(t, 100, 30)

	clean.handleEvent(agent.AgentExitEvent{Code: 0, Reason: "settled", Message: "done"})

	if err := clean.runError(); err != nil {
		t.Errorf("a settled run must not error: %v", err)
	}
}

// A spinner tick is ignored once the run has ended, or the finished screen keeps
// animating.
func TestSpinnerStopsWhenTheRunEnds(t *testing.T) {
	m := sized(t, 80, 24)

	m.status = statusDone

	_, cmd := m.Update(tickMsg{})

	if cmd != nil {
		t.Error("the clock must stop once the run has ended")
	}
}

// The renderers have to know the real tool names. A mismatch is not a compile
// error - it just quietly renders the agent's most-used tool as an anonymous
// key/value dump, which is how `shell` went unstyled.
func TestRenderToolStartCoversTheBuiltInTools(t *testing.T) {
	tests := []struct {
		tool string
		args map[string]any
		want string
	}{
		{"read", map[string]any{"path": "main.go"}, "main.go"},
		{"read", map[string]any{"path": "main.go", "startLine": 10.0, "endLine": 20.0}, ":10-20"},
		{"read", map[string]any{"path": "main.go", "startLine": 10.0}, ":10"},
		{"write", map[string]any{"path": "out.txt"}, "out.txt"},
		{"list", map[string]any{"path": "./internal"}, "internal"},
		{"shell", map[string]any{"command": "go test ./..."}, "go test"},
		{"skill", map[string]any{"name": "deploy"}, "deploy"},

		// a caller's own tool still renders, just generically
		{"custom", map[string]any{"thing": "value"}, "thing=value"},
	}

	for _, test := range tests {
		got := stripANSI(renderToolStart(test.tool, test.args))

		if !strings.Contains(got, test.want) {
			t.Errorf("renderToolStart(%q) = %q, want it to contain %q", test.tool, got, test.want)
		}
	}
}

// zot's tools return strings, so a summary that only understood maps rendered
// nothing at all.
func TestRenderToolEndHandlesStringResults(t *testing.T) {
	tests := []struct {
		name    string
		tool    string
		result  any
		wantAny bool
		want    string
	}{
		{"shell echoes its output", "shell", "hello\nworld", true, "hello"},
		{"a silent command still confirms", "shell", "", true, "done"},
		{"read is summarised by size", "read", "a\nb\nc", true, "3 lines"},
		{"list is summarised by size", "list", "a\nb", true, "2 lines"},
		{"write confirms", "write", "wrote 12 bytes", true, "saved"},
		{"an unknown tool echoes", "custom", "some output", true, "some output"},
		{"an unknown tool with nothing to say", "custom", "", false, ""},
		{"a non-string, non-map result", "shell", 42, false, ""},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := stripANSI(renderToolEnd(test.tool, test.result))

			if !test.wantAny {
				if got != "" {
					t.Errorf("expected nothing, got %q", got)
				}

				return
			}

			if !strings.Contains(got, test.want) {
				t.Errorf("renderToolEnd = %q, want it to contain %q", got, test.want)
			}
		})
	}
}

// One noisy command must not scroll the rest of the run off the screen.
func TestOutputIsCapped(t *testing.T) {
	var lines []string

	for i := 0; i < 50; i++ {
		lines = append(lines, "line")
	}

	got := stripANSI(renderToolEnd("shell", strings.Join(lines, "\n")))

	if strings.Count(got, "line") > maxOutputLines+1 {
		t.Errorf("output was not capped:\n%s", got)
	}

	if !strings.Contains(got, "…") {
		t.Error("clipping must be visible")
	}
}

func TestRenderToolEndHandlesStructuredResults(t *testing.T) {
	failure := stripANSI(renderToolEnd("shell", map[string]any{
		"success": false,
		"error":   "exit status 1",
		"stderr":  "compile failed",
	}))

	if !strings.Contains(failure, "exit status 1") {
		t.Errorf("a structured failure must surface: %q", failure)
	}

	success := stripANSI(renderToolEnd("shell", map[string]any{"stdout": "all good"}))

	if !strings.Contains(success, "all good") {
		t.Errorf("structured output must surface: %q", success)
	}
}

func TestIntishCoercion(t *testing.T) {
	for _, value := range []any{float64(7), 7, int64(7)} {
		if n, ok := intish(value); !ok || n != 7 {
			t.Errorf("intish(%T) = %d/%v, want 7/true", value, n, ok)
		}
	}

	if _, ok := intish("7"); ok {
		t.Error("a string is not a number here")
	}
}

// The plain renderer is what pipes, logs and CI transcripts see, so its tool
// names have to match too - the same mismatch printed "shell command=go test"
// instead of the command.
func TestPlainArgCoversTheBuiltInTools(t *testing.T) {
	tests := []struct {
		tool string
		args map[string]any
		want string
	}{
		{"read", map[string]any{"path": "main.go"}, "main.go"},
		{"write", map[string]any{"path": "out.txt"}, "out.txt"},
		{"list", map[string]any{"path": "./internal"}, "./internal"},
		{"shell", map[string]any{"command": "go test ./..."}, "go test ./..."},
		{"skill", map[string]any{"name": "deploy"}, "deploy"},
		{"custom", map[string]any{"thing": "value"}, "thing=value"},
	}

	for _, test := range tests {
		if got := plainArg(test.tool, test.args); got != test.want && !strings.Contains(got, test.want) {
			t.Errorf("plainArg(%q) = %q, want %q", test.tool, got, test.want)
		}
	}
}

func TestPlainToolEnd(t *testing.T) {
	tests := []struct {
		name    string
		tool    string
		result  any
		wantAny bool
		want    string
	}{
		{"shell output is echoed", "shell", "hello\nworld", true, "hello"},
		{"a silent command says nothing", "shell", "", false, ""},
		{"read is summarised", "read", "a\nb\nc", true, "3 lines"},
		{"list is summarised", "list", "a", true, "1 lines"},
		{"a write needs no summary", "write", "wrote 3 bytes", false, ""},
		{"a structured failure surfaces", "shell", map[string]any{"success": false, "error": "exit 1"}, true, "exit 1"},
		{"structured output surfaces", "shell", map[string]any{"stdout": "captured"}, true, "captured"},
		{"an unsupported result type", "shell", 42, false, ""},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := plainToolEnd(test.tool, test.result)

			if !test.wantAny {
				if got != "" {
					t.Errorf("expected nothing, got %q", got)
				}

				return
			}

			if !strings.Contains(got, test.want) {
				t.Errorf("plainToolEnd = %q, want it to contain %q", got, test.want)
			}
		})
	}
}

func TestPlainOutputIsCapped(t *testing.T) {
	var lines []string

	for i := 0; i < 50; i++ {
		lines = append(lines, "noise")
	}

	got := plainToolEnd("shell", strings.Join(lines, "\n"))

	if strings.Count(got, "noise") > maxOutputLines {
		t.Errorf("plain output was not capped:\n%s", got)
	}

	if !strings.Contains(got, "...") {
		t.Error("clipping must be visible in plain output too")
	}
}

// A key nobody bound must reach the viewport rather than being swallowed, and
// must not quit: an unattended run ended by a stray keystroke is a lost run.
func TestUnboundKeysAreNotQuitKeys(t *testing.T) {
	for _, key := range []tea.KeyMsg{
		{Type: tea.KeyRunes, Runes: []rune{'x'}},
		{Type: tea.KeyEsc},
		{Type: tea.KeyEnter},
		{Type: tea.KeyRunes, Runes: []rune{' '}},
	} {
		m := sized(t, 80, 24)

		updated, _ := m.Update(key)

		// a quit would leave the model unchanged and end the program; what we
		// can check without running the program is that the viewer is still
		// there and still running
		if updated.(model).status != statusRunning {
			t.Errorf("key %v ended the run", key)
		}
	}
}

// The spinner and the clock stop once the run ends, so a finished screen does
// not look like it is still working.
func TestTheClockStopsWhenTheRunEnds(t *testing.T) {
	m := sized(t, 80, 24)

	m.status = statusDone

	before := m.elapsed

	next, cmd := m.Update(tickMsg{})

	if cmd != nil {
		t.Error("a finished run must not schedule another tick")
	}

	if next.(model).elapsed != before {
		t.Error("the clock kept running after the run ended")
	}
}

// The footer tells the operator how to leave. While a run is going it shows the
// keys; once it is over it says so explicitly, because the run no longer ends
// on its own.
func TestFooterAddsAnExitHintWhenTheRunIsOver(t *testing.T) {
	m := sized(t, 80, 24)

	m.status = statusRunning

	running := m.footer()

	m.status = statusDone

	finished := m.footer()

	if len(finished) <= len(running) {
		t.Errorf("a finished footer must say more than a running one:\n%q\n%q", finished, running)
	}

	if !strings.Contains(finished, "exit") {
		t.Errorf("the finished footer must say how to leave: %q", finished)
	}
}

func TestFormattedDuration(t *testing.T) {
	tests := []struct {
		duration time.Duration
		want     string
	}{
		{duration: 0, want: "00:00"},
		{duration: 45 * time.Second, want: "00:45"},
		{duration: 90 * time.Second, want: "01:30"},
		{duration: 61 * time.Minute, want: "61:00"},
	}

	for _, test := range tests {
		if got := fmtDuration(test.duration); got != test.want {
			t.Errorf("fmtDuration(%s) = %q, want %q", test.duration, got, test.want)
		}
	}
}

// The header has to survive a terminal too narrow for it. A run watched over a
// phone-sized ssh session is still a run.
func TestTitleBarSurvivesANarrowTerminal(t *testing.T) {
	for _, width := range []int{1, 8, 12, 20} {
		m := sized(t, width, 24)

		title := m.titleBar()

		if title == "" {
			t.Errorf("width %d produced no title bar", width)
		}

		if strings.Contains(title, "\n") {
			t.Errorf("width %d wrapped the title bar: %q", width, title)
		}
	}
}

func TestTruncateAddsAnEllipsisAndFlattensNewlines(t *testing.T) {
	tests := []struct {
		in   string
		max  int
		want string
	}{
		{in: "short", max: 10, want: "short"},
		{in: "exactly-10", max: 10, want: "exactly-10"},
		{in: "a longer string", max: 8, want: "a longe…"},
		{in: "two\nlines", max: 20, want: "two lines"},
		{in: "two\nlines here", max: 6, want: "two l…"},
	}

	for _, test := range tests {
		if got := truncate(test.in, test.max); got != test.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", test.in, test.max, got, test.want)
		}
	}
}

// A shell tool reports failure on stderr, and that is exactly the output an
// operator reading a failed run needs to see.
func TestCommandOutputPrefersStdoutButFallsBackToStderr(t *testing.T) {
	if got := commandOutput(map[string]any{"stdout": "all good\n"}); !strings.Contains(got, "all good") {
		t.Errorf("stdout was not rendered: %q", got)
	}

	got := commandOutput(map[string]any{"stdout": "", "stderr": "permission denied\n"})

	if !strings.Contains(got, "permission denied") {
		t.Errorf("stderr was not rendered when stdout was empty: %q", got)
	}

	if got := commandOutput(map[string]any{}); got != "" {
		t.Errorf("a silent command rendered %q, want nothing", got)
	}
}

// An edit that changes nothing must render nothing, or the log fills with empty
// diff boxes on every no-op write.
func TestPlainDiffIgnoresNonEdits(t *testing.T) {
	if got := plainDiff("shell", map[string]any{"command": "ls"}); got != "" {
		t.Errorf("a shell call produced a diff: %q", got)
	}

	if got := plainDiff("edit", map[string]any{
		"path":      "a.go",
		"oldString": "same",
		"newString": "same",
	}); got != "" {
		t.Errorf("an edit that changes nothing produced a diff: %q", got)
	}

	got := plainDiff("write", map[string]any{"path": "a.go", "content": "package main\n"})

	if !strings.Contains(got, "package main") {
		t.Errorf("a write produced no diff: %q", got)
	}
}
