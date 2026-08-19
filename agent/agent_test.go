package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// sseServer stands in for a provider. Each element of turns is one complete
// response, served in order, so a test can script a multi-round conversation.
func sseServer(t *testing.T, turns ...[]string) *httptest.Server {
	t.Helper()

	turn := 0

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			t.Errorf("unexpected path %q", r.URL.Path)
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

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
}

// textFrame is a content delta.
func textFrame(text string) string {
	return fmt.Sprintf(`{"choices":[{"delta":{"content":%q}}]}`, text)
}

// stopFrame ends a turn.
func stopFrame() string {
	return `{"choices":[{"delta":{},"finish_reason":"stop"}]}`
}

// successFrame records a successful outcome, which is the only way a run ends
// cleanly: settlement is unconditional.
func successFrame(summary string) string {
	return toolFrame("done_1", "success", fmt.Sprintf(`{"summary":%q}`, summary))
}

// failureFrame records the model giving up: it reached a conclusion, and the
// conclusion is that the task cannot be done.
func failureFrame(reason string) string {
	return toolFrame("done_1", "failure", fmt.Sprintf(`{"reason":%q}`, reason))
}

// toolFrame requests a tool call.
func toolFrame(id, name, arguments string) string {
	return fmt.Sprintf(
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":%q,"type":"function","function":{"name":%q,"arguments":%q}}]},"finish_reason":"tool_calls"}]}`,
		id, name, arguments,
	)
}

func newTestClient(t *testing.T, server *httptest.Server) *Client {
	t.Helper()

	client, err := NewClient(ClientOptions{
		Provider: "custom",
		Model:    "test-model",
		APIKey:   "test-key",
		BaseURL:  server.URL,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	return client
}

// collect drains a run and returns its events and exit.
func collect(t *testing.T, events <-chan AgentEvent, errs <-chan error) ([]AgentEvent, AgentExitEvent) {
	t.Helper()

	var (
		all  []AgentEvent
		exit AgentExitEvent
	)

	for event := range events {
		all = append(all, event)

		if typed, ok := event.(AgentExitEvent); ok {
			exit = typed
		}
	}

	for err := range errs {
		if err != nil {
			t.Fatalf("run error: %v", err)
		}
	}

	return all, exit
}

func TestExecuteWithToolsPlainAnswer(t *testing.T) {
	server := sseServer(t,
		[]string{textFrame("hello "), textFrame("world"), stopFrame()},
		[]string{successFrame("said hello")},
	)

	defer server.Close()

	events, errs := ExecuteWithTools(context.Background(), newTestClient(t, server),
		ExecuteWithToolsOptions{Text: []string{"hi"}})

	all, exit := collect(t, events, errs)

	var tokens strings.Builder

	for _, event := range all {
		if token, ok := event.(TokenAgentEvent); ok {
			tokens.WriteString(token.Token)
		}
	}

	if got := tokens.String(); got != "hello world" {
		t.Errorf("streamed %q, want %q", got, "hello world")
	}

	// answering is not finishing - the run ends when the outcome is recorded
	if exit.Code != 0 || exit.Reason != "settled" {
		t.Errorf("exit = %d/%s, want 0/settled", exit.Code, exit.Reason)
	}
}

// A run the model declared a failure must exit non-zero. It used to share the
// success tool's stop reason, so `zot "…" && deploy` treated "I could not do
// this" as a green light.
func TestExecuteWithToolsFailureExitsNonZero(t *testing.T) {
	server := sseServer(t,
		[]string{failureFrame("cannot reach the host")},
	)

	defer server.Close()

	events, errs := ExecuteWithTools(context.Background(), newTestClient(t, server),
		ExecuteWithToolsOptions{Text: []string{"hi"}})

	_, exit := collect(t, events, errs)

	if exit.Code == 0 {
		t.Errorf("exit code = 0 for a declared failure, want non-zero")
	}

	if exit.Reason != "failed" {
		t.Errorf("exit reason = %q, want failed", exit.Reason)
	}

	if exit.Message != "cannot reach the host" {
		t.Errorf("exit message = %q, want the failure reason", exit.Message)
	}
}

func TestExecuteWithToolsRunsATool(t *testing.T) {
	server := sseServer(t,
		[]string{toolFrame("call_1", "echo", `{"value":"ping"}`)},
		[]string{textFrame("done"), stopFrame()},
		[]string{successFrame("echoed it")},
	)

	defer server.Close()

	var seen map[string]any

	tools := Tools{
		"echo": {
			Description: "echo a value",
			Parameters:  FunctionParameters{"type": "object"},
			Handler: func(_ context.Context, args map[string]any) (any, error) {
				seen = args

				return map[string]any{"echoed": args["value"]}, nil
			},
		},
	}

	events, errs := ExecuteWithTools(context.Background(), newTestClient(t, server),
		ExecuteWithToolsOptions{Text: []string{"echo ping"}, Tools: tools})

	all, exit := collect(t, events, errs)

	if seen["value"] != "ping" {
		t.Errorf("handler saw %v, want value=ping", seen)
	}

	var started, ended bool

	for _, event := range all {
		switch typed := event.(type) {
		case ToolCallStartEvent:
			if typed.Name == "echo" {
				started = true
			}
		case ToolCallEndEvent:
			if typed.Name == "echo" {
				ended = true
			}
		}
	}

	if !started || !ended {
		t.Errorf("tool lifecycle events missing (start=%v end=%v)", started, ended)
	}

	if exit.Code != 0 {
		t.Errorf("exit code = %d, want 0", exit.Code)
	}
}

// The point of settlement, and the reason it is not optional: an answer that
// sounds final is not an ending. The SDK this replaces stopped here.
func TestSettleModeIgnoresProse(t *testing.T) {
	server := sseServer(t,
		[]string{textFrame("All done, the task is completed."), stopFrame()},
		[]string{toolFrame("call_1", "success", `{"summary":"actually finished"}`)},
	)

	defer server.Close()

	events, errs := ExecuteWithTools(context.Background(), newTestClient(t, server),
		ExecuteWithToolsOptions{Text: []string{"do it"}})

	_, exit := collect(t, events, errs)

	if exit.Reason != "settled" {
		t.Errorf("exit reason = %q, want settled", exit.Reason)
	}

	if exit.Message != "actually finished" {
		t.Errorf("exit message = %q, want the terminal tool's summary", exit.Message)
	}

	if exit.Code != 0 {
		t.Errorf("exit code = %d, want 0", exit.Code)
	}
}

// Nudging is bounded, so a model that never records an outcome cannot run
// forever - the run is surfaced as unsettled instead.
func TestSettleModeGivesUp(t *testing.T) {
	server := sseServer(t, []string{textFrame("I think I am finished."), stopFrame()})

	defer server.Close()

	events, errs := ExecuteWithTools(context.Background(), newTestClient(t, server),
		ExecuteWithToolsOptions{Text: []string{"do it"}, MaxSettles: 2})

	_, exit := collect(t, events, errs)

	if exit.Reason != "unsettled" {
		t.Errorf("exit reason = %q, want unsettled", exit.Reason)
	}

	if exit.Code == 0 {
		t.Error("exit code = 0, want non-zero for an unsettled run")
	}
}

func TestEmptyTurnsAreBounded(t *testing.T) {
	server := sseServer(t, []string{stopFrame()})

	defer server.Close()

	events, errs := ExecuteWithTools(context.Background(), newTestClient(t, server),
		ExecuteWithToolsOptions{Text: []string{"say nothing"}})

	_, exit := collect(t, events, errs)

	if exit.Reason != "empty" {
		t.Errorf("exit reason = %q, want empty", exit.Reason)
	}
}

// TestRepeatedToolResultsStopTheRun exercises the loop detection end to end: the
// same call returning the same result, forever, must be caught.
func TestRepeatedToolResultsStopTheRun(t *testing.T) {
	server := sseServer(t, []string{toolFrame("call_1", "search", `{"q":"same"}`)})

	defer server.Close()

	tools := Tools{
		"search": {
			Description: "search",
			Parameters:  FunctionParameters{"type": "object"},
			Handler: func(_ context.Context, _ map[string]any) (any, error) {
				return map[string]any{"records": []any{}}, nil
			},
		},
	}

	events, errs := ExecuteWithTools(context.Background(), newTestClient(t, server),
		ExecuteWithToolsOptions{Text: []string{"search"}, Tools: tools, MaxIterations: 30})

	_, exit := collect(t, events, errs)

	if exit.Reason != "cycle" {
		t.Errorf("exit reason = %q, want cycle", exit.Reason)
	}

	_ = events
}

func TestUnknownToolIsReportedNotFatal(t *testing.T) {
	server := sseServer(t,
		[]string{toolFrame("call_1", "nope", `{}`)},
		[]string{textFrame("recovered"), stopFrame()},
		[]string{successFrame("recovered and finished")},
	)

	defer server.Close()

	events, errs := ExecuteWithTools(context.Background(), newTestClient(t, server),
		ExecuteWithToolsOptions{Text: []string{"go"}, Tools: Tools{}})

	all, exit := collect(t, events, errs)

	var reported bool

	for _, event := range all {
		if typed, ok := event.(ToolCallErrorEvent); ok && typed.Name == "nope" {
			reported = true
		}
	}

	if !reported {
		t.Error("an unknown tool should surface as a ToolCallErrorEvent")
	}

	if exit.Code != 0 {
		t.Errorf("exit code = %d, want the run to recover", exit.Code)
	}
}

func TestClientRejectsPlaintextEndpoint(t *testing.T) {
	_, err := NewClient(ClientOptions{
		Provider: "custom",
		Model:    "m",
		APIKey:   "k",
		BaseURL:  "http://example.com/v1",
	})

	if err == nil {
		t.Fatal("a plaintext non-loopback endpoint must be rejected")
	}
}

func TestClientRequiresCredential(t *testing.T) {
	_, err := NewClient(ClientOptions{Provider: "openai", Model: "gpt-5.4"})

	if err == nil {
		t.Fatal("a provider that needs a key must not resolve without one")
	}
}

func TestToolArgumentsSurviveFragmentation(t *testing.T) {
	// arguments arrive split across frames, identified only by index
	frames := []string{
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"echo","arguments":"{\"val"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"ue\":\"joined\"}"}}]},"finish_reason":"tool_calls"}]}`,
	}

	server := sseServer(t, frames, []string{textFrame("ok"), stopFrame()})

	defer server.Close()

	var seen map[string]any

	tools := Tools{
		"echo": {
			Description: "echo",
			Parameters:  FunctionParameters{"type": "object"},
			Handler: func(_ context.Context, args map[string]any) (any, error) {
				seen = args

				return "ok", nil
			},
		},
	}

	events, errs := ExecuteWithTools(context.Background(), newTestClient(t, server),
		ExecuteWithToolsOptions{Text: []string{"go"}, Tools: tools})

	collect(t, events, errs)

	if seen["value"] != "joined" {
		encoded, _ := json.Marshal(seen)

		t.Errorf("fragmented arguments assembled to %s, want value=joined", encoded)
	}
}

// Settlement cannot be switched off. A run that only ever talks is unsettled,
// whatever it says, because an unattended run needs an unambiguous ending.
func TestSettlementIsNotOptional(t *testing.T) {
	server := sseServer(t, []string{textFrame("Everything is finished and complete."), stopFrame()})

	defer server.Close()

	events, errs := ExecuteWithTools(context.Background(), newTestClient(t, server),
		ExecuteWithToolsOptions{Text: []string{"go"}, MaxSettles: 1})

	_, exit := collect(t, events, errs)

	if exit.Reason != "unsettled" {
		t.Errorf("exit reason = %q, want unsettled - there is no way to opt out", exit.Reason)
	}
}

// The terminal tools are always offered, since the model cannot settle without
// them.
func TestTerminalToolsAreAlwaysOffered(t *testing.T) {
	var offered []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}

		json.NewDecoder(r.Body).Decode(&body)

		offered = nil

		for _, tool := range body.Tools {
			offered = append(offered, tool.Function.Name)
		}

		w.Header().Set("Content-Type", "text/event-stream")

		fmt.Fprintf(w, "data: %s\n\n", successFrame("done"))
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))

	defer server.Close()

	events, errs := ExecuteWithTools(context.Background(), newTestClient(t, server),
		ExecuteWithToolsOptions{Text: []string{"go"}})

	collect(t, events, errs)

	seen := map[string]bool{}

	for _, name := range offered {
		seen[name] = true
	}

	for _, want := range []string{"success", "failure"} {
		if !seen[want] {
			t.Errorf("%s was not offered to the model; offered: %v", want, offered)
		}
	}
}

func TestClientExposesItsResolvedConfiguration(t *testing.T) {
	client, err := NewClient(ClientOptions{
		Provider: "groq",
		Model:    "glm-5.2",
		APIKey:   "k",
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	if got := client.Model(); got != "glm-5.2" {
		t.Errorf("Model = %q", got)
	}

	if got := client.Provider(); got != "groq" {
		t.Errorf("Provider = %q", got)
	}

	if got := client.BaseURL(); got == "" {
		t.Error("BaseURL must resolve to the provider's endpoint")
	}
}

// The provider list is what an error message offers the user, so it has to name
// the ones that actually work.
func TestProvidersIsUsable(t *testing.T) {
	providers := Providers()

	if len(providers) < 5 {
		t.Fatalf("only %d providers listed", len(providers))
	}

	seen := map[string]bool{}

	for _, name := range providers {
		seen[name] = true
	}

	for _, want := range []string{"openai", "anthropic", "groq", "ollama", "custom"} {
		if !seen[want] {
			t.Errorf("%q should be listed", want)
		}
	}
}

// Every event answers to the interface, so a consumer's type switch is total.
func TestEveryEventImplementsTheInterface(t *testing.T) {
	events := []AgentEvent{
		TokenAgentEvent{},
		ReasoningTokenAgentEvent{},
		MessageAgentEvent{},
		IterationEvent{},
		ToolCallStartEvent{},
		ToolCallEndEvent{},
		ToolCallErrorEvent{},
		RetryEvent{},
		RunawayEvent{},
		ResultAgentEvent{},
		AgentExitEvent{},
	}

	seen := map[string]bool{}

	for _, event := range events {
		kind := event.agentEventType()

		if kind == "" {
			t.Errorf("%T reports no type", event)
		}

		if seen[kind] {
			t.Errorf("%T shares the type %q with another event", event, kind)
		}

		seen[kind] = true
	}
}

// The skill tool is registered automatically when a skill is embedded, because
// an embedded skill is unreachable without it - but a caller's own `skill` tool
// must still win.
func TestWithSkillTool(t *testing.T) {
	embedded := []SkillDefinition{{Name: "deploy", Source: SkillSourceEmbedded}}
	onDisk := []SkillDefinition{{Name: "review", Source: SkillSourceDirectory}}

	if tools := withSkillTool(Tools{}, embedded); tools["skill"].Handler == nil {
		t.Error("an embedded skill must bring the skill tool with it")
	}

	if tools := withSkillTool(Tools{}, onDisk); len(tools) != 0 {
		t.Error("directory skills need no tool - they are read with `read`")
	}

	own := Tools{"skill": {Description: "the caller's own"}}

	if tools := withSkillTool(own, embedded); tools["skill"].Description != "the caller's own" {
		t.Error("a caller's own skill tool must not be replaced")
	}

	// the original map is not mutated
	if len(own) != 1 {
		t.Error("withSkillTool must not modify the caller's map")
	}
}

// recordingRecorder captures everything the engine hands over.
type recordingRecorder struct {
	messages []Message
	events   []string
	summary  *Summary
	resets   int
}

func (r *recordingRecorder) RecordMessage(message Message) error {
	r.messages = append(r.messages, message)

	return nil
}

func (r *recordingRecorder) RecordEvent(kind, tool, _ string, _ int) error {
	r.events = append(r.events, kind+":"+tool)

	return nil
}

func (r *recordingRecorder) RecordReset() error {
	r.messages = nil
	r.resets++

	return nil
}

func (r *recordingRecorder) RecordResult(summary Summary) error {
	r.summary = &summary

	return nil
}

// The recorder is what makes a run inspectable and resumable afterwards, so it
// has to see the conversation the run actually ended with - not the one it was
// handed, and not a stream of partial tokens.
func TestRecorderSeesTheRun(t *testing.T) {
	server := sseServer(t,
		[]string{toolFrame("call_1", "echo", `{"value":"ping"}`)},
		[]string{textFrame("done"), stopFrame()},
		[]string{successFrame("echoed it")},
	)

	defer server.Close()

	tools := Tools{
		"echo": {
			Description: "echo a value",
			Parameters:  FunctionParameters{"type": "object"},
			Handler: func(_ context.Context, args map[string]any) (any, error) {
				return map[string]any{"echoed": args["value"]}, nil
			},
		},
	}

	recorder := &recordingRecorder{}

	events, errs := ExecuteWithTools(context.Background(), newTestClient(t, server),
		ExecuteWithToolsOptions{Text: []string{"echo ping"}, Tools: tools, Recorder: recorder})

	_, exit := collect(t, events, errs)

	if recorder.summary == nil {
		t.Fatal("the outcome must reach the recorder")
	}

	if recorder.summary.Reason != exit.Reason || recorder.summary.Code != exit.Code {
		t.Errorf("recorded %+v, but the run exited %s/%d", recorder.summary, exit.Reason, exit.Code)
	}

	if recorder.summary.Iterations == 0 || recorder.summary.Calls == 0 {
		t.Errorf("the recorded budget is empty: %+v", recorder.summary)
	}

	// the seeded instruction, and then everything the run produced
	if len(recorder.messages) < 2 {
		t.Fatalf("recorded %d messages, want the prompt plus the run", len(recorder.messages))
	}

	if recorder.messages[0].Type != TypeUser || recorder.messages[0].Text != "echo ping" {
		t.Errorf("the first recorded message should be the instruction, got %+v", recorder.messages[0])
	}

	// what is recorded has to match what the run ended with, or a resume
	// continues a conversation the agent never had
	if len(recorder.messages) != len(exit.Messages) {
		t.Errorf("recorded %d messages, run ended with %d", len(recorder.messages), len(exit.Messages))
	}

	for index := range recorder.messages {
		if index >= len(exit.Messages) {
			break
		}

		if recorder.messages[index].Type != exit.Messages[index].Type ||
			recorder.messages[index].Text != exit.Messages[index].Text {
			t.Errorf("message %d: recorded %+v, run ended with %+v",
				index, recorder.messages[index], exit.Messages[index])
		}
	}

	var sawToolCall bool

	for _, event := range recorder.events {
		if strings.HasPrefix(event, "toolCall:echo") || strings.Contains(event, ":echo") {
			sawToolCall = true
		}
	}

	if !sawToolCall {
		t.Errorf("the tool call should be in the event log: %v", recorder.events)
	}
}

// A resumed run is seeded with an earlier conversation. Those messages have to
// be recorded once, so the new log stands alone as the full history.
func TestRecorderRecordsSeededMessagesOnce(t *testing.T) {
	server := sseServer(t,
		[]string{textFrame("ok"), stopFrame()},
		[]string{successFrame("done")},
	)

	defer server.Close()

	recorder := &recordingRecorder{}

	seed := []Message{
		{Type: TypeUser, Text: "the original task"},
		{Type: TypeBot, Text: "an earlier answer"},
	}

	events, errs := ExecuteWithTools(context.Background(), newTestClient(t, server),
		ExecuteWithToolsOptions{Messages: seed, Text: []string{"carry on"}, Recorder: recorder})

	_, exit := collect(t, events, errs)

	if len(recorder.messages) < 3 {
		t.Fatalf("recorded %d messages, want the seed plus the run", len(recorder.messages))
	}

	if recorder.messages[0].Text != "the original task" || recorder.messages[1].Text != "an earlier answer" {
		t.Errorf("the seeded conversation must be recorded first: %+v", recorder.messages[:2])
	}

	// no duplicates: a run whose log replayed its own seed would grow the
	// conversation on every resume until it fell out of the context window
	counts := map[string]int{}

	for _, message := range recorder.messages {
		counts[string(message.Type)+"/"+message.Text]++
	}

	for key, count := range counts {
		if count > 1 {
			t.Errorf("message %q was recorded %d times", key, count)
		}
	}

	if len(recorder.messages) != len(exit.Messages) {
		t.Errorf("recorded %d messages, run ended with %d", len(recorder.messages), len(exit.Messages))
	}
}

// A recorder that fails must not end a run. Losing a log line is bad; losing
// the work is worse.
func TestARecorderThatFailsDoesNotBreakTheRun(t *testing.T) {
	server := sseServer(t,
		[]string{textFrame("ok"), stopFrame()},
		[]string{successFrame("done")},
	)

	defer server.Close()

	events, errs := ExecuteWithTools(context.Background(), newTestClient(t, server),
		ExecuteWithToolsOptions{Text: []string{"hi"}, Recorder: failingRecorder{}})

	_, exit := collect(t, events, errs)

	if exit.Code != 0 {
		t.Errorf("exit = %d/%s, want a clean run despite the recorder", exit.Code, exit.Reason)
	}
}

type failingRecorder struct{}

func (failingRecorder) RecordReset() error                      { return errTest }
func (failingRecorder) RecordMessage(Message) error             { return errTest }
func (failingRecorder) RecordEvent(_, _, _ string, _ int) error { return errTest }
func (failingRecorder) RecordResult(Summary) error              { return errTest }

var errTest = errors.New("disk full")

// lockedRecorder is a recordingRecorder that can be read while the run is still
// going, which is the only way to see what a crash would have left behind.
type lockedRecorder struct {
	mu    sync.Mutex
	inner recordingRecorder
}

func (r *lockedRecorder) RecordMessage(message Message) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.inner.RecordMessage(message)
}

func (r *lockedRecorder) RecordEvent(kind, tool, text string, iteration int) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.inner.RecordEvent(kind, tool, text, iteration)
}

func (r *lockedRecorder) RecordReset() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.inner.RecordReset()
}

func (r *lockedRecorder) RecordResult(summary Summary) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.inner.RecordResult(summary)
}

func (r *lockedRecorder) snapshot() []Message {
	r.mu.Lock()
	defer r.mu.Unlock()

	return append([]Message(nil), r.inner.messages...)
}

// The session log promises that "a crashed run still leaves everything up to the
// crash" and that replaying the messages is enough to continue. The conversation
// was written only once the run had ended, so a run killed at iteration 500 left
// a log holding the seed and nothing else, and --resume started the task again
// from scratch - discarding hours of work in exactly the case the log exists for.
func TestTheConversationIsRecordedAsTheRunGoes(t *testing.T) {
	recorder := &lockedRecorder{}

	// captured from inside the second tool call: what the log would hold if the
	// process died right there
	var midRun []Message

	round := 0

	tools := map[string]ToolDefinition{
		"echo": {
			Description: "echo",
			Parameters:  map[string]any{"type": "object"},
			Handler: func(context.Context, map[string]any) (any, error) {
				round++

				if round == 2 {
					midRun = recorder.snapshot()
				}

				return "ok", nil
			},
		},
	}

	server := sseServer(t,
		[]string{toolFrame("call_1", "echo", `{"value":"one"}`)},
		[]string{toolFrame("call_2", "echo", `{"value":"two"}`)},
		[]string{successFrame("finished")},
	)

	defer server.Close()

	events, errs := ExecuteWithTools(context.Background(), newTestClient(t, server),
		ExecuteWithToolsOptions{Text: []string{"go"}, Tools: tools, Recorder: recorder})

	collect(t, events, errs)

	if round < 2 {
		t.Fatalf("the run made %d tool rounds, want at least 2", round)
	}

	// the seed is one user message; anything beyond it is work that survived
	if len(midRun) <= 1 {
		t.Fatalf("mid-run the log held %d messages, want the first round's work already durable", len(midRun))
	}

	var sawActivity bool

	for _, message := range midRun {
		if message.Type == TypeActivity {
			sawActivity = true
		}
	}

	if !sawActivity {
		t.Error("the first round's tool call and result were not in the log a crash would have left")
	}
}

// endlessToolRounds is a run that never finishes on its own: the (repeated)
// last scripted turn keeps requesting the same tool, so the run is still going
// whenever a cancellation test needs it to be.
func endlessToolRounds(t *testing.T) (*httptest.Server, Tools) {
	t.Helper()

	server := sseServer(t, []string{toolFrame("call_1", "echo", `{}`)})

	tools := Tools{
		"echo": {
			Description: "echo",
			Parameters:  map[string]any{"type": "object"},
			Handler: func(context.Context, map[string]any) (any, error) {
				return "ok", nil
			},
		},
	}

	return server, tools
}

// The events channel is documented as streaming until the run ends - but an
// embedder that cancels the run and walks away from the channel must not leave
// the run goroutine parked forever on a send nobody will ever receive. The
// goroutine ending is observable as errs closing, which it only does on return.
func TestAnAbandonedConsumerDoesNotLeakTheRun(t *testing.T) {
	server, tools := endlessToolRounds(t)

	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())

	defer cancel()

	events, errs := ExecuteWithTools(ctx, newTestClient(t, server),
		ExecuteWithToolsOptions{Text: []string{"go"}, Tools: tools})

	// the run is genuinely under way before the consumer walks away
	<-events

	cancel()

	// the consumer never reads events again; the run goroutine must still end
	finished := make(chan struct{})

	go func() {
		for range errs {
			// a cancelled run reports its context error here - expected
		}

		close(finished)
	}()

	select {
	case <-finished:
	case <-time.After(15 * time.Second):
		t.Fatal("the run goroutine is still parked on an event send nobody will receive")
	}
}

// The flip side of not leaking: a consumer that keeps draining - the documented
// contract, and what the TUI does - must still receive the exit event when the
// run is cancelled. Cancellation changes how the run ends, never whether that
// ending is reported.
func TestACancelledRunStillDeliversTheExitEvent(t *testing.T) {
	server, tools := endlessToolRounds(t)

	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())

	defer cancel()

	events, errs := ExecuteWithTools(ctx, newTestClient(t, server),
		ExecuteWithToolsOptions{Text: []string{"go"}, Tools: tools})

	var (
		sawExit bool
		first   = true
	)

	for event := range events {
		if first {
			first = false

			cancel()
		}

		if _, ok := event.(AgentExitEvent); ok {
			sawExit = true
		}
	}

	for range errs {
		// the cancellation surfaces here too; the exit event is what is under test
	}

	if !sawExit {
		t.Fatal("a draining consumer must still receive the exit event after cancellation")
	}
}
