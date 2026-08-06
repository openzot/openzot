// Package loop runs the agentic conversation: call the model, execute the tools
// it asks for, feed the results back, repeat until the task settles.
//
// Most of this file is bounds. Each one exists because an unbounded agent fails
// in a specific, expensive way - burning a token budget on a loop it cannot see,
// retrying an error that will never succeed, or declaring victory because it
// happened to use the word "completed".
package loop

import "sort"

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

	// MinInputTokens is reserved so the instructions and the tool schemas always
	// fit, however long the conversation grows.
	MinInputTokens = 10_000

	// DefaultCompactMinTokens is the estimated-input-token floor below which the
	// compact strategy leaves the conversation whole - summarising a short thread
	// costs more than carrying it.
	DefaultCompactMinTokens = 50_000

	// DefaultCompactMinMessages is the floor on how many messages must be eligible
	// for summarising before the compact strategy runs.
	DefaultCompactMinMessages = 20

	// DefaultCompactTriggerRatio is the fraction of the context window at which the
	// compact strategy fires.
	DefaultCompactTriggerRatio = 0.9

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

	// StopTime - the wall-clock time budget ran out.
	StopTime StopReason = "time"

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

	// iterCheckpoint, callCheckpoint and timeCheckpoint record the index of the
	// next approaching-limit checkpoint to fire for each bounded limit, so each
	// checkpoint notice is injected at most once.
	iterCheckpoint int
	callCheckpoint int
	timeCheckpoint int
}

// DefaultLimitCheckpoints are the percentages of a bounded limit at which the
// model is told it is approaching that limit - once each - so it can pace itself
// and finish before the hard stop rather than being cut off mid-task with no
// warning. Configurable per run.
var DefaultLimitCheckpoints = []int{50, 80, 90}

// normalizeCheckpoints cleans a configured checkpoint list into an ascending set
// of valid percentages.
//
// Nil means "use the defaults"; a non-nil empty slice means "no checkpoints" -
// the operator turned them off. Values outside 1..99 are dropped, because 0 is
// no progress and 100 is the limit itself (already its own stop), and duplicates
// are collapsed so a list cannot fire the same notice twice.
func normalizeCheckpoints(configured []int) []int {
	if configured == nil {
		return DefaultLimitCheckpoints
	}

	seen := map[int]bool{}
	out := make([]int, 0, len(configured))

	for _, p := range configured {
		if p >= 1 && p <= 99 && !seen[p] {
			seen[p] = true

			out = append(out, p)
		}
	}

	sort.Ints(out)

	return out
}
