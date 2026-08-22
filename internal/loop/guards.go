// Package loop runs the agentic conversation: call the model, execute the tools
// it asks for, feed the results back, repeat until the task settles.
//
// Most of this file is bounds. Each one exists because an unbounded agent fails
// in a specific, expensive way - burning a token budget on a loop it cannot see,
// retrying an error that will never succeed, or declaring victory because it
// happened to use the word "completed".
package loop

import (
	"sort"
	"time"
)

// Bounds on a single run. Every default here encodes a failure that happened.
const (
	// DefaultMaxIterations caps agentic rounds - one model call plus the tools
	// it requests. The loop is iterative rather than recursive, so any value is
	// safe; this is a behavioural bound, not a stack one.
	DefaultMaxIterations = 1000

	// DefaultMaxCalls caps total tool calls across a run, independently of
	// rounds: a single round can request many tools.
	DefaultMaxCalls = 1000

	// DefaultMaxContinuations caps CONSECUTIVE recovery attempts - output
	// truncated, or a retriable provider error - with no good turn between
	// them. Distinct from iterations, which count normal progress.
	//
	// Consecutive, not lifetime, because a recovery that worked is not
	// evidence of anything. A run-lifetime count meant twenty transient blips
	// spread over four hours - each one recovered from, each one followed by
	// real work - left the run with no budget at all, and the twenty-first
	// blip ended it however healthy it was. The bound is meant to catch a run
	// that cannot get going again, so it asks that question and no other: it
	// zeroes the moment a turn comes back whole, exactly like the empty and
	// cycle counters.
	DefaultMaxContinuations = 20

	// DefaultMaxRecoveries caps recovery attempts across a whole run, however
	// they are spaced: the point at which a provider stops being given the
	// benefit of the doubt.
	//
	// MaxContinuations catches a provider refusing right now - twenty in a row
	// and the run is stuck. It cannot catch the other shape: one that answers
	// often enough to keep resetting the consecutive count, while the run
	// spends most of its life retrying rather than working. Every good turn
	// says the upstream is fine and the tally says it is not, and without this
	// the tally is never consulted.
	//
	// So the number has to be one a run can actually reach. A healthy run
	// spends recoveries in tens - a truncated answer continued, the occasional
	// 500, a context-limit compaction - across an iteration budget that
	// defaults to a thousand. Two hundred is a fifth of that budget spent on
	// recovery instead of progress, which no working provider does. Set high
	// enough not to punish a long, output-heavy run that legitimately
	// continues a lot; low enough that a chronically failing endpoint is
	// called what it is instead of nursed for hours.
	//
	// Deliberately absolute rather than a multiple of MaxContinuations. The two
	// answer unrelated questions, and tying them together means lowering the
	// consecutive bound - the obvious thing to want, to fail fast on a stuck
	// provider - silently lowers this one into a range a long healthy run
	// reaches, which is the accumulating budget the consecutive count exists to
	// get rid of.
	DefaultMaxRecoveries = 200

	// DefaultMaxCycles is how many times the loop will nudge a model out of a
	// detected repetition before giving up on it.
	DefaultMaxCycles = 2

	// DefaultRetryBackoff is the pause before the first retry of a retriable
	// provider failure; each consecutive retry doubles it, up to MaxRetryBackoff.
	//
	// Retrying instantly is worse than not retrying: a provider outage burns the
	// whole continuation budget inside a few milliseconds - so a run dies to a
	// blip it would have outlived - while hammering an endpoint that is already
	// failing. The delay is what turns the continuation budget into a window of
	// time rather than a count of round trips.
	DefaultRetryBackoff = 1 * time.Second

	// MaxRetryBackoff caps the doubling, so a long continuation budget cannot
	// leave a run asleep for hours on an outage that has already ended.
	MaxRetryBackoff = 30 * time.Second

	// MaxRateLimitWait caps how long a run will sit out a provider-advised
	// Retry-After. Sitting out a real rate-limit window is the right thing for an
	// unattended run - it is why a 429 no longer ends one - but no legitimate
	// window needs longer than this, and a mistaken or hostile header must not be
	// able to park a run for hours.
	MaxRateLimitWait = 5 * time.Minute

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
	SuccessTool = "success"
	FailureTool = "failure"
)

// StopReason explains why a run ended.
type StopReason string

const (
	// StopSettled - the model called the success tool. The only clean ending in
	// settle mode.
	StopSettled StopReason = "settled"

	// StopFailed - the model called the failure tool: it reached a conclusion,
	// and the conclusion is that the task cannot be done. A settled ending, but
	// not a successful one, so it must never be reported as StopSettled is.
	StopFailed StopReason = "failed"

	// StopStop - the model finished talking and settle mode is off.
	StopStop StopReason = "stop"

	// StopIterations - the round budget ran out.
	StopIterations StopReason = "iterations"

	// StopCalls - the tool-call budget ran out.
	StopCalls StopReason = "calls"

	// StopContinuations - too many recovery attempts, either in a row or
	// across the run.
	StopContinuations StopReason = "continuations"

	// StopCycle - the model kept repeating itself after being nudged.
	StopCycle StopReason = "cycle"

	// StopTime - the wall-clock time budget ran out.
	StopTime StopReason = "time"

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
	Iterations int
	Calls      int

	// Continuations counts CONSECUTIVE recovery attempts; a turn that comes
	// back whole zeroes it. Recoveries is the total across the run, which only
	// ever rises - it is what the run is reported to have spent, and what
	// DefaultMaxRecoveries backstops.
	Continuations int
	Recoveries    int

	Cycles  int
	Empties int
	Settles int

	// InputTokens and OutputTokens accumulate the provider-reported prompt and
	// completion tokens across the run - the actual billed usage, not the local
	// estimate. Each model call bills its full prompt, so these are summed per
	// turn.
	InputTokens  int
	OutputTokens int

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

// spendContinuation records one recovery attempt against both counts: the
// consecutive run of them, and the total across the run.
func (b *Budget) spendContinuation() {
	b.Continuations++
	b.Recoveries++
}
