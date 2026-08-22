package provider

import (
	"encoding/json"
	"strings"
)

// The vocabulary every transport shares.
//
// A transport translates between these types and whatever its wire format
// happens to look like. Keeping them here rather than in any one transport is
// what lets a non-OpenAI provider be added without touching the engine: the
// engine only ever sees a Request and a stream of Events.

// Role values in the chat-completions wire format.
const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleTool      = "tool"
)

// Finish reasons the provider may report.
const (
	FinishStop      = "stop"
	FinishLength    = "length"
	FinishToolCalls = "tool_calls"
)

// ToolCall is a request from the model to run a tool.
type ToolCall struct {
	ID       string       `json:"id,omitempty"`
	Type     string       `json:"type,omitempty"`
	Function FunctionCall `json:"function"`
}

// FunctionCall is the name and JSON-encoded arguments of a tool call.
type FunctionCall struct {
	Name string `json:"name"`

	// Arguments is a JSON document as a string, which is how the wire format
	// carries it. It arrives in fragments when streaming and is concatenated.
	Arguments string `json:"arguments"`
}

// ReasoningItem is a model's own thinking, carried between turns as an opaque
// item rather than as text.
//
// A reasoning model produces one alongside the tool calls it decided on, and
// where nothing is stored server-side this is the only copy of it: replaying the
// call without the item that produced it is rejected outright ("item … was
// provided without its required 'reasoning' item"), and even where it is
// accepted the model has to re-derive its thinking on every tool round. zot
// never inspects the state - it comes out of one response and goes back into the
// next untouched.
// The tags are for zot's own persistence - the session log, and the loop's
// thread round trip - not for the wire; the transport builds its own items.
type ReasoningItem struct {
	// ID identifies the item to the provider, and links it to the calls it
	// produced.
	ID string `json:"id,omitempty"`

	// Summary is the précis the model emitted, replayed exactly as it arrived.
	Summary []any `json:"summary,omitempty"`

	// EncryptedContent is the state itself, returned inline only because the
	// request asked for it.
	EncryptedContent string `json:"encrypted_content,omitempty"`
}

// ChatMessage is one message in a request or response.
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content,omitempty"`

	// Name identifies the tool for a tool-result message.
	Name string `json:"name,omitempty"`

	// ToolCallID links a tool result back to the call that requested it.
	ToolCallID string `json:"tool_call_id,omitempty"`

	// ToolCalls are the calls an assistant turn is requesting.
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`

	// ReasoningContent is the reasoning channel some providers emit alongside
	// the answer.
	ReasoningContent string `json:"reasoning_content,omitempty"`

	// ReasoningItems is the opaque reasoning state this turn produced, to be
	// replayed with it. Never serialised here: the Responses transport that
	// carries it builds its own items.
	ReasoningItems []ReasoningItem `json:"-"`

	// ReasoningDetails is the chat-completions counterpart: the gateway's
	// structured reasoning blocks (OpenRouter's reasoning_details), replayed on
	// the assistant message verbatim and in order. A reasoning model
	// interleaves thinking with its tool calls, and a gateway that carries that
	// thinking requires it back on the next request - dropping it degrades the
	// model's continuity at best, and at worst the upstream rejects the request
	// once the chain has grown. Opaque by design: zot never inspects it.
	ReasoningDetails json.RawMessage `json:"reasoning_details,omitempty"`

	// Images are shown alongside Content. Marshalled into content parts rather
	// than a field of their own - see MarshalJSON - because that is the only
	// shape the API accepts them in.
	//
	// Only a user message may carry them: an OpenAI-compatible endpoint rejects
	// image parts on a tool result, which is why a tool that produces an image
	// returns text and the engine attaches the image to a message of its own.
	Images []Image `json:"-"`
}

// MarshalJSON renders the message for the wire, promoting content to an array
// of parts when the message carries images.
//
// It lives here rather than in the transport because every path that sends a
// message goes through this type, and a message that quietly dropped its images
// on one of them would be a bug nobody sees until a model insists it was shown
// nothing.
func (m ChatMessage) MarshalJSON() ([]byte, error) {
	// alias sheds the method so the default encoding is still reachable
	type alias ChatMessage

	encoded, err := json.Marshal(alias(m))
	if err != nil {
		return nil, err
	}

	if len(m.Images) == 0 {
		return encoded, nil
	}

	images := make([]any, 0, len(m.Images))

	for _, image := range m.Images {
		if !image.Ready() {
			continue // a blob that went missing; the text still describes it
		}

		url := map[string]any{"url": image.DataURL()}

		if image.Detail != "" {
			url["detail"] = image.Detail
		}

		images = append(images, map[string]any{"type": "image_url", "image_url": url})
	}

	// nothing sendable survived: send the plain string rather than an array
	// with a lone text part, which is a shape some endpoints are fussier about
	if len(images) == 0 {
		return encoded, nil
	}

	parts := make([]any, 0, len(images)+1)

	if m.Content != "" {
		parts = append(parts, map[string]any{"type": "text", "text": m.Content})
	}

	parts = append(parts, images...)

	object := map[string]json.RawMessage{}

	if err := json.Unmarshal(encoded, &object); err != nil {
		return nil, err
	}

	content, err := json.Marshal(parts)
	if err != nil {
		return nil, err
	}

	object["content"] = content

	return json.Marshal(object)
}

// Tool is a tool definition offered to the model.
type Tool struct {
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

// ToolFunction describes a callable tool.
type ToolFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

// Request is a chat-completion request.
type Request struct {
	Messages  []ChatMessage
	Tools     []Tool
	MaxTokens *int
	Stop      []string
}

// Usage is the token accounting a provider reports.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// Event is one item from a streamed completion.
type Event struct {
	// Token is a fragment of the answer.
	Token string

	// ReasoningToken is a fragment of the reasoning channel.
	ReasoningToken string

	// ToolCalls is the assembled set, emitted once when the turn ends.
	ToolCalls []ToolCall

	// ReasoningItems is the opaque reasoning state the turn produced, delivered
	// on the same final event as the tool calls it belongs with.
	ReasoningItems []ReasoningItem

	// ReasoningDetails is the chat-completions reasoning state (see
	// ChatMessage.ReasoningDetails), delivered on the final event.
	ReasoningDetails json.RawMessage

	// FinishReason is set on the final event.
	FinishReason string

	// Usage is set on the final event when the provider reports it.
	Usage *Usage

	// Err terminates the stream.
	Err error
}

// textBuilder accumulates streamed fragments.
type textBuilder struct {
	parts []string
}

func (b *textBuilder) WriteString(s string) {
	if s != "" {
		b.parts = append(b.parts, s)
	}
}

func (b *textBuilder) String() string {
	return strings.Join(b.parts, "")
}
