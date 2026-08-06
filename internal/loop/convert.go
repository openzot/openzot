package loop

import (
	"github.com/openzot/openzot/internal/compaction"
	"github.com/openzot/openzot/internal/provider"
	"github.com/openzot/openzot/internal/thread"
)

// Conversions between the loop's message shape and the ones the subsystems use.
//
// They are kept in one file rather than scattered because getting a conversion
// wrong is silent: the thread heuristics need type and meta to spot a loop, and
// a conversion that drops meta simply stops detecting anything.

// EventKind identifies what happened.
type EventKind string

const (
	EventIteration      EventKind = "iteration"
	EventToken          EventKind = "token"
	EventReasoningToken EventKind = "reasoningToken"
	EventMessage        EventKind = "message"
	EventToolCallStart  EventKind = "toolCallStart"
	EventToolCallEnd    EventKind = "toolCallEnd"
	EventToolCallError  EventKind = "toolCallError"
	EventRetry          EventKind = "retry"
	EventRunaway        EventKind = "runaway"
	EventCompact        EventKind = "compact"
)

// Event is emitted as a run progresses.
type Event struct {
	Kind      EventKind
	Text      string
	Tool      string
	Iteration int

	// MessageType and Activity describe a completed message.
	MessageType MessageType
	Activity    *Activity

	// Args are a tool call's decoded arguments; Result is what it returned.
	Args   map[string]any
	Result any
}

func toThreadMessages(messages []Message) []thread.Message {
	converted := make([]thread.Message, 0, len(messages))

	for _, message := range messages {
		entry := thread.Message{"type": string(message.Type), "text": message.Text}

		// the heuristics compare the platform's map shape, because their corpus
		// is JSON captured from the TypeScript implementation
		if meta := message.Activity.threadMeta(); meta != nil {
			entry["meta"] = meta
		}

		converted = append(converted, entry)
	}

	return converted
}

func fromThreadMessages(messages []thread.Message) []Message {
	converted := make([]Message, 0, len(messages))

	for _, message := range messages {
		entry := Message{Type: MessageType(message.Type()), Text: message.Text()}

		if meta, ok := message.Meta(); ok {
			entry.Activity = activityFromMeta(meta)
		}

		converted = append(converted, entry)
	}

	return converted
}

func toCompactionMessages(messages []Message) []compaction.Message {
	converted := make([]compaction.Message, 0, len(messages))

	for _, message := range messages {
		entry := compaction.Message{
			Type: compaction.MessageType(message.Type),
			Text: message.Text,
		}

		// compaction prices what a message costs, and a tool call's payload is
		// most of what it costs
		if message.Activity != nil {
			entry.Payload = message.Activity.Payload()
		}

		converted = append(converted, entry)
	}

	return converted
}

// toChatMessages renders the conversation into the provider wire format.
//
// Activity messages are the interesting case: a request half becomes an
// assistant turn carrying tool_calls, and a response half becomes a tool
// message referencing the same id. Providers validate that pairing, so a
// response whose request was trimmed away is dropped rather than sent.
func toChatMessages(messages []Message) []provider.ChatMessage {
	var (
		converted []provider.ChatMessage
		pending   = map[string]bool{}
	)

	// repair the history before rendering it: a provider rejects the whole
	// request rather than the invalid part, so anything left unpaired here ends
	// an otherwise healthy run with an opaque 400
	for _, message := range Organize(messages) {
		switch message.Type {
		case TypeActivity:
			activity := message.Activity

			if activity == nil {
				continue
			}

			switch activity.Kind {
			case ActivityRequest:
				pending[activity.ID] = true

				converted = append(converted, provider.ChatMessage{
					Role: provider.RoleAssistant,
					ToolCalls: []provider.ToolCall{{
						ID:   activity.ID,
						Type: "function",
						Function: provider.FunctionCall{
							Name:      activity.Name,
							Arguments: activity.Arguments,
						},
					}},
				})

			case ActivityResponse:
				// a result whose call was trimmed away would be rejected
				if !pending[activity.ID] {
					continue
				}

				delete(pending, activity.ID)

				converted = append(converted, provider.ChatMessage{
					Role:       provider.RoleTool,
					Name:       activity.Name,
					ToolCallID: activity.ID,
					Content:    activity.ResultText(),
				})
			}

		case TypeBot:
			converted = append(converted, provider.ChatMessage{
				Role:    provider.RoleAssistant,
				Content: message.Text,
			})

		case TypeReasoning:
			// the reasoning channel is not replayed: providers reject their own
			// reasoning content on the way back in, and it is the model's
			// scratchpad rather than conversation

		case TypeInstructions, TypeCheckpoint:
			converted = append(converted, provider.ChatMessage{
				Role:    provider.RoleSystem,
				Content: message.Text,
			})

		default:
			converted = append(converted, provider.ChatMessage{
				Role:    provider.RoleUser,
				Content: message.Text,
			})
		}
	}

	// an assistant turn requesting a call that never got a result leaves the
	// conversation invalid; drop the dangling halves
	if len(pending) > 0 {
		converted = dropDangling(converted, pending)
	}

	return converted
}

// dropDangling removes assistant tool-call turns whose results are missing.
func dropDangling(messages []provider.ChatMessage, pending map[string]bool) []provider.ChatMessage {
	kept := make([]provider.ChatMessage, 0, len(messages))

	for _, message := range messages {
		if len(message.ToolCalls) == 1 && pending[message.ToolCalls[0].ID] {
			continue
		}

		kept = append(kept, message)
	}

	return kept
}
