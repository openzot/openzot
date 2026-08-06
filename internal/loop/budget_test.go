package loop

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// Budget semantics, ported from the TypeScript engine's maxIterations and
// recursion suites.
//
// The budgets look interchangeable and are not. Iterations count every trip
// round the loop; continuations count only the times the model was cut off
// mid-answer. Conflating them is how a run that is making steady progress
// through tool calls gets killed for "running out of output space", and how a
// model that never stops being truncated runs forever. Both were real.

// A time cap ends a run at the iteration boundary. Unbounded by default, but a
// run pinned to a deadline must stop with StopTime rather than running to the
// iteration backstop.
func TestATimeBudgetStopsTheRun(t *testing.T) {
	calls := 0

	result := run(t, Options{
		// a stub that calls a tool forever
		Client:        stub(t, []string{tool("call_1", "echo", "{}")}),
		Tools:         echoTool(&calls),
		MaxDuration:   time.Millisecond,
		MaxIterations: 100000, // high, so time is what stops it, not iterations
		MaxCycles:     100000, // high, so cycle detection is not what stops it
	})

	if result.Reason != StopTime {
		t.Errorf("Reason = %q, want %q", result.Reason, StopTime)
	}

	// it did some work before the deadline, and nowhere near the iteration cap
	if result.Budget.Iterations >= 100000 {
		t.Errorf("Iterations = %d, want the time cap to bite first", result.Budget.Iterations)
	}
}

// With no time cap, a run is never stopped for time - the default is unbounded.
func TestTimeIsUnboundedByDefault(t *testing.T) {
	result := run(t, Options{
		Client:        stub(t, []string{text("done"), stop()}),
		MaxIterations: 5,
	})

	if result.Reason == StopTime {
		t.Error("a run with no time cap must never stop for time")
	}
}

// A tool round is progress. It costs an iteration and a call, and nothing else.
func TestToolRoundsDoNotSpendTheContinuationBudget(t *testing.T) {
	calls := 0

	result := run(t, Options{
		Client: stub(t,
			[]string{tool("call_1", "echo", "{}")},
			[]string{tool("call_2", "echo", "{}")},
			[]string{text("done"), stop()},
		),
		Tools:            echoTool(&calls),
		MaxIterations:    10,
		MaxContinuations: 1,
	})

	if result.Budget.Continuations != 0 {
		t.Errorf("tool rounds spent %d continuations, want 0", result.Budget.Continuations)
	}

	if result.Budget.Calls != 2 {
		t.Errorf("Calls = %d, want 2", result.Budget.Calls)
	}

	if result.Reason != StopStop {
		t.Errorf("Reason = %q, want the run to finish normally", result.Reason)
	}
}

// Being cut off mid-answer is not progress, and it is the only thing the
// continuation budget is there to bound.
func TestTruncationSpendsTheContinuationBudget(t *testing.T) {
	result := run(t, Options{
		Client: stub(t,
			[]string{text("half an ans"), truncated()},
			[]string{text("wer"), stop()},
		),
		MaxIterations: 10,
	})

	if result.Budget.Continuations != 1 {
		t.Errorf("Continuations = %d, want 1", result.Budget.Continuations)
	}
}

// The two budgets are independent: a run can exhaust one while the other is
// barely touched, and neither may end the run on the other's behalf.
func TestTheTwoBudgetsAreIndependent(t *testing.T) {
	calls := 0

	// tool calls forever, with a continuation budget of one
	result := run(t, Options{
		Client:           stub(t, []string{tool("call_1", "echo", "{}")}),
		Tools:            echoTool(&calls),
		MaxIterations:    4,
		MaxContinuations: 1,
		MaxCycles:        1000,
	})

	if result.Reason != StopIterations {
		t.Errorf("Reason = %q, want the iteration budget to be what stops it", result.Reason)
	}

	if result.Budget.Continuations != 0 {
		t.Errorf("Continuations = %d, want the continuation budget untouched", result.Budget.Continuations)
	}

	// and the other way round: truncated forever, with plenty of iterations
	result = run(t, Options{
		Client:           stub(t, []string{text("more"), truncated()}),
		MaxIterations:    50,
		MaxContinuations: 3,
	})

	if result.Reason != StopContinuations {
		t.Errorf("Reason = %q, want the continuation budget to be what stops it", result.Reason)
	}

	if result.Budget.Iterations >= 50 {
		t.Errorf("Iterations = %d, want the continuation budget to bite first", result.Budget.Iterations)
	}
}

// Everything that goes round the loop costs an iteration - tool rounds,
// truncation retries, empty turns and settle nudges alike. It is the backstop
// that bounds a run no matter which way the model misbehaves.
func TestEveryKindOfRoundCostsAnIteration(t *testing.T) {
	tests := []struct {
		name  string
		turns [][]string
		tools map[string]ToolDefinition
	}{
		{
			name:  "tool rounds",
			turns: [][]string{{tool("call_1", "echo", "{}")}},
			tools: echoTool(new(int)),
		},
		{
			name:  "truncation retries",
			turns: [][]string{{text("more"), truncated()}},
		},
		{
			name:  "empty turns",
			turns: [][]string{{stop()}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := run(t, Options{
				Client:           stub(t, test.turns...),
				Tools:            test.tools,
				MaxIterations:    3,
				MaxCalls:         100,
				MaxContinuations: 100,
				MaxEmpties:       100,
			})

			if result.Budget.Iterations != 3 {
				t.Errorf("Iterations = %d, want the budget spent", result.Budget.Iterations)
			}

			if result.Reason != StopIterations {
				t.Errorf("Reason = %q, want %q", result.Reason, StopIterations)
			}
		})
	}
}

// One iteration is single-step mode: one model call, then stop. It is what a
// caller uses to drive the loop themselves.
func TestASingleIterationIsOneModelCall(t *testing.T) {
	calls := 0

	result := run(t, Options{
		Client:        stub(t, []string{tool("call_1", "echo", "{}")}),
		Tools:         echoTool(&calls),
		MaxIterations: 1,
		MaxCycles:     1000,
	})

	if result.Budget.Iterations != 1 {
		t.Errorf("Iterations = %d, want 1", result.Budget.Iterations)
	}

	if calls != 1 {
		t.Errorf("the tool ran %d times, want once", calls)
	}

	if result.Reason != StopIterations {
		t.Errorf("Reason = %q, want %q", result.Reason, StopIterations)
	}
}

// A non-positive budget means "unset", not "zero".
//
// The distinction: the iteration count and the no-progress guards (cycles,
// empties) are hard backstops - a non-positive value must never leave them
// unbounded, because that is how a runaway run fails dangerously. The call and
// time budgets are the opposite: unbounded is their intended default, so a
// non-positive value means exactly that.
func TestBudgetDefaults(t *testing.T) {
	for _, value := range []int{0, -1, -1000} {
		engine, err := New(Options{
			Client:        stub(t, []string{stop()}),
			MaxCalls:      value,
			MaxIterations: value,
			MaxCycles:     value,
			MaxEmpties:    value,
		})
		if err != nil {
			t.Fatalf("New: %v", err)
		}

		// backstops fall back to their finite defaults
		if engine.maxIterations != DefaultMaxIterations {
			t.Errorf("a budget of %d left iterations at %d, want the default backstop",
				value, engine.maxIterations)
		}

		if engine.maxCycles <= 0 || engine.maxEmpties <= 0 {
			t.Errorf("a budget of %d left a guard unbounded: cycles=%d empties=%d",
				value, engine.maxCycles, engine.maxEmpties)
		}

		// calls stays unbounded - a non-positive value is "no cap", not a default
		if engine.maxCalls != 0 {
			t.Errorf("a budget of %d gave maxCalls=%d, want 0 (unbounded)", value, engine.maxCalls)
		}
	}
}

// Deep agentic loops must be stack-safe. The TypeScript engine recursed per
// round and had to be rewritten when a long run overflowed; this documents that
// zot's loop is iterative and cannot.
func TestADeepRunDoesNotGrowTheStack(t *testing.T) {
	calls := 0

	result := run(t, Options{
		Client:        stub(t, []string{tool("call_1", "echo", "{}")}),
		Tools:         echoTool(&calls),
		MaxIterations: 500,
		MaxCalls:      500,
		MaxCycles:     1000,
	})

	if result.Budget.Iterations != 500 {
		t.Errorf("Iterations = %d, want the full 500 rounds", result.Budget.Iterations)
	}

	if calls < 400 {
		t.Errorf("the tool ran %d times over 500 rounds", calls)
	}
}

// Malformed arguments are the model's mistake to correct. Invoking the handler
// with a half-decoded map would be worse than not invoking it at all.
func TestMalformedArgumentsReachTheModelNotTheHandler(t *testing.T) {
	invoked := 0

	tools := map[string]ToolDefinition{
		"echo": {
			Name:       "echo",
			Parameters: map[string]any{"type": "object"},
			Handler: func(context.Context, map[string]any) (any, error) {
				invoked++

				return "ok", nil
			},
		},
	}

	result := run(t, Options{
		Client: stub(t,
			[]string{tool("call_1", "echo", `{"value": `)},
			[]string{text("let me try that again"), stop()},
		),
		Tools:         tools,
		MaxIterations: 5,
	})

	if invoked != 0 {
		t.Errorf("the handler ran %d times on undecodable arguments", invoked)
	}

	if !mentionsAFailure(result.Messages) {
		t.Error("the decode failure must be fed back so the model can correct it")
	}

	if result.Reason != StopStop {
		t.Errorf("Reason = %q, want the run to carry on", result.Reason)
	}
}

// A tool that fails is information, not an outage. The run continues with the
// error in hand.
func TestAFailingToolIsReportedAndTheRunContinues(t *testing.T) {
	tools := map[string]ToolDefinition{
		"echo": {
			Name:       "echo",
			Parameters: map[string]any{"type": "object"},
			Handler: func(context.Context, map[string]any) (any, error) {
				return nil, errors.New("permission denied")
			},
		},
	}

	result := run(t, Options{
		Client: stub(t,
			[]string{tool("call_1", "echo", "{}")},
			[]string{text("understood"), stop()},
		),
		Tools:         tools,
		MaxIterations: 5,
	})

	if result.Reason != StopStop {
		t.Errorf("Reason = %q, want the run to survive a failing tool", result.Reason)
	}

	if !containsText(result.Messages, "permission denied") {
		t.Error("the failure must be visible to the model")
	}
}

// A handler that returns nothing still has to produce a result message, or the
// call is left unanswered and the next request is invalid.
func TestAHandlerReturningNothingStillAnswersTheCall(t *testing.T) {
	tools := map[string]ToolDefinition{
		"echo": {
			Name:       "echo",
			Parameters: map[string]any{"type": "object"},
			Handler: func(context.Context, map[string]any) (any, error) {
				return nil, nil
			},
		},
	}

	result := run(t, Options{
		Client: stub(t,
			[]string{tool("call_1", "echo", "{}")},
			[]string{text("done"), stop()},
		),
		Tools:         tools,
		MaxIterations: 5,
	})

	requests, responses := countActivities(result.Messages)

	if requests != responses || requests == 0 {
		t.Errorf("got %d calls and %d results, want them paired", requests, responses)
	}
}

// A finish reason zot has no special handling for - content_filter is the one
// providers actually send - must not derail the run.
func TestAnUnrecognisedFinishReasonIsNotFatal(t *testing.T) {
	result := run(t, Options{
		Client: stub(t, []string{
			text("I cannot help with that"),
			`{"choices":[{"delta":{},"finish_reason":"content_filter"}]}`,
		}),
		MaxIterations: 5,
	})

	if result.Reason != StopStop {
		t.Errorf("Reason = %q, want the turn treated as an ending", result.Reason)
	}

	if result.Err != nil {
		t.Errorf("Err = %v, want none", result.Err)
	}
}

// A turn that claims tool calls and carries none is a provider bug. It has to
// degrade to an empty turn rather than panic on the missing payload.
func TestAToolCallFinishWithNoCallsIsNotFatal(t *testing.T) {
	result := run(t, Options{
		Client: stub(t, []string{
			`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		}),
		MaxIterations: 3,
		MaxEmpties:    2,
	})

	if result.Err != nil {
		t.Errorf("Err = %v, want none", result.Err)
	}

	if result.Reason != StopEmpty && result.Reason != StopIterations {
		t.Errorf("Reason = %q, want the turn treated as empty", result.Reason)
	}
}

// containsText reports whether any message holds the given text.
func containsText(messages []Message, want string) bool {
	for _, message := range messages {
		if strings.Contains(message.Text, want) {
			return true
		}
	}

	return false
}

// mentionsAFailure reports whether any tool result carries an error.
func mentionsAFailure(messages []Message) bool {
	for _, message := range messages {
		if message.Activity != nil && message.Activity.Failure != "" {
			return true
		}
	}

	return false
}

// countActivities counts each half of the tool-call pairs.
func countActivities(messages []Message) (requests, responses int) {
	for _, message := range messages {
		if message.Activity == nil {
			continue
		}

		switch message.Activity.Kind {
		case ActivityRequest:
			requests++
		case ActivityResponse:
			responses++
		}
	}

	return requests, responses
}

// The point of the whole feature: as a run approaches a bounded limit, the model
// is told, at each configured checkpoint, so it can pace itself and finish
// rather than being cut off mid-task. Here the iteration budget is 10 and the
// checkpoints are 50% and 80%, so the model must be warned at iteration 5 and 8.
func TestIterationCheckpointsFireAsTheLimitApproaches(t *testing.T) {
	result := run(t, Options{
		Client:           stub(t, []string{tool("call_1", "echo", "{}")}),
		Tools:            echoTool(new(int)),
		MaxIterations:    10,
		MaxCycles:        100000, // don't let cycle detection stop it early
		LimitCheckpoints: []int{50, 80},
	})

	if result.Reason != StopIterations {
		t.Fatalf("Reason = %q, want the run to reach the iteration cap", result.Reason)
	}

	fired := 0

	for _, m := range result.Messages {
		if strings.Contains(m.Text, "through your step budget") {
			fired++
		}
	}

	if fired != 2 {
		t.Errorf("iteration checkpoints fired %d times, want 2 (at 50%% and 80%%)", fired)
	}
}

// Tool-call checkpoints fire against the call budget, independently of
// iterations - a single iteration can burn many calls.
func TestCallCheckpointsFire(t *testing.T) {
	result := run(t, Options{
		Client:           stub(t, []string{tool("call_1", "echo", "{}")}),
		Tools:            echoTool(new(int)),
		MaxCalls:         10,
		MaxIterations:    1000,
		MaxCycles:        100000,
		LimitCheckpoints: []int{80},
	})

	fired := 0

	for _, m := range result.Messages {
		if strings.Contains(m.Text, "through your tool-call budget") {
			fired++
		}
	}

	if fired == 0 {
		t.Error("the tool-call budget checkpoint never fired")
	}
}

// An unbounded limit has nothing to approach, so it never fires a checkpoint -
// only the finite iteration backstop does.
func TestUnboundedLimitsHaveNoCheckpoints(t *testing.T) {
	result := run(t, Options{
		Client:           stub(t, []string{tool("call_1", "echo", "{}")}),
		Tools:            echoTool(new(int)),
		MaxIterations:    6,
		MaxCalls:         0, // unbounded
		MaxCycles:        100000,
		LimitCheckpoints: []int{50},
	})

	for _, m := range result.Messages {
		if strings.Contains(m.Text, "through your tool-call budget") {
			t.Error("an unbounded call budget must not fire a checkpoint")
		}
	}
}

// An explicit empty checkpoint list turns the notices off entirely.
func TestCheckpointsCanBeDisabled(t *testing.T) {
	result := run(t, Options{
		Client:           stub(t, []string{tool("call_1", "echo", "{}")}),
		Tools:            echoTool(new(int)),
		MaxIterations:    6,
		MaxCycles:        100000,
		LimitCheckpoints: []int{}, // disabled
	})

	for _, m := range result.Messages {
		if strings.Contains(m.Text, "through your") {
			t.Errorf("checkpoints were disabled but a notice fired: %q", m.Text)
		}
	}
}

// Each checkpoint fires at most once, even though the run passes it on many
// subsequent iterations.
func TestEachCheckpointFiresOnce(t *testing.T) {
	result := run(t, Options{
		Client:           stub(t, []string{tool("call_1", "echo", "{}")}),
		Tools:            echoTool(new(int)),
		MaxIterations:    20,
		MaxCycles:        100000,
		LimitCheckpoints: []int{50},
	})

	fired := 0

	for _, m := range result.Messages {
		if strings.Contains(m.Text, "through your step budget") {
			fired++
		}
	}

	if fired != 1 {
		t.Errorf("the 50%% checkpoint fired %d times, want exactly once", fired)
	}
}
