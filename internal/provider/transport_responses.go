package provider

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

func init() {
	RegisterTransport(TransportResponses, func(httpClient *http.Client) Transport {
		return &responsesTransport{http: httpClient}
	})
}

type responsesTransport struct {
	http *http.Client
}

func (t *responsesTransport) Name() string { return TransportResponses }

// The OpenAI Responses API.
//
// It is a second wire format for the same job, and for reasoning models it is
// the better one: reasoning state is carried between turns as an opaque item the
// model can resume from, rather than being discarded because chat-completions
// has nowhere to put it. That is the whole reason for supporting it - a
// reasoning model driven over chat-completions re-derives its thinking on every
// tool round.
//
// Three shape differences matter:
//
//   - Input is a flat list of typed items, not messages with roles. A tool call
//     and its result are separate items linked by call_id.
//   - Tools are flat: {type, name, description, parameters}, with no nested
//     "function" object.
//   - Streaming is typed events (response.output_text.delta, …) rather than
//     anonymous deltas, so there is no index-based reassembly to do.

// responseItem is one entry in the Responses API's input list.
type responseItem struct {
	Type string `json:"type,omitempty"`

	// message
	Role    string `json:"role,omitempty"`
	Content any    `json:"content,omitempty"`

	// function_call
	CallID    string `json:"call_id,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`

	// function_call_output
	Output string `json:"output,omitempty"`

	// reasoning, replayed verbatim so the model can resume its own thinking
	ID string `json:"id,omitempty"`

	// @note a reasoning item's summary is required on the way back in and is
	// usually empty, so it cannot be omitempty - but nor may it appear on the
	// other item types. A pointer is what distinguishes "no summary field" from
	// "a summary field holding an empty list".
	Summary *[]any `json:"summary,omitempty"`

	// @note encrypted reasoning state, returned when the caller is not storing
	// the conversation server-side. Opaque, and must be handed back untouched.
	EncryptedContent string `json:"encrypted_content,omitempty"`
}

// responseTool is a tool definition in the Responses shape.
type responseTool struct {
	Type        string         `json:"type"`
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

// responsesRequest is the body sent to /responses.
type responsesRequest struct {
	Model  string         `json:"model"`
	Input  []responseItem `json:"input"`
	Tools  []responseTool `json:"tools,omitempty"`
	Stream bool           `json:"stream"`

	MaxOutputTokens *int `json:"max_output_tokens,omitempty"`

	// @note zot keeps no server-side conversation, so nothing is stored and the
	// reasoning state has to come back inline to be replayed.
	Store   bool     `json:"store"`
	Include []string `json:"include,omitempty"`

	Instructions string `json:"instructions,omitempty"`
}

// toResponsesInput converts chat messages into Responses items.
//
// The system turn becomes top-level instructions; assistant tool calls and tool
// results become sibling items rather than nested structures.
//
// An assistant turn's reasoning items go back first, ahead of the calls they
// produced. That order is not cosmetic: with store:false the provider holds
// nothing, so a function_call replayed on its own is rejected with "item … was
// provided without its required 'reasoning' item" - which is every second
// request of every tool round.
func toResponsesInput(messages []ChatMessage) (instructions string, items []responseItem) {
	for _, message := range messages {
		switch message.Role {
		case RoleSystem:
			// several system turns concatenate rather than the last one winning
			if instructions != "" {
				instructions += "\n\n"
			}

			instructions += message.Content

		case RoleTool:
			items = append(items, responseItem{
				Type:   "function_call_output",
				CallID: message.ToolCallID,
				Output: message.Content,
			})

		case RoleAssistant:
			for _, item := range message.ReasoningItems {
				summary := item.Summary

				if summary == nil {
					summary = []any{}
				}

				items = append(items, responseItem{
					Type:             "reasoning",
					ID:               item.ID,
					Summary:          &summary,
					EncryptedContent: item.EncryptedContent,
				})
			}

			for _, call := range message.ToolCalls {
				items = append(items, responseItem{
					Type:      "function_call",
					CallID:    call.ID,
					Name:      call.Function.Name,
					Arguments: call.Function.Arguments,
				})
			}

			if message.Content != "" {
				items = append(items, responseItem{
					Type: "message",
					Role: RoleAssistant,
					Content: []map[string]any{
						{"type": "output_text", "text": message.Content},
					},
				})
			}

		default:
			items = append(items, responseItem{
				Type: "message",
				Role: RoleUser,
				Content: []map[string]any{
					{"type": "input_text", "text": message.Content},
				},
			})
		}
	}

	return instructions, items
}

// toResponsesTools flattens tool definitions into the Responses shape.
func toResponsesTools(tools []Tool) []responseTool {
	converted := make([]responseTool, 0, len(tools))

	for _, tool := range tools {
		converted = append(converted, responseTool{
			Type:        "function",
			Name:        tool.Function.Name,
			Description: tool.Function.Description,
			Parameters:  tool.Function.Parameters,
		})
	}

	return converted
}

// responsesEvent is one typed SSE frame.
type responsesEvent struct {
	Type string `json:"type"`

	Delta string `json:"delta"`

	Item struct {
		Type      string `json:"type"`
		ID        string `json:"id"`
		CallID    string `json:"call_id"`
		Name      string `json:"name"`
		Arguments string `json:"arguments"`

		// a reasoning item's replayable state, present only because the request
		// asked for it inline
		Summary          []any  `json:"summary"`
		EncryptedContent string `json:"encrypted_content"`
	} `json:"item"`

	Response struct {
		Status string `json:"status"`

		IncompleteDetails *struct {
			Reason string `json:"reason"`
		} `json:"incomplete_details"`

		Usage *struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
			TotalTokens  int `json:"total_tokens"`
		} `json:"usage"`
	} `json:"response"`

	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (t *responsesTransport) Stream(ctx context.Context, config Config, request Request) <-chan Event {
	events := make(chan Event)

	go func() {
		defer close(events)

		// @note the Responses API has no stop-sequence parameter at all, so there
		// is nothing to translate Request.Stop into. Saying so is the only honest
		// option: dropping the sequences silently would leave a caller believing
		// generation is bounded when nothing bounds it.
		if len(request.Stop) > 0 {
			sendTerminal(events, Event{Err: &Error{Status: 0, Message: "the responses transport cannot honour stop sequences - the API has no equivalent parameter; use the chat-completions transport for them"}})

			return
		}

		instructions, input := toResponsesInput(request.Messages)

		body := responsesRequest{
			Model:           config.Model,
			Input:           input,
			Tools:           toResponsesTools(request.Tools),
			Stream:          true,
			MaxOutputTokens: request.MaxTokens,
			Instructions:    instructions,

			// nothing is stored server-side, so ask for the reasoning state
			// inline - it has to come back to be replayed on the next turn
			Store:   false,
			Include: []string{"reasoning.encrypted_content"},
		}

		response, err := httpPost(ctx, t.http, config, config.responsesURL(), body)
		if err != nil {
			sendTerminal(events, Event{Err: err})

			return
		}

		defer response.Body.Close()

		stream := newStallReader(response.Body, streamStallTimeout)

		defer stream.stop()

		if err := consumeResponsesSSE(ctx, stream, events); err != nil {
			sendTerminal(events, Event{Err: err})
		}
	}()

	return events
}

// consumeResponsesSSE parses the typed event stream.
//
// Simpler than the chat-completions path: tool calls arrive as complete items,
// so there is no index-keyed reassembly. What it does have to do is map the
// terminal status onto the same finish reasons the rest of the engine reasons
// about, so the loop does not need to know which transport ran.
func consumeResponsesSSE(ctx context.Context, body io.Reader, events chan<- Event) error {
	scanner := bufio.NewScanner(body)

	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

	var (
		calls     []ToolCall
		reasoning []ReasoningItem
		finish    string
		usage     *Usage
	)

	// whether the provider said the turn was over, rather than the body simply
	// stopping - see the check after the loop
	terminal := false

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		line := strings.TrimSpace(scanner.Text())

		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}

		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))

		if payload == "[DONE]" {
			terminal = true

			break
		}

		var event responsesEvent

		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			continue
		}

		switch event.Type {
		case "response.output_text.delta":
			if event.Delta != "" {
				if !send(ctx, events, Event{Token: event.Delta}) {
					return ctx.Err()
				}
			}

		case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
			if event.Delta != "" {
				if !send(ctx, events, Event{ReasoningToken: event.Delta}) {
					return ctx.Err()
				}
			}

		case "response.output_item.done":
			switch event.Item.Type {
			case "function_call":
				calls = append(calls, ToolCall{
					ID:   event.Item.CallID,
					Type: "function",
					Function: FunctionCall{
						Name:      event.Item.Name,
						Arguments: event.Item.Arguments,
					},
				})

			case "reasoning":
				// the state itself, kept in arrival order so it can go back ahead
				// of the calls it produced
				reasoning = append(reasoning, ReasoningItem{
					ID:               event.Item.ID,
					Summary:          event.Item.Summary,
					EncryptedContent: event.Item.EncryptedContent,
				})
			}

		case "response.completed", "response.incomplete":
			terminal = true

			if event.Response.Usage != nil {
				usage = &Usage{
					PromptTokens:     event.Response.Usage.InputTokens,
					CompletionTokens: event.Response.Usage.OutputTokens,
					TotalTokens:      event.Response.Usage.TotalTokens,
				}
			}

			// an incomplete response that ran out of output maps onto the same
			// truncation recovery the chat path uses
			if event.Response.IncompleteDetails != nil &&
				event.Response.IncompleteDetails.Reason == "max_output_tokens" {
				finish = FinishLength
			}

		case "response.failed", "error":
			message := "the provider reported a failure"

			if event.Error != nil && event.Error.Message != "" {
				message = event.Error.Message
			}

			return &Error{Status: 0, Message: message}
		}
	}

	if err := scanner.Err(); err != nil {
		return err
	}

	// the same rule the chat transport applies: a body that stopped without any
	// terminal frame is a cut-off turn, not a finished one. Defaulting to "stop"
	// here made the identical truncation read as a complete answer.
	if !terminal {
		return errTruncatedStream
	}

	if finish == "" {
		if len(calls) > 0 {
			finish = FinishToolCalls
		} else {
			finish = FinishStop
		}
	}

	// a final frame lost to cancellation is a cancelled turn, not a finished
	// one: report it so the caller's terminal send says so
	if !send(ctx, events, Event{
		ToolCalls:      calls,
		ReasoningItems: reasoning,
		FinishReason:   finish,
		Usage:          usage,
	}) {
		return ctx.Err()
	}

	return nil
}
