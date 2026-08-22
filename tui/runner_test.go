package tui

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/openzot/openzot/agent"
)

// runAgent is the seam between the engine and the screen. It is a pure pump,
// and the thing worth proving about a pump is that nothing goes missing: every
// event reaches the program, an error reaches it too, and the stream always
// ends with a done message so the viewer knows the run is over rather than
// hanging on a spinner nobody will ever stop.

// scriptedClient answers with the given SSE frame sets, one per turn.
func scriptedClient(t *testing.T, turns ...[]string) *agent.Client {
	t.Helper()

	turn := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")

		index := turn
		if index >= len(turns) {
			index = len(turns) - 1
		}

		turn++

		for _, frame := range turns[index] {
			fmt.Fprintf(w, "data: %s\n\n", frame)
		}

		fmt.Fprint(w, "data: [DONE]\n\n")
	}))

	t.Cleanup(server.Close)

	client, err := agent.NewClient(agent.ClientOptions{
		Provider: "custom",
		Model:    "test-model",
		APIKey:   "k",
		BaseURL:  server.URL,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	return client
}

// headless starts a Bubble Tea program with no terminal attached, collecting
// every message it receives.
func headless(t *testing.T) (*tea.Program, *collector, func() model) {
	t.Helper()

	seen := &collector{}

	program := tea.NewProgram(
		&recordingModel{collector: seen, inner: newModel("zot", "do the thing", "test-model", "custom", "/tmp/work", false)},
		tea.WithInput(nil),
		tea.WithOutput(io.Discard),
		tea.WithoutSignalHandler(),
	)

	finished := make(chan tea.Model, 1)

	go func() {
		final, err := program.Run()
		if err != nil {
			t.Errorf("program.Run: %v", err)
		}

		finished <- final
	}()

	return program, seen, func() model {
		program.Quit()

		select {
		case final := <-finished:
			if recording, ok := final.(*recordingModel); ok {
				return recording.inner
			}

			return model{}

		case <-time.After(5 * time.Second):
			t.Fatal("the program did not stop")

			return model{}
		}
	}
}

// collector records what the program was sent.
type collector struct {
	events []agent.AgentEvent
	errs   []error
	done   int
}

// recordingModel wraps the real model, noting the agent messages that arrive.
type recordingModel struct {
	collector *collector
	inner     model
}

func (r *recordingModel) Init() tea.Cmd { return nil }

func (r *recordingModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch typed := msg.(type) {
	case agentEventMsg:
		r.collector.events = append(r.collector.events, typed.ev)
	case agentErrMsg:
		r.collector.errs = append(r.collector.errs, typed.err)
	case agentDoneMsg:
		r.collector.done++
	}

	updated, cmd := r.inner.Update(msg)

	if typed, ok := updated.(model); ok {
		r.inner = typed
	}

	return r, cmd
}

func (r *recordingModel) View() string { return "" }

func TestRunAgentRelaysEveryEventAndThenDone(t *testing.T) {
	client := scriptedClient(t,
		[]string{
			`{"choices":[{"delta":{"content":"working on it"}}]}`,
			`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
		},
		[]string{
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"d","type":"function","function":{"name":"success","arguments":"{\"summary\":\"all done\"}"}}]},"finish_reason":"tool_calls"}]}`,
		},
	)

	program, seen, stop := headless(t)

	runAgent(context.Background(), program, client, agent.ExecuteWithToolsOptions{
		Text: []string{"do the thing"},
	}, make(chan struct{}))

	final := stop()

	if seen.done != 1 {
		t.Errorf("the done message arrived %d times, want exactly one", seen.done)
	}

	if len(seen.events) == 0 {
		t.Fatal("no events reached the program")
	}

	var tokens strings.Builder

	var exited bool

	for _, event := range seen.events {
		switch typed := event.(type) {
		case agent.TokenAgentEvent:
			tokens.WriteString(typed.Token)
		case agent.AgentExitEvent:
			exited = true
		}
	}

	if !strings.Contains(tokens.String(), "working on it") {
		t.Errorf("the streamed answer did not reach the screen: %q", tokens.String())
	}

	if !exited {
		t.Error("the exit event must reach the screen; it is what stops the spinner")
	}

	if final.status == statusRunning {
		t.Error("the viewer should not still be showing a running run")
	}
}

// A run that cannot reach its provider must surface the failure rather than
// leaving a spinner turning forever.
func TestRunAgentRelaysAFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)

		fmt.Fprint(w, `{"error":{"message":"upstream is down"}}`)
	}))

	defer server.Close()

	client, err := agent.NewClient(agent.ClientOptions{
		Provider: "custom",
		Model:    "test-model",
		APIKey:   "k",
		BaseURL:  server.URL,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	program, seen, stop := headless(t)

	runAgent(context.Background(), program, client, agent.ExecuteWithToolsOptions{
		Text: []string{"do the thing"},

		// a persistent outage is retried with a growing backoff; this test is
		// about the failure reaching the screen, not about waiting it out
		RetryBackoff: -1,
	}, make(chan struct{}))

	final := stop()

	if seen.done != 1 {
		t.Errorf("the done message arrived %d times, want exactly one", seen.done)
	}

	if len(seen.errs) == 0 {
		t.Fatal("the provider failure never reached the screen")
	}

	if final.runError() == nil {
		t.Error("a failed run must be reportable to the caller")
	}
}

// A cancelled run still has to end cleanly: the pump drains and the done
// message arrives, or the viewer never comes back.
func TestRunAgentEndsOnCancellation(t *testing.T) {
	client := scriptedClient(t, []string{
		`{"choices":[{"delta":{"content":"thinking"}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
	})

	ctx, cancel := context.WithCancel(context.Background())

	cancel()

	program, seen, stop := headless(t)

	runAgent(ctx, program, client, agent.ExecuteWithToolsOptions{Text: []string{"do the thing"}}, make(chan struct{}))

	stop()

	if seen.done != 1 {
		t.Errorf("the done message arrived %d times, want exactly one", seen.done)
	}
}

// Quitting the viewer must stop the agent, not merely stop watching it. The
// agent holds shell and file-write access, so returning from the viewer with the
// run still going leaves something editing the working tree with nothing on
// screen reporting what it does. In the zot CLI process exit hides this; in a
// long-lived embedding process it does not.
func TestQuittingTheViewerStopsTheAgent(t *testing.T) {
	streaming := make(chan struct{})
	cancelled := make(chan struct{})

	var once sync.Once

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")

		flusher, _ := w.(http.Flusher)

		if flusher != nil {
			flusher.Flush()
		}

		once.Do(func() { close(streaming) })

		// hold the turn open, the way a model thinking through a long task does,
		// and report whether the client ever went away
		select {
		case <-r.Context().Done():
			close(cancelled)
		case <-time.After(20 * time.Second):
		}
	}))

	t.Cleanup(server.Close)

	client, err := agent.NewClient(agent.ClientOptions{
		Provider: "custom",
		Model:    "test-model",
		APIKey:   "k",
		BaseURL:  server.URL,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	m := newModel("zot", "do the thing", "test-model", "custom", t.TempDir(), false)

	// start stands in for the user pressing q: it returns as soon as the agent
	// is under way, exactly as (*tea.Program).Run does on tea.Quit
	start := func(p *tea.Program) (tea.Model, error) {
		<-streaming

		return m, nil
	}

	if _, err := runViewer(context.Background(), m, client, agent.ExecuteWithToolsOptions{
		Text: []string{"do the thing"},
	}, start); err == nil {
		t.Error("quitting mid-run should report that the run did not finish")
	}

	select {
	case <-cancelled:
	case <-time.After(15 * time.Second):
		t.Fatal("the agent was still running after the viewer quit")
	}
}

// abortRecorder captures the run's recorded outcome.
type abortRecorder struct {
	mu     sync.Mutex
	result *agent.Summary
}

func (r *abortRecorder) RecordMessage(agent.Message) error             { return nil }
func (r *abortRecorder) RecordEvent(string, string, string, int) error { return nil }
func (r *abortRecorder) RecordFailure(*agent.Failure) error            { return nil }
func (r *abortRecorder) RecordReset() error                            { return nil }
func (r *abortRecorder) RecordResult(summary agent.Summary) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.result = &summary
	return nil
}

func (r *abortRecorder) recorded() *agent.Summary {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.result
}

// Quitting the viewer must leave the run's aborted outcome in the session.
// runViewer used to return the moment the program did, racing the engine's
// result record against the caller's deferred session close - a quit run was
// logged as "running/interrupted" forever, with no outcome at all.
func TestQuittingTheViewerStillRecordsTheOutcome(t *testing.T) {
	streaming := make(chan struct{})

	var once sync.Once

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")

		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}

		once.Do(func() { close(streaming) })

		select {
		case <-r.Context().Done():
		case <-time.After(20 * time.Second):
		}
	}))

	t.Cleanup(server.Close)

	client, err := agent.NewClient(agent.ClientOptions{
		Provider: "custom",
		Model:    "test-model",
		APIKey:   "k",
		BaseURL:  server.URL,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	recorder := &abortRecorder{}

	m := newModel("zot", "do the thing", "test-model", "custom", t.TempDir(), false)

	// the program runs headlessly so the event pump is genuinely consuming;
	// quitting once the stream is under way is the user pressing q mid-run
	start := func(p *tea.Program) (tea.Model, error) {
		go func() {
			<-streaming

			p.Quit()
		}()

		return p.Run()
	}

	_, _ = runViewer(context.Background(), m, client, agent.ExecuteWithToolsOptions{
		Text:     []string{"do the thing"},
		Recorder: recorder,
	}, start, tea.WithInput(nil), tea.WithOutput(io.Discard), tea.WithoutSignalHandler())

	result := recorder.recorded()

	if result == nil {
		t.Fatal("the quit run recorded no outcome at all")
	}

	if result.Reason != agent.ReasonAborted {
		t.Errorf("Reason = %q, want the abort on record", result.Reason)
	}
}
