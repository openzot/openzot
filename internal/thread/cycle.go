package thread

import (
	"errors"
	"strings"
)

var errNoEstimator = errors.New("thread: no token estimator supplied and a message carries no usage")

// DefaultMinResultRepetitions is how many consecutive identical tool results
// HasRepeatedResultRun requires before reporting a loop.
const DefaultMinResultRepetitions = 3

// Opt returns a pointer to v, for populating the optional fields of
// CycleOptions, TextRunOptions and GuardOptions.
//
// These are pointers rather than plain ints because zero is a meaningful value
// that clamps to the minimum, and a plain int cannot tell "unset" from
// "explicitly zero" - a caller passing MinRepetitions: 0 means "as low as
// possible", not "use the default of 2".
func Opt[T any](v T) *T {
	return &v
}

// CycleOptions tunes cycle detection. The zero value is the production default.
type CycleOptions struct {
	// MinRepetitions is how many consecutive repeats of a pattern constitute a
	// cycle. At the default of 2, [A B A B] is cyclic; at 3 it is not. Clamped
	// to at least 1.
	MinRepetitions *int

	// MinPatternLength is the shortest pattern considered, in messages. The
	// default of 2 catches the common ask/answer/ask/answer loop. Clamped to at
	// least 1.
	MinPatternLength *int

	// MaxPatternLength bounds how long a repeating sequence may be. Unset means
	// len(messages)/MinRepetitions, the longest that could repeat enough times.
	MaxPatternLength *int

	// MinResultRepetitions overrides DefaultMinResultRepetitions. Clamped to at
	// least 2.
	MinResultRepetitions *int
}

// clamp resolves an optional setting: unset yields fallback, set yields the
// value floored at minimum.
func clamp(value *int, minimum, fallback int) int {
	if value == nil {
		return fallback
	}

	if *value < minimum {
		return minimum
	}

	return *value
}

func (o CycleOptions) minRepetitions() int {
	return clamp(o.MinRepetitions, 1, 2)
}

func (o CycleOptions) minPatternLength() int {
	return clamp(o.MinPatternLength, 1, 2)
}

func (o CycleOptions) minResultRepetitions() int {
	return clamp(o.MinResultRepetitions, 2, DefaultMinResultRepetitions)
}

// HasRepeatedSuffix reports whether the thread ends in a repeating block of
// messages - the plainest form of a loop, where the model and its context cycle
// through the same exchange verbatim.
//
// Messages are reduced to a fingerprint of (type, text, meta), and patterns are
// tried shortest-first because tight loops are both more common and more urgent
// than long ones.
//
// It requires the repeats to be byte-identical and adjacent, which is its
// blind spot: one interleaved message - a reasoning turn between tool calls -
// breaks the run. HasRepeatedResultRun covers that case.
func HasRepeatedSuffix(messages []Message, options CycleOptions) bool {
	minRepetitions := options.minRepetitions()
	minPatternLength := options.minPatternLength()

	maxPatternLength := 0

	if options.MaxPatternLength != nil {
		maxPatternLength = *options.MaxPatternLength

		if maxPatternLength < minPatternLength {
			maxPatternLength = minPatternLength
		}
	}

	if len(messages) < minPatternLength*minRepetitions {
		return false
	}

	fingerprints := make([]string, len(messages))

	for index, message := range messages {
		var meta any

		if message.HasMeta() {
			meta = safeStringify(message["meta"])
		}

		fingerprints[index] = safeStringify([]any{
			message.Type(),
			message.Text(),
			meta,
		})
	}

	if maxPatternLength == 0 {
		maxPatternLength = len(messages) / minRepetitions
	}

	for patternLength := minPatternLength; patternLength <= maxPatternLength; patternLength++ {
		if len(messages) < patternLength*minRepetitions {
			continue
		}

		pattern := fingerprints[len(fingerprints)-patternLength:]

		repetitions := 0

		for index := len(fingerprints) - patternLength; index >= 0; index -= patternLength {
			segment := fingerprints[index : index+patternLength]

			matches := true

			for offset, fingerprint := range segment {
				if fingerprint != pattern[offset] {
					matches = false

					break
				}
			}

			if !matches {
				break
			}

			repetitions++
		}

		if repetitions >= minRepetitions {
			return true
		}
	}

	return false
}

// activityTailEntry is one tool-call activity reduced to its identity.
type activityTailEntry struct {
	kind       string
	name       string
	inputHash  string
	hasInput   bool
	outputHash string
	hasOutput  bool
}

// activityFunction extracts the tool-call payload from a message's meta.
func activityFunction(message Message) (kind string, function map[string]any, ok bool) {
	meta, ok := message.Meta()
	if !ok {
		return "", nil, false
	}

	activity, ok := meta["activity"].(map[string]any)
	if !ok {
		return "", nil, false
	}

	kind, ok = activity["type"].(string)
	if !ok {
		return "", nil, false
	}

	function, ok = activity["function"].(map[string]any)
	if !ok {
		return "", nil, false
	}

	return kind, function, true
}

// HasRepeatedActivityTail reports whether the thread ends in a run of tool calls
// that keeps re-treading the same small set of signatures.
//
// It compresses the trailing contiguous activity messages to (type, name, input,
// output) and asks whether that set has collapsed. Unlike HasRepeatedSuffix it
// tolerates the text around the calls varying, which catches a model that
// narrates differently each time while doing the same thing.
//
// A request whose repeats produce genuinely different outputs is spared: polling
// an endpoint until it changes is progress, not a loop.
func HasRepeatedActivityTail(messages []Message, _ CycleOptions) bool {
	var tail []activityTailEntry

	for index := len(messages) - 1; index >= 0; index-- {
		message := messages[index]

		if message.Type() != "activity" {
			break
		}

		kind, function, ok := activityFunction(message)
		if !ok {
			break
		}

		name, ok := function["name"].(string)
		if !ok {
			break
		}

		entry := activityTailEntry{kind: kind, name: name}

		if arguments, present := function["arguments"]; present {
			entry.inputHash = safeStringify(arguments)
			entry.hasInput = true
		}

		if result, present := function["result"]; present {
			entry.outputHash = safeStringify(result)
			entry.hasOutput = true
		}

		tail = append(tail, entry)
	}

	for left, right := 0, len(tail)-1; left < right; left, right = left+1, right-1 {
		tail[left], tail[right] = tail[right], tail[left]
	}

	if len(tail) < 8 {
		return false
	}

	var requestInputs, responseOutputs []string

	for _, entry := range tail {
		if entry.kind == "request" && entry.hasInput {
			requestInputs = append(requestInputs, entry.inputHash)
		}

		if entry.kind == "response" && entry.hasOutput {
			responseOutputs = append(responseOutputs, entry.outputHash)
		}
	}

	if len(requestInputs) < 4 || len(responseOutputs) < 4 {
		return false
	}

	uniqueNames := map[string]struct{}{}
	uniqueSignatures := map[string]struct{}{}

	for _, entry := range tail {
		uniqueNames[entry.name] = struct{}{}

		var input, output any

		if entry.hasInput {
			input = entry.inputHash
		}

		if entry.hasOutput {
			output = entry.outputHash
		}

		uniqueSignatures[safeStringify([]any{entry.kind, entry.name, input, output})] = struct{}{}
	}

	uniqueInputs := distinct(requestInputs)
	uniqueOutputs := distinct(responseOutputs)

	// pair each request with the response that follows it, so a request whose
	// answers keep changing can be recognised as progress

	outputSets := map[string]map[string]struct{}{}
	outputCounts := map[string]int{}

	for index := 0; index < len(tail)-1; index++ {
		request, response := tail[index], tail[index+1]

		if request.kind != "request" || response.kind != "response" {
			continue
		}

		if request.name != response.name || !request.hasInput || !response.hasOutput {
			continue
		}

		key := safeStringify([]any{request.name, request.inputHash})

		if outputSets[key] == nil {
			outputSets[key] = map[string]struct{}{}
		}

		outputSets[key][response.outputHash] = struct{}{}
		outputCounts[key]++
	}

	progressing := false

	for key, outputs := range outputSets {
		if outputCounts[key] >= 3 && len(outputs) == outputCounts[key] {
			progressing = true

			break
		}
	}

	compressed := len(uniqueSignatures)*2 <= len(tail)

	return compressed &&
		len(uniqueNames) < len(tail) &&
		uniqueInputs < len(requestInputs) &&
		uniqueOutputs < len(responseOutputs) &&
		!progressing
}

func distinct(values []string) int {
	seen := make(map[string]struct{}, len(values))

	for _, value := range values {
		seen[value] = struct{}{}
	}

	return len(seen)
}

// HasRepeatedResultRun reports whether the last few tool results are identical -
// the model issuing the same call and getting the same answer, learning nothing.
//
// It walks only the tool-result stream, skipping every message in between. That
// is the gap it fills: HasRepeatedSuffix needs the surrounding messages to
// repeat byte-for-byte, and HasRepeatedActivityTail needs a contiguous run of
// activities, so a single interleaved reasoning message defeats both - and a
// reasoning model emits one between every tool call.
//
// Three details are load-bearing:
//
//   - Arguments are part of the signature. A model issuing different calls that
//     happen to return the same trivial result (several distinct shell commands
//     each producing empty output) is making progress, not looping.
//   - Synthetic "_"-prefixed activities are skipped, so a notice injected to
//     nudge the model out of a loop cannot break the run and mask the loop.
//   - Results still streaming are skipped: not a settled outcome to compare.
func HasRepeatedResultRun(messages []Message, options CycleOptions) bool {
	minRepetitions := options.minResultRepetitions()

	var signatures []string

	for index := len(messages) - 1; index >= 0; index-- {
		message := messages[index]

		if message.Type() != "activity" {
			continue
		}

		kind, function, ok := activityFunction(message)
		if !ok || kind != "response" {
			continue
		}

		name, ok := function["name"].(string)
		if !ok || strings.HasPrefix(name, "_") {
			continue
		}

		result, present := function["result"]
		if !present {
			continue
		}

		signatures = append(signatures, safeStringify([]any{
			name,
			safeStringify(function["arguments"]),
			safeStringify(result),
		}))

		// measured from the newest backwards, so once the window is full the
		// answer is already determined

		if len(signatures) >= minRepetitions {
			break
		}
	}

	if len(signatures) < minRepetitions {
		return false
	}

	for _, signature := range signatures[1:] {
		if signature != signatures[0] {
			return false
		}
	}

	return true
}

// hasRepeatedMessageTextRun is the thread-level adapter around
// HasRepeatedTextRun: a backstop for a degenerate message that reached the
// thread despite the streaming guard.
//
// Reasoning and activity messages are exempt. The reasoning channel is the
// model's scratchpad and activity carries tool output; both are legitimately
// repetitive - enumerations, grids, table rows - and neither is the answer the
// user sees.
func hasRepeatedMessageTextRun(messages []Message, _ CycleOptions) bool {
	start := len(messages) - 5
	if start < 0 {
		start = 0
	}

	for _, message := range messages[start:] {
		kind := message.Type()

		if kind == "reasoning" || kind == "activity" {
			continue
		}

		if HasRepeatedTextRun(message.Text(), TextRunOptions{}) {
			return true
		}
	}

	return false
}

// cycleHeuristic pairs a detector with the name reported by DescribeThreadCycle.
type cycleHeuristic struct {
	name   string
	detect func([]Message, CycleOptions) bool
}

// threadCycleHeuristics is ordered: the first to fire is the one reported. They
// overlap deliberately - each covers a blind spot of the others.
var threadCycleHeuristics = []cycleHeuristic{
	{"repeated_suffix", HasRepeatedSuffix},
	{"repeated_activity_tail", HasRepeatedActivityTail},
	{"repeated_result_run", HasRepeatedResultRun},
	{"repeated_message_text_run", hasRepeatedMessageTextRun},
}

// IsThreadCyclic reports whether any heuristic considers the thread stuck.
func IsThreadCyclic(messages []Message, options CycleOptions) bool {
	return DescribeThreadCycle(messages, options) != ""
}

// DescribeThreadCycle names the first heuristic to fire, or returns the empty
// string when the thread is progressing.
//
// Attribution matters more than it looks: without it a stuck run is only ever
// "stopped for looping", and the four heuristics fail in very different ways.
func DescribeThreadCycle(messages []Message, options CycleOptions) string {
	for _, heuristic := range threadCycleHeuristics {
		if heuristic.detect(messages, options) {
			return heuristic.name
		}
	}

	return ""
}
