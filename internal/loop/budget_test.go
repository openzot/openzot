package loop

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openzot/openzot/internal/provider"
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

// A retriable provider failure has to be waited out, not hammered. Retrying
// instantly spends the whole continuation budget inside a single outage - twenty
// round trips in a few milliseconds - so a run dies to a blip that a short pause
// would have outlived, and the retries pile onto an endpoint that is already
// failing.
func TestRetriableFailuresAreSpacedOut(t *testing.T) {
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))

	t.Cleanup(failing.Close)

	client, err := provider.New(provider.Config{
		Provider: provider.Custom,
		Model:    "test-model",
		APIKey:   "k",
		BaseURL:  failing.URL,
	})
	if err != nil {
		t.Fatalf("provider.New: %v", err)
	}

	started := time.Now()

	result := run(t, Options{
		Client:           client,
		Messages:         []Message{{Type: TypeUser, Text: "go"}},
		MaxContinuations: 3,
		MaxIterations:    50,
		RetryBackoff:     20 * time.Millisecond,
	})

	elapsed := time.Since(started)

	if result.Reason != StopError {
		t.Fatalf("reason = %q, want the run to end on the provider failure", result.Reason)
	}

	if result.Budget.Continuations != 3 {
		t.Fatalf("continuations = %d, want the budget spent", result.Budget.Continuations)
	}

	// 20ms, then 40ms, then 80ms: the doubling means three retries cannot fit
	// into anything close to the zero delay they used to take.
	if want := 100 * time.Millisecond; elapsed < want {
		t.Errorf("three retries took %s, want at least %s of backoff between them", elapsed, want)
	}
}

// Cancelling a run must cut a backoff short rather than making the caller wait
// out a pause that no longer has a retry at the end of it.
func TestBackoffEndsWhenTheRunIsCancelled(t *testing.T) {
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))

	t.Cleanup(failing.Close)

	client, err := provider.New(provider.Config{
		Provider: provider.Custom,
		Model:    "test-model",
		APIKey:   "k",
		BaseURL:  failing.URL,
	})
	if err != nil {
		t.Fatalf("provider.New: %v", err)
	}

	engine, err := New(Options{
		Client:           client,
		Messages:         []Message{{Type: TypeUser, Text: "go"}},
		MaxContinuations: 5,
		RetryBackoff:     time.Hour,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	started := time.Now()
	result := engine.Run(ctx, nil)

	if elapsed := time.Since(started); elapsed > 30*time.Second {
		t.Fatalf("cancellation took %s to end an hour-long backoff", elapsed)
	}

	if result.Reason != StopAborted {
		t.Errorf("reason = %q, want the cancellation to end the run", result.Reason)
	}
}

// The default backoff must be a real pause: a zero default would silently
// restore the tight retry loop. Asserted on the constructed engine because
// reaching it behaviourally costs a second of wall clock per retry.
func TestRetryBackoffDefaultsToARealPause(t *testing.T) {
	engine, err := New(Options{Client: stub(t, []string{stop()})})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if engine.retryBackoff <= 0 {
		t.Errorf("default retry backoff = %s, want a positive pause", engine.retryBackoff)
	}

	// and a caller can still opt out, which is what keeps these tests fast
	engine, err = New(Options{Client: stub(t, []string{stop()}), RetryBackoff: -1})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if got := backoffFor(engine.retryBackoff, 1); got != 0 {
		t.Errorf("opted-out backoff = %s, want none", got)
	}
}

// The pause doubles per consecutive retry so a persistent outage is not retried
// at the same rate as a one-off blip, and is capped so a long continuation
// budget cannot leave a run asleep for hours.
func TestBackoffDoublesAndIsCapped(t *testing.T) {
	base := time.Second

	if got := backoffFor(base, 1); got != base {
		t.Errorf("first retry waits %s, want %s", got, base)
	}

	if got := backoffFor(base, 2); got != 2*base {
		t.Errorf("second retry waits %s, want %s", got, 2*base)
	}

	if got := backoffFor(base, 3); got != 4*base {
		t.Errorf("third retry waits %s, want %s", got, 4*base)
	}

	if got := backoffFor(base, 40); got != MaxRetryBackoff {
		t.Errorf("a long outage waits %s, want the cap %s", got, MaxRetryBackoff)
	}

	// the cap binds the base too: a caller-configured backoff above it must not
	// make the first retry the longest wait of the run
	if got := backoffFor(2*MaxRetryBackoff, 1); got != MaxRetryBackoff {
		t.Errorf("a base above the cap waits %s on the first retry, want the cap %s", got, MaxRetryBackoff)
	}
}

// A rate limit must not kill a run. 429 is deliberately excluded from
// IsRetriable because it needs the provider's own schedule rather than a tight
// loop - but nothing waited on that schedule, so the loop fell straight through
// to StopError. One throttle response at iteration 400 of an overnight run ended
// it, with the whole continuation budget unspent.
func TestARateLimitIsWaitedOutRatherThanFatal(t *testing.T) {
	var (
		mu    sync.Mutex
		calls int
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		calls++
		first := calls == 1
		mu.Unlock()

		if first {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, `{"error":{"message":"slow down"}}`)

			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: %s\n\n", tool("c1", SuccessTool, `{"summary":"done anyway"}`))
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

	started := time.Now()

	result := run(t, Options{
		Client:     client,
		Messages:   []Message{{Type: TypeUser, Text: "go"}},
		MaxSettles: 5,
	})

	elapsed := time.Since(started)

	if result.Reason != StopSettled {
		t.Fatalf("reason = %q (%v), want the run to survive the rate limit", result.Reason, result.Err)
	}

	if result.Budget.Continuations != 1 {
		t.Errorf("continuations = %d, want the rate limit to cost exactly one", result.Budget.Continuations)
	}

	// the provider asked for a second; honouring that is the whole point, so a
	// retry that came back sooner means the advice was ignored
	if elapsed < time.Second {
		t.Errorf("retried after %s, want the advised second to be waited out", elapsed)
	}
}

// A provider that advises an absurd Retry-After must not park an unattended run
// for hours: the advice is honoured up to a cap, and no further.
func TestAnAbsurdRetryAfterIsCapped(t *testing.T) {
	if got := rateLimitWait(48*time.Hour, true, time.Second); got != MaxRateLimitWait {
		t.Errorf("wait = %s, want the cap %s", got, MaxRateLimitWait)
	}

	// advice inside the cap is followed exactly, rather than rounded to our own
	// backoff schedule
	if got := rateLimitWait(90*time.Second, true, time.Second); got != 90*time.Second {
		t.Errorf("wait = %s, want the advised 90s", got)
	}

	// and with no advice at all the ordinary backoff applies
	if got := rateLimitWait(0, false, 4*time.Second); got != 4*time.Second {
		t.Errorf("wait = %s, want the fallback backoff", got)
	}
}

// The backoff is a floor under the provider's advice, not just a fallback for
// its absence. "Retry-After: 0" (or a date already past) is advice to retry
// now - and a provider that keeps sending it while still answering 429 would
// otherwise be hammered with instant retries, the exact tight loop the backoff
// exists to prevent.
func TestAZeroRetryAfterIsFlooredByTheBackoff(t *testing.T) {
	if got := rateLimitWait(0, true, 4*time.Second); got != 4*time.Second {
		t.Errorf("wait = %s, want the 4s backoff floor under \"retry now\"", got)
	}

	// advice above the floor still wins: the provider knows its own window
	if got := rateLimitWait(90*time.Second, true, 4*time.Second); got != 90*time.Second {
		t.Errorf("wait = %s, want the advised 90s over the smaller backoff", got)
	}
}

// And end to end: repeated 429s advising "retry now" must still space their
// retries out on the backoff schedule rather than burning the continuation
// budget in milliseconds.
func TestRepeated429WithZeroRetryAfterStillBacksOff(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"error":{"message":"slow down"}}`)
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

	started := time.Now()

	result := run(t, Options{
		Client:           client,
		Messages:         []Message{{Type: TypeUser, Text: "go"}},
		MaxContinuations: 3,
		MaxIterations:    50,
		RetryBackoff:     20 * time.Millisecond,
	})

	elapsed := time.Since(started)

	if result.Reason != StopError {
		t.Fatalf("reason = %q, want the run to end once the budget is spent", result.Reason)
	}

	if result.Budget.Continuations != 3 {
		t.Fatalf("continuations = %d, want the budget spent", result.Budget.Continuations)
	}

	// 20ms, then 40ms, then 80ms: the advised zero must not undercut the floor
	if want := 100 * time.Millisecond; elapsed < want {
		t.Errorf("three rate-limited retries took %s, want at least %s of backoff between them", elapsed, want)
	}
}

// The backoff paces *consecutive* failures. Once a turn succeeds, the outage it
// was pacing is over, and the next blip - hours later, in a long run - must
// start again from the base delay rather than from wherever the last outage
// left the schedule.
func TestBackoffRestartsAfterASuccessfulTurn(t *testing.T) {
	var (
		mu       sync.Mutex
		requests int
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		requests++
		request := requests
		mu.Unlock()

		switch request {
		case 1, 2:
			// a two-deep outage: retries wait base, then 2x base
			w.WriteHeader(http.StatusInternalServerError)

		case 3:
			// a successful tool round - the outage is over
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "data: %s\n\n", tool("c1", "echo", `{}`))
			fmt.Fprint(w, "data: [DONE]\n\n")

		case 4:
			// a fresh, unrelated blip: it must wait base again, not 4x base
			w.WriteHeader(http.StatusInternalServerError)

		default:
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "data: %s\n\n", tool("c2", SuccessTool, `{"summary":"done"}`))
			fmt.Fprint(w, "data: [DONE]\n\n")
		}
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

	calls := 0

	base := 300 * time.Millisecond

	started := time.Now()

	result := run(t, Options{
		Client:           client,
		Tools:            echoTool(&calls),
		Messages:         []Message{{Type: TypeUser, Text: "go"}},
		MaxContinuations: 10,
		MaxIterations:    20,
		MaxSettles:       5,
		RetryBackoff:     base,
	})

	elapsed := time.Since(started)

	if result.Reason != StopSettled {
		t.Fatalf("reason = %q (%v), want the run to finish", result.Reason, result.Err)
	}

	if result.Budget.Continuations != 3 {
		t.Fatalf("continuations = %d, want 3", result.Budget.Continuations)
	}

	// base + 2x base + base = 4x base when the counter resets on success; a
	// counter that kept escalating would wait base + 2x + 4x = 7x base. The
	// bound sits between the two with generous slack for a loaded machine.
	if floor := 4 * base; elapsed < floor {
		t.Fatalf("the retries took %s, want at least %s of backoff", elapsed, floor)
	}

	if ceiling := 6 * base; elapsed > ceiling {
		t.Errorf("the retries took %s, want under %s - the backoff must restart from the base after a successful turn", elapsed, ceiling)
	}
}

// The consecutive-failure counter is the backoff's own, not the continuation
// budget. That budget is also spent by truncation recoveries (and context-limit
// compactions), so keying the backoff off it made an unrelated first blip start
// at an escalated wait - here, 8x base for a run whose only prior continuations
// were truncated answers.
func TestOtherContinuationsDoNotEscalateTheBackoff(t *testing.T) {
	var (
		mu       sync.Mutex
		requests int
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		requests++
		request := requests
		mu.Unlock()

		switch request {
		case 1, 2, 3:
			// truncated answers: each spends a continuation, none is a failure
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "data: %s\n\n", text("more to say"))
			fmt.Fprintf(w, "data: %s\n\n", truncated())
			fmt.Fprint(w, "data: [DONE]\n\n")

		case 4:
			// the run's first retriable failure: it must wait base, not 8x base
			w.WriteHeader(http.StatusInternalServerError)

		default:
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "data: %s\n\n", text("done"))
			fmt.Fprintf(w, "data: %s\n\n", stop())
			fmt.Fprint(w, "data: [DONE]\n\n")
		}
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

	base := 300 * time.Millisecond

	started := time.Now()

	result := run(t, Options{
		Client:           client,
		Messages:         []Message{{Type: TypeUser, Text: "go"}},
		MaxContinuations: 10,
		MaxIterations:    20,
		RetryBackoff:     base,
	})

	elapsed := time.Since(started)

	if result.Reason != StopStop {
		t.Fatalf("reason = %q (%v), want the run to finish", result.Reason, result.Err)
	}

	if result.Budget.Continuations != 4 {
		t.Fatalf("continuations = %d, want 3 truncations plus 1 retry", result.Budget.Continuations)
	}

	// one failure, one wait of base. Keyed off the shared budget it would have
	// been 8x base; the bound leaves generous slack for a loaded machine.
	if floor := base; elapsed < floor {
		t.Fatalf("the retry took %s, want at least the %s base backoff", elapsed, floor)
	}

	if ceiling := 4 * base; elapsed > ceiling {
		t.Errorf("the retry took %s, want under %s - truncation recoveries must not escalate the failure backoff", elapsed, ceiling)
	}
}
