// Package thread assembles conversation threads that fit a model's context
// window, and detects when an agent has stopped making progress.
//
// A thread is the list of messages sent in a single request. BuildThread trims
// that list to a token budget; the cycle heuristics in cycle.go and the
// streaming guard in guard.go decide whether the conversation has degenerated
// into a loop.
//
// The behaviour here is pinned by testdata/corpus.json, a case corpus seeded
// from a production implementation. See corpus_test.go.
package thread

import (
	"encoding/json"
	"math"
)

// Message is one entry in a conversation thread.
//
// It is map-backed rather than a struct because messages carry provider- and
// caller-specific fields that must survive a round trip untouched: BuildThread
// hands the whole message to a token estimator and returns it with a usage
// annotation, and dropping an unrecognised field on the way through would
// silently corrupt the caller's data.
type Message map[string]any

// Type returns the message type ("user", "bot", "activity", "reasoning", ...),
// or the empty string when absent or not a string.
func (m Message) Type() string {
	value, _ := m["type"].(string)

	return value
}

// Text returns the message text, or the empty string when absent or not a
// string.
func (m Message) Text() string {
	value, _ := m["text"].(string)

	return value
}

// Meta returns the message metadata, or nil when absent.
func (m Message) Meta() (map[string]any, bool) {
	value, ok := m["meta"].(map[string]any)

	return value, ok
}

// HasMeta reports whether the message carries a meta field at all, including an
// explicit null. The distinction matters: a fingerprint treats "no meta" and
// "meta present but empty" as different messages.
func (m Message) HasMeta() bool {
	_, present := m["meta"]

	return present
}

// Clone returns a shallow copy, so annotating a message does not mutate the
// caller's.
func (m Message) Clone() Message {
	clone := make(Message, len(m)+1)

	for key, value := range m {
		clone[key] = value
	}

	return clone
}

// Usage is the token cost of a message or a thread.
type Usage struct {
	Tokens float64 `json:"tokens"`
}

// EstimateFunc reports the token usage of a message.
type EstimateFunc func(Message) (Usage, error)

// InclusiveFunc trims a message to fit trimTo tokens.
//
// Returning ok=false drops the message instead, ending the thread there. The
// returned message is expected to carry an updated usage; BuildThread clamps it
// to trimTo regardless, so a trimmer that overshoots cannot blow the budget.
type InclusiveFunc func(message Message, trimTo float64) (trimmed Message, ok bool, err error)

// BuildOptions configures BuildThread.
type BuildOptions struct {
	// Messages is the full conversation, oldest first.
	Messages []Message

	// Estimate reports the token cost of a message. Only consulted for messages
	// that do not already carry a non-negative usage.
	Estimate EstimateFunc

	// MaxTokens is the budget the assembled thread must fit within.
	MaxTokens float64

	// MinMessages is the number of newest messages kept even when they exceed
	// MaxTokens. Without a floor, one oversized message starves the whole turn.
	MinMessages int

	// Inclusive, when set, keeps the message that straddles the budget boundary
	// rather than dropping it whole.
	//
	// InclusiveAll keeps it untrimmed; Inclusive trims it to the remaining
	// budget. Both stop the walk at that message.
	Inclusive InclusiveFunc

	// InclusiveAll keeps the straddling message without trimming it. Ignored
	// when Inclusive is set.
	InclusiveAll bool
}

// BuildResult is an assembled thread and its total cost.
type BuildResult struct {
	Messages []Message `json:"messages"`
	Usage    Usage     `json:"usage"`
}

// normalizeTokens coerces a token count to a usable non-negative number.
// Anything infinite, NaN or negative counts as zero rather than poisoning the
// running total.
func normalizeTokens(tokens float64) float64 {
	if math.IsNaN(tokens) || math.IsInf(tokens, 0) || tokens < 0 {
		return 0
	}

	return tokens
}

// messageUsage reads a message's recorded usage, reporting whether one is
// present and usable. A recorded usage short-circuits the estimator.
func messageUsage(message Message) (Usage, bool) {
	raw, ok := message["usage"].(map[string]any)
	if !ok {
		return Usage{}, false
	}

	tokens, ok := raw["tokens"].(float64)
	if !ok {
		return Usage{}, false
	}

	// @note a recorded usage counts only when it is a real, non-negative
	// number. NaN and negatives fall through to the estimator, matching the
	// `usage?.tokens >= 0` guard they came from - a NaN comparison is false.
	if math.IsNaN(tokens) || tokens < 0 {
		return Usage{}, false
	}

	return Usage{Tokens: tokens}, true
}

// withUsage returns a copy of the message annotated with the given usage, and
// with any pre-existing usage replaced.
func withUsage(message Message, usage Usage) Message {
	annotated := message.Clone()

	annotated["usage"] = map[string]any{"tokens": usage.Tokens}

	return annotated
}

// BuildThread assembles the largest suffix of the conversation that fits within
// MaxTokens.
//
// It walks from the newest message backwards, which is what makes the fit exact:
// the budget is spent on the most recent context first, and the walk stops the
// moment the next message would not fit. The result is returned oldest-first.
//
// The function is deliberately indifferent to message type and ordering - it
// only counts tokens. Keeping paired messages together (a tool-call request and
// its response, which a provider rejects if split) is the caller's job, and must
// be done before trimming rather than after.
func BuildThread(options BuildOptions) (BuildResult, error) {
	minMessages := options.MinMessages
	if minMessages < 0 {
		minMessages = -minMessages
	}

	// estimate every message up front, reusing any usage already recorded

	processed := make([]Message, len(options.Messages))

	for index, message := range options.Messages {
		usage, ok := messageUsage(message)

		if !ok {
			if options.Estimate == nil {
				return BuildResult{}, errNoEstimator
			}

			estimated, err := options.Estimate(message)
			if err != nil {
				return BuildResult{}, err
			}

			usage = estimated
		}

		usage.Tokens = normalizeTokens(usage.Tokens)

		processed[index] = withUsage(message, usage)
	}

	inclusive := options.Inclusive != nil || options.InclusiveAll

	trimmed := make([]Message, 0, len(processed))

	var total float64

	for index := len(processed) - 1; index >= 0; index-- {
		message := processed[index]

		usage, _ := messageUsage(message)

		tokens := normalizeTokens(usage.Tokens)

		// the newest minMessages are kept whatever they cost

		withinMinimum := len(processed)-index <= minMessages

		if total+tokens > options.MaxTokens && !withinMinimum {
			if inclusive {
				if options.Inclusive != nil {
					trimTo := options.MaxTokens - total

					if trimTo > 0 {
						replacement, keep, err := options.Inclusive(message, trimTo)
						if err != nil {
							return BuildResult{}, err
						}

						if !keep {
							break
						}

						message = replacement

						replacementUsage, _ := messageUsage(message)

						tokens = normalizeTokens(replacementUsage.Tokens)

						// @note clamp rather than trust: a trimmer that returns
						// more than it was asked for must not blow the budget
						if tokens > trimTo {
							tokens = trimTo

							message = withUsage(message, Usage{Tokens: tokens})
						}
					}
				}

				total += tokens

				trimmed = append(trimmed, message)
			}

			break
		}

		total += tokens

		trimmed = append(trimmed, message)

		if total >= options.MaxTokens && !withinMinimum && !inclusive {
			break
		}
	}

	// restore chronological order

	for left, right := 0, len(trimmed)-1; left < right; left, right = left+1, right-1 {
		trimmed[left], trimmed[right] = trimmed[right], trimmed[left]
	}

	return BuildResult{Messages: trimmed, Usage: Usage{Tokens: total}}, nil
}

// safeStringify renders a value as JSON for use as a comparison key, degrading
// to a sentinel rather than failing.
//
// Values reaching the heuristics come from provider payloads and may contain
// anything, including structures that cannot be marshalled. A cycle check must
// never be the thing that aborts a run, so an unserialisable value collapses to
// a constant - which makes two such values compare equal, and is the intended
// trade: the alternative is no check at all.
func safeStringify(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return `"[unserializable]"`
	}

	return string(encoded)
}
