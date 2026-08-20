package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/openzot/openzot/agent"
)

// sized returns a model that has been through a window-size message, which is
// what makes the viewport usable.
func sized(t *testing.T, width, height int) model {
	t.Helper()

	m := newModel("zot", "do the thing", "gpt-5.4-mini", "openai", "/tmp/work", false)

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
	m := newModel("zot", "task", "m", "b", "/w", false)

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

// A compaction rewrites the conversation and spends a model call, so it must be
// visible in the log rather than happening silently.
func TestHandleEventShowsCompaction(t *testing.T) {
	m := sized(t, 100, 30)

	m.handleEvent(agent.CompactionEvent{Detail: "compacted 30 earlier messages into a checkpoint"})

	if log := strings.Join(m.entries, "\n"); !strings.Contains(log, "compacted 30 earlier messages") {
		t.Errorf("a compaction must appear in the log:\n%s", log)
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
		{"a run the model declared failed", agent.AgentExitEvent{Code: 1, Reason: "failed", Message: "cannot reach the host"}, statusFailed},
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

// A run the model declared a failure is not a crash and not a budget cut: it
// reached a conclusion. Reporting it as "exited (code 1)" reads as a harness
// malfunction, which sends the operator looking in the wrong place.
func TestDeclaredFailureRendersAsAnOutcomeNotACrash(t *testing.T) {
	m := sized(t, 100, 30)

	m.handleEvent(agent.AgentExitEvent{Code: 1, Reason: "failed", Message: "cannot reach the host"})

	log := stripANSI(strings.Join(m.entries, "\n"))

	if !strings.Contains(log, "cannot reach the host") {
		t.Errorf("log %q should carry the model's stated reason", log)
	}

	if strings.Contains(log, "code 1") {
		t.Errorf("log %q reports a declared failure as a process exit code", log)
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
	m := newModel("zot", "task", "m", "b", "/w", false)

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

		// edit has a diff panel (see diffForTool) but had no header of its own,
		// so it fell through to the generic branch and dumped both versions of
		// the file inline, directly above the diff that renders them properly
		{"edit", map[string]any{"path": "main.go", "oldString": "before", "newString": "after"}, "main.go"},

		// a caller's own tool still renders, just generically
		{"custom", map[string]any{"thing": "value"}, "thing=value"},
	}

	for _, test := range tests {
		got := stripANSI(renderToolStart(test.tool, test.args))

		if !strings.Contains(got, test.want) {
			t.Errorf("renderToolStart(%q) = %q, want it to contain %q", test.tool, got, test.want)
		}
	}

	// the file's contents belong in the diff panel, not in the header line
	header := stripANSI(renderToolStart("edit", map[string]any{
		"path": "main.go", "oldString": "the whole previous file", "newString": "the whole new file",
	}))

	if strings.Contains(header, "the whole previous file") || strings.Contains(header, "the whole new file") {
		t.Errorf("the edit header dumped the file contents: %q", header)
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

		// a task or tool argument in CJK or emoji was cut mid-rune, so the meta
		// bar and the tool lines rendered a replacement character - and the cap
		// counted bytes, so the line was cut far short of the width it was given
		{in: "日本語のタスク説明文です", max: 6, want: "日本語のタ…"},
		{in: "🚀🚀🚀🚀🚀", max: 3, want: "🚀🚀…"},
		{in: "日本語", max: 10, want: "日本語"},
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

// The plan and progress renderers are the reason these tools exist in the
// viewer: an operator watching a long run reads the plan to judge the approach
// and the progress to see where it is. These pin that the structure survives
// into the rendered lines, not just the tool name.
func TestRenderPlanShowsNumberedSteps(t *testing.T) {
	out := stripANSI(renderToolStart("plan", map[string]interface{}{
		"steps":     []interface{}{"read the handler", "add validation", "write a test"},
		"rationale": "smallest safe change first",
	}))

	for _, want := range []string{"plan", "smallest safe change first", "1. read the handler", "2. add validation", "3. write a test"} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered plan missing %q:\n%s", want, out)
		}
	}
}

func TestRenderProgressShowsStatus(t *testing.T) {
	out := stripANSI(renderToolStart("progress", map[string]interface{}{
		"current":   "adding validation",
		"completed": []interface{}{"read the handler"},
		"blockers":  []interface{}{"missing a fixture"},
		"nextSteps": []interface{}{"write a test"},
	}))

	for _, want := range []string{"adding validation", "done", "read the handler", "blocked", "missing a fixture", "next", "write a test"} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered progress missing %q:\n%s", want, out)
		}
	}
}

// A plan with no steps still renders its header rather than crashing, and a
// model that emits a non-string step must not break the render.
func TestPlanRenderingIsRobust(t *testing.T) {
	if out := stripANSI(renderToolStart("plan", map[string]interface{}{})); !strings.Contains(out, "plan") {
		t.Errorf("a stepless plan should still render a header: %q", out)
	}

	// strList drops the non-string entry rather than panicking
	if got := strList(map[string]interface{}{"steps": []interface{}{"ok", 42, nil, "also ok"}}, "steps"); len(got) != 2 {
		t.Errorf("strList should keep only the strings, got %v", got)
	}

	if got := strList(map[string]interface{}{"steps": "not an array"}, "steps"); got != nil {
		t.Errorf("strList on a non-array should be nil, got %v", got)
	}
}

// The plain (non-TTY) renderers surface the same structure for a piped run.
func TestPlainPlanAndProgress(t *testing.T) {
	plan := plainArg("plan", map[string]interface{}{
		"steps":     []interface{}{"step one", "step two"},
		"rationale": "because",
	})
	for _, want := range []string{"because", "1. step one", "2. step two"} {
		if !strings.Contains(plan, want) {
			t.Errorf("plain plan missing %q:\n%s", want, plan)
		}
	}

	progress := plainArg("progress", map[string]interface{}{
		"current":  "working",
		"blockers": []interface{}{"a blocker"},
	})
	for _, want := range []string{"working", "blocked", "a blocker"} {
		if !strings.Contains(progress, want) {
			t.Errorf("plain progress missing %q:\n%s", want, progress)
		}
	}
}

// A long autonomous run can emit far more lines than anyone scrolls through, so
// the viewer must bound its scrollback rather than grow memory without limit.
// The full run stays in the session log.
func TestActivityLogIsBoundedForLongRuns(t *testing.T) {
	// a not-yet-sized model: render() no-ops, so this exercises the scrollback cap
	// without the per-append viewport cost (which is what a real, model-paced run
	// pays anyway, now bounded to the cap)
	m := newModel("zot", "do the thing", "m", "b", "d", false)

	limit := m.maxEntries          // DefaultMaxScrollback
	total := limit + limit/4 + 200 // enough to force a trim past the cap + slack
	for i := 0; i < total; i++ {
		m.appendEntry(fmt.Sprintf("line %d", i))
	}

	// bounded: never more than the cap plus the trim slack, whatever the run length
	if len(m.entries) > limit+limit/4 {
		t.Fatalf("scrollback must stay bounded, got %d entries", len(m.entries))
	}

	if !m.truncated {
		t.Error("truncation must be flagged once the cap is exceeded")
	}

	// the newest line always survives
	if got := m.entries[len(m.entries)-1]; got != fmt.Sprintf("line %d", total-1) {
		t.Errorf("the most recent line must be kept, got %q", got)
	}

	// the oldest kept line is exactly total - len(entries), and older ones are gone
	oldest := total - len(m.entries)
	if got := m.entries[0]; got != fmt.Sprintf("line %d", oldest) {
		t.Errorf("the oldest kept line should be line %d, got %q", oldest, got)
	}
}

// When the log has been trimmed, the viewer must say so and point at the session
// log, so a watcher knows the on-screen history is not the whole run.
func TestTrimmedLogShowsAMarker(t *testing.T) {
	m := sized(t, 100, 30)
	m.truncated = true
	m.appendEntry("a recent line") // triggers a render

	if !strings.Contains(m.vp.View(), "trimmed") {
		t.Errorf("a trimmed log must show a marker, got:\n%s", m.vp.View())
	}
}

// The scrollback cap is configurable (Meta.MaxScrollback / ui.scrollback): a
// caller can keep fewer or more lines than the default.
func TestScrollbackCapIsConfigurable(t *testing.T) {
	m := newModel("zot", "t", "m", "b", "d", false)
	m.maxEntries = 50 // what Run sets from Meta.MaxScrollback

	for i := 0; i < 300; i++ {
		m.appendEntry(fmt.Sprintf("line %d", i))
	}

	if len(m.entries) > m.maxEntries+m.maxEntries/4 {
		t.Errorf("a custom cap of %d must be honoured, kept %d", m.maxEntries, len(m.entries))
	}

	if len(m.entries) < m.maxEntries {
		t.Errorf("should keep about the cap %d, kept only %d", m.maxEntries, len(m.entries))
	}
}

// The header shows only the configured fields, in the configured order.
func TestMetaBarRendersConfiguredFieldsInOrder(t *testing.T) {
	m := sized(t, 200, 30)
	m.stats = []string{"iter", "model"} // a reversed subset
	m.model = "glm-5.2"
	m.iteration = 5

	bar := m.metaBar()

	if strings.Contains(bar, "provider") || strings.Contains(bar, "elapsed") {
		t.Errorf("unconfigured fields must not show: %q", bar)
	}

	if i, j := strings.Index(bar, "iter"), strings.Index(bar, "model"); i < 0 || j < 0 || i > j {
		t.Errorf("fields must appear in the configured order (iter then model): %q", bar)
	}
}

// An empty stat list falls back to the default set.
func TestMetaBarDefaultsWhenUnset(t *testing.T) {
	m := sized(t, 400, 30)

	bar := m.metaBar()

	for _, field := range DefaultStats {
		if !strings.Contains(bar, field) {
			t.Errorf("the default bar must include %q: %q", field, bar)
		}
	}
}

// Guard against drift: every name in KnownStats must actually be renderable by
// the meta bar (config validates against KnownStats, so a listed-but-unrendered
// name would validate and then silently vanish).
func TestEveryKnownStatIsRenderable(t *testing.T) {
	m := sized(t, 500, 30)

	for _, name := range KnownStats {
		m.stats = []string{name}

		if bar := m.metaBar(); !strings.Contains(bar, name) {
			t.Errorf("KnownStats lists %q but the meta bar does not render it", name)
		}
	}
}

// The header shows the provider-reported token usage, and progress against any
// configured limits (5/1000); a limit that is unset shows no denominator.
func TestMetaBarShowsTokensAndLimits(t *testing.T) {
	m := sized(t, 400, 30)
	m.stats = []string{"iter", "tools", "elapsed", "tokens"}
	m.iteration = 5
	m.maxIterations = 1000
	m.toolCount = 12 // maxCalls unset -> no denominator
	m.maxDuration = 30 * time.Minute
	m.inputTokens = 32000
	m.outputTokens = 13000

	bar := m.metaBar()

	if !strings.Contains(bar, "5/1000") {
		t.Errorf("iter must show progress against its limit: %q", bar)
	}

	if strings.Contains(bar, "12/") {
		t.Errorf("tools has no limit and must show no denominator: %q", bar)
	}

	if !strings.Contains(bar, "/30:00") {
		t.Errorf("elapsed must show the time limit: %q", bar)
	}

	if !strings.Contains(bar, "32.0k") || !strings.Contains(bar, "13.0k") {
		t.Errorf("tokens must show provider usage compactly: %q", bar)
	}
}

// A usage update from the run sets the counts the meta bar reads.
func TestHandleEventRecordsUsage(t *testing.T) {
	m := sized(t, 100, 30)

	m.handleEvent(agent.UsageEvent{InputTokens: 1234, OutputTokens: 567})

	if m.inputTokens != 1234 || m.outputTokens != 567 {
		t.Errorf("usage not recorded: in=%d out=%d", m.inputTokens, m.outputTokens)
	}
}

func TestFmtTokens(t *testing.T) {
	for _, tc := range []struct {
		n    int
		want string
	}{
		{0, "0"},
		{532, "532"},
		{45200, "45.2k"},
		{1_200_000, "1.2M"},
	} {
		if got := fmtTokens(tc.n); got != tc.want {
			t.Errorf("fmtTokens(%d) = %q, want %q", tc.n, got, tc.want)
		}
	}
}

// A caller collecting the run's outcome (a draft run) asks the viewer to close
// itself when the run ends; a run of record holds the final screen, because the
// screen is its report.
func TestQuitOnDoneClosesTheViewerWhenTheRunEnds(t *testing.T) {
	exit := agentEventMsg{ev: agent.AgentExitEvent{Code: 0, Reason: "settled", Message: "done"}}

	m := sized(t, 100, 30)
	m.quitOnDone = true

	_, cmd := m.Update(exit)
	if cmd == nil {
		t.Fatal("the viewer should quit once the run ends")
	}

	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Errorf("cmd() = %T, want tea.QuitMsg", cmd())
	}

	// mid-run events must not quit, even with the flag set
	running := sized(t, 100, 30)
	running.quitOnDone = true

	if _, cmd := running.Update(agentEventMsg{ev: agent.IterationEvent{Iteration: 1}}); cmd != nil {
		t.Error("the viewer must stay open while the run is going")
	}

	// without the flag the final screen is held for review
	held := sized(t, 100, 30)

	if _, cmd := held.Update(exit); cmd != nil {
		t.Error("a run of record must hold its final screen")
	}
}

// A fatal agent error also ends the run; the self-closing viewer must not hang
// on it.
func TestQuitOnDoneClosesTheViewerOnAgentError(t *testing.T) {
	m := sized(t, 100, 30)
	m.quitOnDone = true

	_, cmd := m.Update(agentErrMsg{err: fmt.Errorf("provider down")})
	if cmd == nil {
		t.Fatal("the viewer should quit on a fatal error")
	}
}
