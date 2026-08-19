// Package compaction condenses a conversation that has outgrown the model's
// context window.
//
// When the estimated token count approaches the limit, the older half of the
// conversation is replaced by a single LLM-generated summary and the recent tail
// is preserved verbatim. That keeps the agent oriented without discarding the
// history outright.
//
// The behaviour here is pinned by testdata/corpus.json. See corpus_test.go.
//
// Two layers live in this package, and telling them apart matters. The token
// counters and BuildSummaryPrompt are what the engine calls on every run. The
// decide-and-rewrite surface - Check, CheckCompaction, Split,
// SplitMessagesForCompaction, ApplySummary - is the ported reference: the loop
// makes those decisions itself (splitForCheckpoint and applyCheckpoint, in
// internal/loop) because it pins the system prompt and existing checkpoints,
// which the seed had no concept of. These are kept because the corpus pins them
// against the implementation zot was ported from, which is what makes a
// divergence in the live path detectable rather than a matter of opinion. They
// are reference, not dead code, and deleting them would delete the parity check
// with them.
package compaction

import (
	"fmt"
	"math"
	"strings"

	"github.com/openzot/openzot/internal/tokenizer"
)

const (
	// MinMessages is the floor below which compaction is not worth it - the
	// summary would cost more than the history it replaces.
	MinMessages = 4

	// KeepRecentRatio is the fraction of the conversation preserved verbatim as
	// the recent tail.
	KeepRecentRatio = 0.25

	// TriggerRatio is the fraction of the budget at which compaction fires.
	TriggerRatio = 0.9
)

// MessageType identifies what a message is.
//
// Named rather than a bare string because compaction branches on it in two
// places that matter: the system prompt is never summarised and is always
// re-ordered to the front, and only conversational types reach the summary
// prompt. A typo would quietly summarise away a system prompt.
type MessageType string

// The message types compaction distinguishes. Others pass through untouched.
const (
	TypeUser      MessageType = "user"
	TypeBot       MessageType = "bot"
	TypeContext   MessageType = "context"
	TypeReasoning MessageType = "reasoning"
	// TypeInstructions is the run's system prompt: never summarised, always
	// hoisted to the front.
	TypeInstructions MessageType = "instructions"
	TypeActivity     MessageType = "activity"
)

// Message is the minimal shape compaction operates on.
type Message struct {
	ID   string      `json:"id,omitempty"`
	Type MessageType `json:"type"`
	Text string      `json:"text"`

	// Payload is anything the message costs that is not in its text - in
	// practice a tool call's name, arguments and result, which a request half
	// carries entirely outside the text.
	//
	// A rendered string rather than a structure, because compaction only ever
	// prices it. What that string is is the caller's business.
	Payload string `json:"payload,omitempty"`

	// Summary is set on a message this package produced, recording what it
	// replaced. Provenance, not content: nothing reads it to make a decision.
	//
	// Typed rather than a map, but serialised under `meta` with the field names
	// the TypeScript implementation uses - the corpus compares this output
	// byte for byte, and that equivalence is the point of having a corpus.
	Summary *Summary `json:"meta,omitempty"`
}

// Summary records what a compaction replaced.
type Summary struct {
	// Compacted marks the message as one this package produced.
	Compacted bool `json:"compacted"`

	// DroppedCount is how many messages were condensed into this one.
	DroppedCount int `json:"droppedCount"`

	// SummarizedIDs names them, where the caller gave them ids.
	SummarizedIDs []string `json:"summarizedIds,omitempty"`
}

// Options configures a compaction decision.
type Options struct {
	// Model selects the tokenizer vocabulary. An empty value uses the default
	// encoding, which is close enough for a budget decision.
	Model string

	// MaxTokens is the upper bound for the message list.
	MaxTokens int

	// KeepRecentCount is how many messages to preserve verbatim from the tail.
	// Unset derives it from KeepRecentRatio.
	KeepRecentCount *int

	// TriggerRatio overrides the default fraction of MaxTokens at which
	// compaction fires.
	TriggerRatio *float64
}

func (o Options) triggerRatio() float64 {
	if o.TriggerRatio != nil {
		return *o.TriggerRatio
	}

	return TriggerRatio
}

// Check is the outcome of a compaction decision.
type Check struct {
	ShouldCompact bool

	// MessagesToSummarize is the older head that will be condensed. Empty when
	// ShouldCompact is false.
	MessagesToSummarize []Message

	// MessagesToKeep is the recent tail preserved verbatim, with any system-prompt
	// messages hoisted to the front.
	MessagesToKeep []Message

	// EstimatedTokens is the estimate that drove the decision, reported either
	// way so a caller can log how close it is running.
	EstimatedTokens int
}

// Split is the result of dividing a conversation into summarise and keep
// buckets.
type Split struct {
	MessagesToSummarize []Message
	MessagesToKeep      []Message
}

// CountMessageTokens returns a message's token cost for the given model.
//
// Counted with the model's own vocabulary rather than estimated from length:
// every budgeting decision downstream rests on this number, and a character
// heuristic is wrong by enough to matter. Wire-format overhead is included,
// because a long thread of short messages is otherwise under-counted by more
// than the margin that decides whether it fits.
//
// A message's payload is counted with its text, exactly as the TypeScript
// engine's estimateMessageUsage does: it prices an activity as
// `text + JSON.stringify(meta)`. The whole payload rather than selected fields,
// because what a provider charges for is the rendered tool call, and leaving a
// field out of the estimate is how a conversation overruns its window while the
// estimate says there is room. A request half carries no text at all, so
// counting text alone prices a write of a whole file as an empty message.
func CountMessageTokens(model string, message Message) int {
	return tokenizer.CountMessage(model, message.Text+message.Payload)
}

// CountMessagesTokens sums the per-message counts.
func CountMessagesTokens(model string, messages []Message) int {
	total := 0

	for _, message := range messages {
		total += CountMessageTokens(model, message)
	}

	return total
}

// CheckCompaction decides whether the conversation needs condensing, and if so
// how to divide it.
//
// It declines in three cases: under the trigger threshold, under MinMessages,
// or when the split leaves nothing summarisable - which happens when the head is
// entirely the system prompt. Producing a summary of nothing is worse than not
// compacting.
func CheckCompaction(messages []Message, options Options) Check {
	estimated := CountMessagesTokens(options.Model, messages)

	threshold := int(math.Floor(float64(options.MaxTokens) * options.triggerRatio()))

	if estimated < threshold || len(messages) < MinMessages {
		return Check{EstimatedTokens: estimated}
	}

	split := SplitMessagesForCompaction(messages, options)

	if len(split.MessagesToSummarize) == 0 {
		return Check{EstimatedTokens: estimated}
	}

	return Check{
		ShouldCompact:       true,
		MessagesToSummarize: split.MessagesToSummarize,
		MessagesToKeep:      split.MessagesToKeep,
		EstimatedTokens:     estimated,
	}
}

// SplitMessagesForCompaction divides a conversation into the head to summarise
// and the tail to keep.
//
// System-prompt messages are never summarised - they carry the system context the
// model must not lose - so any found in the head are moved to the front of the
// keep bucket instead, ready for Apply to place them ahead of the summary.
func SplitMessagesForCompaction(messages []Message, options Options) Split {
	if len(messages) < MinMessages {
		return Split{MessagesToKeep: messages}
	}

	keepRecent := int(math.Max(2, math.Ceil(float64(len(messages))*KeepRecentRatio)))

	if options.KeepRecentCount != nil {
		keepRecent = *options.KeepRecentCount
	}

	splitIndex := len(messages) - keepRecent

	if splitIndex <= 0 {
		return Split{MessagesToKeep: messages}
	}

	head, tail := messages[:splitIndex], messages[splitIndex:]

	var instructions, summarizable []Message

	for _, message := range head {
		if message.Type == TypeInstructions {
			instructions = append(instructions, message)
		} else {
			summarizable = append(summarizable, message)
		}
	}

	if len(summarizable) == 0 {
		return Split{MessagesToKeep: messages}
	}

	keep := make([]Message, 0, len(instructions)+len(tail))
	keep = append(keep, instructions...)
	keep = append(keep, tail...)

	return Split{MessagesToSummarize: summarizable, MessagesToKeep: keep}
}

// summarisableTypes are the message types that carry substantive conversation.
// The system prompt and activity are excluded: they add noise to a summary without
// adding conversational value.
var summarisableTypes = map[MessageType]struct{}{
	TypeUser:      {},
	TypeBot:       {},
	TypeContext:   {},
	TypeReasoning: {},
}

// BuildSummaryPrompt renders the prompt asking a model to condense the history.
//
// The structure - discussed / accomplished / details / ongoing - is what keeps
// summaries usable across turns. The instruction to preserve opaque identifiers
// exactly matters more than it reads: a summary that helpfully shortens an ID
// silently breaks every later reference to it.
func BuildSummaryPrompt(messagesToSummarize []Message) string {
	var lines []string

	for _, message := range messagesToSummarize {
		if _, ok := summarisableTypes[message.Type]; !ok {
			continue
		}

		lines = append(lines, fmt.Sprintf("[%s]: %s", strings.ToUpper(string(message.Type)), message.Text))
	}

	return `You are summarising a conversation that has grown too long for the context window.

Produce a concise but comprehensive summary structured as follows:

---
## Context Summary

### What was discussed
[Key topics, questions, and information exchanged]

### What was accomplished
[Tasks completed, decisions made, or solutions provided]

### Important details to remember
[Specific facts, preferences, constraints, or values that must persist:
names, IDs, dates, URLs, file paths, numeric values, user preferences, etc.
Preserve opaque identifiers exactly - do NOT shorten or reconstruct them.]

### Ongoing state
[Pending questions, open tasks, or items the user is waiting on]
---

Do not respond to the conversation; only output the summary in the format above.

<conversation>
` + strings.Join(lines, "\n") + `
</conversation>`
}

// ApplySummary merges a generated summary back into the conversation.
//
// The resulting order is the system prompt, then summary, then the recent tail. The
// system prompt has to come first or the model reads the summary without knowing
// who it is, and a summary placed before the system prompt is a surprisingly common
// way to lose an agent's persona mid-run.
//
// The summary carries the ids it replaced in meta.summarizedIds, so a caller can
// filter the originals out of later history and avoid re-compacting them.
func ApplySummary(messagesToKeep []Message, summaryText string, messagesToSummarize []Message) []Message {
	var summarizedIDs []string

	for _, message := range messagesToSummarize {
		if message.ID != "" {
			summarizedIDs = append(summarizedIDs, message.ID)
		}
	}

	var instructions, rest []Message

	for _, message := range messagesToKeep {
		if message.Type == TypeInstructions {
			instructions = append(instructions, message)
		} else {
			rest = append(rest, message)
		}
	}

	summary := Message{
		Type: TypeContext,
		Text: fmt.Sprintf(
			"[Conversation summary - %d earlier message(s) condensed]\n\n%s",
			len(messagesToSummarize),
			strings.TrimSpace(summaryText),
		),
		Summary: &Summary{
			Compacted:     true,
			DroppedCount:  len(messagesToSummarize),
			SummarizedIDs: summarizedIDs,
		},
	}

	merged := make([]Message, 0, len(instructions)+1+len(rest))
	merged = append(merged, instructions...)
	merged = append(merged, summary)
	merged = append(merged, rest...)

	return merged
}
