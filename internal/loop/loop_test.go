package loop

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openzot/openzot/internal/provider"
)

// stub serves scripted turns over the OpenAI-compatible wire format, so the loop
// can be driven without a model.
func stub(t *testing.T, turns ...[]string) *provider.Client {
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

	client, err := provider.New(provider.Config{
		Provider: provider.Custom,
		Model:    "test-model",
		APIKey:   "k",
		BaseURL:  server.URL,
	})
	if err != nil {
		t.Fatalf("provider.New: %v", err)
	}

	return client
}

func text(s string) string {
	return fmt.Sprintf(`{"choices":[{"delta":{"content":%q}}]}`, s)
}

func stop() string {
	return `{"choices":[{"delta":{},"finish_reason":"stop"}]}`
}

func truncated() string {
	return `{"choices":[{"delta":{},"finish_reason":"length"}]}`
}

func tool(id, name, arguments string) string {
	return fmt.Sprintf(
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":%q,"type":"function","function":{"name":%q,"arguments":%q}}]},"finish_reason":"tool_calls"}]}`,
		id, name, arguments,
	)
}

func run(t *testing.T, options Options) Result {
	t.Helper()

	engine, err := New(options)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	return engine.Run(context.Background(), nil)
}

func echoTool(calls *int) map[string]ToolDefinition {
	return map[string]ToolDefinition{
		"echo": {
			Name:        "echo",
			Description: "echo",
			Parameters:  map[string]any{"type": "object"},
			Handler: func(context.Context, map[string]any) (any, error) {
				*calls++

				return "ok", nil
			},
		},
	}
}

func TestNewAppliesDefaults(t *testing.T) {
	engine, err := New(Options{Client: stub(t, []string{stop()})})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if engine.maxIterations != DefaultMaxIterations ||
		engine.maxContinuations != DefaultMaxContinuations ||
		engine.maxCycles != DefaultMaxCycles ||
		engine.maxEmpties != DefaultMaxEmpties {
		t.Errorf("defaults not applied: %+v", engine)
	}

	// calls and time are unbounded unless set - only the iteration count is a
	// hard default backstop
	if engine.maxCalls != 0 {
		t.Errorf("maxCalls = %d, want 0 (unbounded) by default", engine.maxCalls)
	}

	if engine.maxDuration != 0 {
		t.Errorf("maxDuration = %v, want unbounded by default", engine.maxDuration)
	}

	// settle mode is opt-in
	if engine.settleMode() {
		t.Error("settle mode must be off unless MaxSettles is positive")
	}

	if engine.inputBudget < MinInputTokens {
		t.Errorf("input budget %d is below the floor", engine.inputBudget)
	}
}

func TestNewRequiresAClient(t *testing.T) {
	if _, err := New(Options{}); err == nil {
		t.Fatal("an engine without a client must not be constructed")
	}
}

func TestIterationBudgetStopsTheRun(t *testing.T) {
	calls := 0

	result := run(t, Options{
		Client:        stub(t, []string{tool("c1", "echo", `{}`)}),
		Tools:         echoTool(&calls),
		Messages:      []Message{{Type: TypeUser, Text: "go"}},
		MaxIterations: 3,

		// disable the cycle guard so the iteration cap is what fires
		MaxCycles: 1000,
	})

	if result.Reason != StopIterations {
		t.Errorf("reason = %q, want iterations", result.Reason)
	}

	if result.Budget.Iterations != 3 {
		t.Errorf("iterations = %d, want 3", result.Budget.Iterations)
	}
}

func TestCallBudgetStopsTheRun(t *testing.T) {
	calls := 0

	result := run(t, Options{
		Client:        stub(t, []string{tool("c1", "echo", `{}`)}),
		Tools:         echoTool(&calls),
		Messages:      []Message{{Type: TypeUser, Text: "go"}},
		MaxCalls:      2,
		MaxIterations: 50,
		MaxCycles:     1000,
	})

	if result.Reason != StopCalls {
		t.Errorf("reason = %q, want calls", result.Reason)
	}

	if calls > 2 {
		t.Errorf("handler ran %d times, want at most the budget of 2", calls)
	}
}

func TestEmptyTurnsAreBounded(t *testing.T) {
	result := run(t, Options{
		Client:     stub(t, []string{stop()}),
		Messages:   []Message{{Type: TypeUser, Text: "go"}},
		MaxEmpties: 2,
	})

	if result.Reason != StopEmpty {
		t.Errorf("reason = %q, want empty", result.Reason)
	}

	if result.Budget.Empties != 2 {
		t.Errorf("empties = %d, want 2", result.Budget.Empties)
	}
}

func TestTruncatedOutputIsContinued(t *testing.T) {
	result := run(t, Options{
		Client: stub(t,
			[]string{text("half an answ"), truncated()},
			[]string{text("er, continued"), stop()},
		),
		Messages: []Message{{Type: TypeUser, Text: "go"}},
	})

	if result.Reason != StopStop {
		t.Errorf("reason = %q, want stop", result.Reason)
	}

	if result.Budget.Continuations != 1 {
		t.Errorf("continuations = %d, want 1", result.Budget.Continuations)
	}

	// the continuation notice must be in the thread, telling the model to pick
	// up where it stopped rather than start again
	var nudged bool

	for _, message := range result.Messages {
		if strings.Contains(message.Text, "cut off at the output limit") {
			nudged = true
		}
	}

	if !nudged {
		t.Error("a truncated turn must be followed by a continuation notice")
	}
}

func TestTruncationIsBounded(t *testing.T) {
	result := run(t, Options{
		Client:           stub(t, []string{text("x"), truncated()}),
		Messages:         []Message{{Type: TypeUser, Text: "go"}},
		MaxContinuations: 2,
	})

	if result.Reason != StopContinuations {
		t.Errorf("reason = %q, want continuations", result.Reason)
	}
}

func TestRepeatedToolResultsTripTheCycleGuard(t *testing.T) {
	calls := 0

	result := run(t, Options{
		Client:        stub(t, []string{tool("c1", "echo", `{"q":"same"}`)}),
		Tools:         echoTool(&calls),
		Messages:      []Message{{Type: TypeUser, Text: "go"}},
		MaxIterations: 50,
		MaxCycles:     1,
	})

	if result.Reason != StopCycle {
		t.Errorf("reason = %q, want cycle", result.Reason)
	}

	// the run must have been nudged before being stopped
	if result.Budget.Cycles != 1 {
		t.Errorf("cycles = %d, want 1", result.Budget.Cycles)
	}
}

func TestSettleModeRequiresATerminalCall(t *testing.T) {
	result := run(t, Options{
		Client: stub(t,
			[]string{text("All done, the task is completed."), stop()},
			[]string{tool("c9", SuccessTool, `{"summary":"really done"}`)},
		),
		Messages:   []Message{{Type: TypeUser, Text: "go"}},
		MaxSettles: 5,
	})

	if result.Reason != StopSettled {
		t.Fatalf("reason = %q, want settled", result.Reason)
	}

	if result.Message != "really done" {
		t.Errorf("message = %q, want the terminal call's summary", result.Message)
	}

	if result.Budget.Settles != 1 {
		t.Errorf("settles = %d, want 1 nudge before the terminal call", result.Budget.Settles)
	}
}

func TestSettleModeFailureToolAlsoEnds(t *testing.T) {
	result := run(t, Options{
		Client:     stub(t, []string{tool("c9", FailureTool, `{"reason":"cannot reach the host"}`)}),
		Messages:   []Message{{Type: TypeUser, Text: "go"}},
		MaxSettles: 5,
	})

	if result.Reason != StopSettled {
		t.Errorf("reason = %q, want settled", result.Reason)
	}

	if result.Message != "cannot reach the host" {
		t.Errorf("message = %q, want the failure reason", result.Message)
	}
}

func TestSettleModeGivesUpEventually(t *testing.T) {
	result := run(t, Options{
		Client:     stub(t, []string{text("I believe I am finished."), stop()}),
		Messages:   []Message{{Type: TypeUser, Text: "go"}},
		MaxSettles: 2,
	})

	if result.Reason != StopUnsettled {
		t.Errorf("reason = %q, want unsettled", result.Reason)
	}
}

// Outside settle mode a plain stop is a legitimate ending.
func TestPlainStopEndsWithoutSettleMode(t *testing.T) {
	result := run(t, Options{
		Client:   stub(t, []string{text("here you go"), stop()}),
		Messages: []Message{{Type: TypeUser, Text: "go"}},
	})

	if result.Reason != StopStop {
		t.Errorf("reason = %q, want stop", result.Reason)
	}
}

func TestCancellationStopsTheRun(t *testing.T) {
	engine, err := New(Options{
		Client:   stub(t, []string{text("hi"), stop()}),
		Messages: []Message{{Type: TypeUser, Text: "go"}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	cancel()

	result := engine.Run(ctx, nil)

	if result.Reason != StopAborted {
		t.Errorf("reason = %q, want aborted", result.Reason)
	}
}

func TestUnknownToolIsFedBackNotFatal(t *testing.T) {
	result := run(t, Options{
		Client: stub(t,
			[]string{tool("c1", "missing", `{}`)},
			[]string{text("recovered"), stop()},
		),
		Messages: []Message{{Type: TypeUser, Text: "go"}},
	})

	if result.Reason != StopStop {
		t.Errorf("reason = %q, want the run to recover and stop normally", result.Reason)
	}

	var reported bool

	for _, message := range result.Messages {
		if strings.Contains(message.Text, "no such tool") {
			reported = true
		}
	}

	if !reported {
		t.Error("the failure must be fed back to the model")
	}
}

func TestToolErrorIsFedBackNotFatal(t *testing.T) {
	tools := map[string]ToolDefinition{
		"boom": {
			Name:       "boom",
			Parameters: map[string]any{"type": "object"},
			Handler: func(context.Context, map[string]any) (any, error) {
				return nil, fmt.Errorf("disk on fire")
			},
		},
	}

	result := run(t, Options{
		Client: stub(t,
			[]string{tool("c1", "boom", `{}`)},
			[]string{text("noted"), stop()},
		),
		Tools:    tools,
		Messages: []Message{{Type: TypeUser, Text: "go"}},
	})

	var reported bool

	for _, message := range result.Messages {
		if strings.Contains(message.Text, "disk on fire") {
			reported = true
		}
	}

	if !reported {
		t.Error("a tool failure must reach the model so it can adapt")
	}
}

func TestEventsAreEmitted(t *testing.T) {
	engine, err := New(Options{
		Client:   stub(t, []string{text("hello"), stop()}),
		Messages: []Message{{Type: TypeUser, Text: "go"}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var kinds []EventKind

	engine.Run(context.Background(), func(event Event) {
		kinds = append(kinds, event.Kind)
	})

	want := map[EventKind]bool{EventIteration: false, EventToken: false, EventMessage: false}

	for _, kind := range kinds {
		if _, tracked := want[kind]; tracked {
			want[kind] = true
		}
	}

	for kind, seen := range want {
		if !seen {
			t.Errorf("no %s event was emitted", kind)
		}
	}
}

func TestInstructionsRendersSkillsAndSettleInstruction(t *testing.T) {
	engine, err := New(Options{
		Client:       stub(t, []string{stop()}),
		Instructions: "you are an agent",
		Skills:       []Skill{{Name: "deploy", Description: "ship it", Path: "/skills/deploy/SKILL.md"}},
		MaxSettles:   5,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	instructions := engine.instructions()

	for _, want := range []string{
		"you are an agent",
		"<available_skills>",
		"<name>deploy</name>",
		"/skills/deploy/SKILL.md",
		SuccessTool,
		FailureTool,
	} {
		if !strings.Contains(instructions, want) {
			t.Errorf("instructions is missing %q:\n%s", want, instructions)
		}
	}
}

func TestInstructionsOmitsSettleInstructionWhenOff(t *testing.T) {
	engine, err := New(Options{Client: stub(t, []string{stop()}), Instructions: "plain"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if strings.Contains(engine.instructions(), SuccessTool) {
		t.Error("the settle instruction must not appear when settle mode is off")
	}
}

func TestToolDefinitionsAddTerminalToolsInSettleMode(t *testing.T) {
	options := Options{
		Client:     stub(t, []string{stop()}),
		Tools:      map[string]ToolDefinition{"echo": {Name: "echo"}},
		MaxSettles: 5,
	}

	engine, err := New(options)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	names := map[string]bool{}

	for _, tool := range engine.toolDefinitions() {
		names[tool.Function.Name] = true
	}

	for _, want := range []string{"echo", SuccessTool, FailureTool} {
		if !names[want] {
			t.Errorf("tool %q missing from the definitions", want)
		}
	}

	options.MaxSettles = 0

	engine, _ = New(options)

	for _, tool := range engine.toolDefinitions() {
		if tool.Function.Name == SuccessTool {
			t.Error("terminal tools must not be offered outside settle mode")
		}
	}
}
