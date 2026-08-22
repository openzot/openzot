package loop

import (
	"encoding/json"

	"github.com/openzot/openzot/internal/provider"
)

// A tool call and its result, as first-class fields rather than a bag of keys.
//
// The engine used to carry these in a `map[string]any` under a `meta.activity`
// key, inherited from a hosted API whose messages are a generic envelope: one
// shape that has to carry attachments, ratings, triggers and a dozen other
// things zot has no concept of. zot has exactly one thing to put there - a tool
// call - and a map buys nothing for it: every read is a type assertion that can
// fail silently, a misspelt key is a compile-time success and a runtime no-op,
// and nothing tells you which keys are expected.
//
// So it is a struct. A missing field is a compile error, the shape is the
// documentation, and the pairing and rendering code stops guessing.

// ActivityKind is which half of a tool call a message carries.
type ActivityKind string

const (
	// ActivityRequest is the model asking for a tool to be run.
	ActivityRequest ActivityKind = "request"

	// ActivityResponse is what the tool returned.
	ActivityResponse ActivityKind = "response"

	// ActivityTrigger is an instruction to act now, carrying no call. Retained
	// because a conversation loaded from elsewhere can hold one; the engine
	// never produces them.
	ActivityTrigger ActivityKind = "trigger"
)

// Activity is one half of a tool-call pair.
type Activity struct {
	// Kind is which half this is.
	Kind ActivityKind `json:"kind"`

	// ID is the provider's call identifier, which is what joins the two halves.
	ID string `json:"id,omitempty"`

	// Name is the tool being called.
	Name string `json:"name,omitempty"`

	// Arguments is the call's arguments as the provider sent them - a JSON
	// string, kept verbatim rather than decoded, because it is replayed to the
	// provider exactly as received and re-encoding could change it.
	Arguments string `json:"arguments,omitempty"`

	// Result is what the tool returned, on a response. Whatever a handler
	// produced, so it is rendered as JSON when it reaches the model.
	Result any `json:"result,omitempty"`

	// Failure explains why a call could not be run. Set instead of Result.
	Failure string `json:"failure,omitempty"`

	// ReasoningItems is the opaque reasoning state the turn that made this call
	// produced, carried so it can be replayed alongside the call. Set on the
	// first request of a turn only - the state belongs to the turn, not to each
	// call, and replaying it once per call would send it several times.
	ReasoningItems []provider.ReasoningItem `json:"reasoning_items,omitempty"`

	// ReasoningDetails is the chat-completions counterpart: the gateway's
	// structured reasoning blocks, replayed verbatim on the assistant message
	// that carries this call. Same placement rule as ReasoningItems - the
	// turn's first call only.
	ReasoningDetails json.RawMessage `json:"reasoning_details,omitempty"`
}

// IsPair reports whether two activities are the two halves of one call.
//
// Identity wins when both sides have one: providers issue a call id precisely so
// a result can name its call, and two identical calls in the same turn are only
// distinguishable that way. Name and arguments are the fallback, for histories
// reconstructed from somewhere that did not keep ids.
func (a *Activity) IsPair(other *Activity) bool {
	if a == nil || other == nil {
		return false
	}

	// a pair is one of each
	if a.Kind == other.Kind {
		return false
	}

	for _, kind := range []ActivityKind{a.Kind, other.Kind} {
		if kind != ActivityRequest && kind != ActivityResponse {
			return false
		}
	}

	if a.ID != "" && other.ID != "" {
		return a.ID == other.ID
	}

	return a.Name == other.Name && a.Arguments == other.Arguments
}

// ResultText renders what the model is shown for a response.
//
// A failure is presented as a JSON object rather than bare prose so the model
// sees a tool result of the shape it expects, with the error inside it.
func (a *Activity) ResultText() string {
	if a == nil {
		return ""
	}

	value := a.Result

	if a.Failure != "" {
		value = map[string]any{"error": a.Failure}
	}

	if value == nil {
		return ""
	}

	if text, ok := value.(string); ok {
		return text
	}

	encoded, err := json.Marshal(value)
	if err != nil {
		return ""
	}

	return string(encoded)
}

// Payload renders everything about a call that is not in the message text.
//
// Used for token accounting, and deliberately the whole activity rather than
// selected fields: what the provider is charged for is the rendered tool call,
// and leaving a field out of the estimate is how a conversation overruns its
// window while the estimate says there is room.
func (a *Activity) Payload() string {
	if a == nil {
		return ""
	}

	encoded, err := json.Marshal(a)
	if err != nil {
		return ""
	}

	return string(encoded)
}

// threadMeta renders the activity back into the map shape the thread
// heuristics read.
//
// internal/thread works on `map[string]any` messages because its corpus is
// JSON captured from the TypeScript implementation, and the cycle heuristics
// compare that structure directly. Rather than reshape the corpus - which would
// break the guarantee that zot's heuristics answer exactly as the original's -
// the typed activity is rendered into that shape at the boundary.
func (a *Activity) threadMeta() map[string]any {
	if a == nil {
		return nil
	}

	function := map[string]any{"name": a.Name, "arguments": a.Arguments}

	if a.Kind == ActivityResponse {
		if a.Failure != "" {
			function["result"] = map[string]any{"error": a.Failure}
		} else {
			function["result"] = a.Result
		}
	}

	meta := map[string]any{"activity": map[string]any{
		"type":     string(a.Kind),
		"id":       a.ID,
		"function": function,
	}}

	// A sibling of "activity" rather than a member of it: the activity map is
	// the platform shape the thread heuristics match on, and this is zot's own
	// state. It has to survive the round trip through the thread builder or the
	// call it belongs to is replayed without it.
	if len(a.ReasoningItems) > 0 {
		meta["reasoning_items"] = a.ReasoningItems
	}

	if len(a.ReasoningDetails) > 0 {
		meta["reasoning_details"] = a.ReasoningDetails
	}

	return meta
}

// activityFromMeta reads an activity out of the platform's map shape.
//
// The one place the old shape is still understood: a conversation loaded from a
// session log written by an older build, or handed in by an embedder that
// speaks the platform's format.
func activityFromMeta(meta map[string]any) *Activity {
	raw, ok := meta["activity"].(map[string]any)
	if !ok {
		return nil
	}

	activity := &Activity{}

	kind, _ := raw["type"].(string)
	activity.Kind = ActivityKind(kind)
	activity.ID, _ = raw["id"].(string)

	if function, ok := raw["function"].(map[string]any); ok {
		activity.Name, _ = function["name"].(string)

		switch arguments := function["arguments"].(type) {
		case string:
			activity.Arguments = arguments
		case nil:
		default:
			if encoded, err := json.Marshal(arguments); err == nil {
				activity.Arguments = string(encoded)
			}
		}

		activity.Result = function["result"]
	}

	if activity.Kind == "" {
		return nil
	}

	activity.ReasoningItems = reasoningItemsFromMeta(meta["reasoning_items"])
	activity.ReasoningDetails = reasoningDetailsFromMeta(meta["reasoning_details"])

	return activity
}

// reasoningDetailsFromMeta reads the chat-completions reasoning blocks back
// out of a meta map. They are opaque, so whatever shape the round trip gave
// them is re-marshalled verbatim.
func reasoningDetailsFromMeta(value any) json.RawMessage {
	switch typed := value.(type) {
	case nil:
		return nil
	case json.RawMessage:
		return typed
	case string:
		return json.RawMessage(typed)
	default:
		data, err := json.Marshal(typed)
		if err != nil {
			return nil
		}

		return data
	}
}

// reasoningItemsFromMeta reads the reasoning state back out of a meta map.
//
// Typed when the map came straight from threadMeta, which is the live path; the
// JSON round trip covers a conversation that reached here through a session log
// or an embedder, where the same value arrives as decoded maps.
func reasoningItemsFromMeta(raw any) []provider.ReasoningItem {
	if raw == nil {
		return nil
	}

	if items, ok := raw.([]provider.ReasoningItem); ok {
		return items
	}

	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil
	}

	var items []provider.ReasoningItem

	if err := json.Unmarshal(encoded, &items); err != nil {
		return nil
	}

	return items
}
