// Package loop runs the agentic conversation: call the model, execute the tools
// it asks for, feed the results back, repeat until the task settles.
//
// Most of this file is bounds. Each one exists because an unbounded agent fails
// in a specific, expensive way - burning a token budget on a loop it cannot see,
// retrying an error that will never succeed, or declaring victory because it
// happened to use the word "completed".
package loop

// Bounds on a single run. Every default here encodes a failure that happened.
const (
	// DefaultMaxIterations caps agentic rounds - one model call plus the tools
	// it requests. The loop is iterative rather than recursive, so any value is
	// safe; this is a behavioural bound, not a stack one.
	DefaultMaxIterations = 1000

	// DefaultMaxCalls caps total tool calls across a run, independently of
	// rounds: a single round can request many tools.
	DefaultMaxCalls = 1000

	// DefaultMaxContinuations caps recovery attempts - output truncated, an
	// empty turn, or a retriable provider error. Distinct from iterations,
	// which count normal progress.
	DefaultMaxContinuations = 20

	// DefaultMaxCycles is how many times the loop will nudge a model out of a
	// detected repetition before giving up on it.
	DefaultMaxCycles = 2

	// DefaultMaxEmpties caps turns that stop with neither text nor a tool call.
	// Far tighter than the continuation budget because retrying an empty turn
	// rarely recovers.
	DefaultMaxEmpties = 3

	// DefaultMaxSettles caps settle nudges. A positive MaxSettles enables settle
	// mode, where a run is finished only when the model calls a terminal tool -
	// never because its prose sounded final.
	DefaultMaxSettles = 20

	// MinInputTokens is reserved so the backstory and the tool schemas always
	// fit, however long the conversation grows.
	MinInputTokens = 10_000

	// CallBudgetLowRatio and CallBudgetLowCap decide when to warn the model that
	// it is running out of tool calls. Telling it before it runs out lets it
	// wrap up; telling it after is just a failure.
	CallBudgetLowRatio = 0.2
	CallBudgetLowCap   = 10

	// RunawayGuardMinChars is the output length below which the streaming
	// repetition guard will not trip. Short repetitive output ends on its own.
	RunawayGuardMinChars = 2_000
)

// Terminal tool names. In settle mode the model ends a run by calling one of
// these, which is unambiguous in a way prose never is.
const (
	SuccessTool = "_success"
	FailureTool = "_failure"
)

// StopReason explains why a run ended.
type StopReason string

const (
	// StopSettled - the model called a terminal tool. The only clean ending in
	// settle mode.
	StopSettled StopReason = "settled"

	// StopStop - the model finished talking and settle mode is off.
	StopStop StopReason = "stop"

	// StopIterations - the round budget ran out.
	StopIterations StopReason = "iterations"

	// StopCalls - the tool-call budget ran out.
	StopCalls StopReason = "calls"

	// StopContinuations - too many recovery attempts.
	StopContinuations StopReason = "continuations"

	// StopCycle - the model kept repeating itself after being nudged.
	StopCycle StopReason = "cycle"

	// StopEmpty - too many turns produced nothing at all.
	StopEmpty StopReason = "empty"

	// StopUnsettled - settle mode exhausted its nudges without a terminal call.
	StopUnsettled StopReason = "unsettled"

	// StopAborted - the caller cancelled.
	StopAborted StopReason = "aborted"

	// StopError - an unrecoverable failure.
	StopError StopReason = "error"
)

// Budget tracks what a run has spent.
type Budget struct {
	Iterations    int
	Calls         int
	Continuations int
	Cycles        int
	Empties       int
	Settles       int

	// budgetWarned records that the low-call-budget notice has been injected, so
	// it fires at most once per run.
	budgetWarned bool
}

// callBudgetLowThreshold is the remaining-call count at which the model is
// warned. Proportional, but capped: on a large budget a percentage would warn
// far too early to be useful.
func callBudgetLowThreshold(maxCalls int) int {
	threshold := int(float64(maxCalls) * CallBudgetLowRatio)

	if threshold > CallBudgetLowCap {
		threshold = CallBudgetLowCap
	}

	if threshold < 1 {
		threshold = 1
	}

	return threshold
}
