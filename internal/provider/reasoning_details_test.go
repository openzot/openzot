package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A gateway's structured reasoning blocks must come out of the stream and go
// back in verbatim: a reasoning model interleaves thinking with its tool
// calls, and dropping the blocks degrades its continuity at best - at worst
// the upstream rejects the request once the chain has grown. This is the
// chat-completions counterpart of the Responses transport's reasoning items.
func TestReasoningDetailsAreCapturedAndReplayed(t *testing.T) {
	var replayed json.RawMessage

	turn := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Role             string          `json:"role"`
				ReasoningDetails json.RawMessage `json:"reasoning_details"`
			} `json:"messages"`
		}

		_ = json.NewDecoder(r.Body).Decode(&req)

		for _, m := range req.Messages {
			if m.Role == "assistant" && len(m.ReasoningDetails) > 0 {
				replayed = m.ReasoningDetails
			}
		}

		w.Header().Set("Content-Type", "text/event-stream")

		if turn == 0 {
			turn++

			// the text detail streams as fragments sharing an index; the
			// signature arrives on a late fragment
			for _, frame := range []string{
				`{"choices":[{"delta":{"reasoning_details":[{"type":"reasoning.text","index":0,"text":"thinking ha","format":"stealth-v1"}]}}]}`,
				`{"choices":[{"delta":{"reasoning_details":[{"type":"reasoning.text","index":0,"text":"rd about it","signature":"sig-abc"}]}}]}`,
				`{"choices":[{"delta":{"reasoning_details":[{"type":"reasoning.encrypted","index":1,"data":"opaque-blob","id":"enc-1"}]}}]}`,
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call-1","type":"function","function":{"name":"read","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`,
			} {
				fmt.Fprintf(w, "data: %s\n\n", frame)
			}
		} else {
			fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"done"},"finish_reason":"stop"}]}`+"\n\n")
		}

		fmt.Fprint(w, "data: [DONE]\n\n")
	}))

	defer server.Close()

	client, err := New(Config{Provider: Custom, Model: "m", APIKey: "k", BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}

	// first turn: capture the details off the final event
	var details json.RawMessage

	for event := range client.Stream(context.Background(), Request{Messages: []ChatMessage{{Role: RoleUser, Content: "go"}}}) {
		if event.Err != nil {
			t.Fatalf("stream: %v", event.Err)
		}

		if len(event.ReasoningDetails) > 0 {
			details = event.ReasoningDetails
		}
	}

	var blocks []map[string]any

	if err := json.Unmarshal(details, &blocks); err != nil {
		t.Fatalf("details are not a JSON array: %v", err)
	}

	if len(blocks) != 2 {
		t.Fatalf("blocks = %d, want the text fragments merged and the encrypted block kept", len(blocks))
	}

	if blocks[0]["text"] != "thinking hard about it" || blocks[0]["signature"] != "sig-abc" {
		t.Errorf("merged text block = %v", blocks[0])
	}

	if blocks[1]["data"] != "opaque-blob" {
		t.Errorf("encrypted block = %v", blocks[1])
	}

	// second turn: replay them on the assistant message and confirm the wire
	// carries them verbatim
	second := Request{Messages: []ChatMessage{
		{Role: RoleUser, Content: "go"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "call-1", Type: "function", Function: FunctionCall{Name: "read", Arguments: "{}"}}}, ReasoningDetails: details},
		{Role: RoleTool, ToolCallID: "call-1", Name: "read", Content: "result"},
	}}

	if _, _, _, err := client.Complete(context.Background(), second); err != nil {
		t.Fatalf("second turn: %v", err)
	}

	if !strings.Contains(string(replayed), "thinking hard about it") || !strings.Contains(string(replayed), "opaque-blob") {
		t.Errorf("the wire did not carry the details back: %s", replayed)
	}
}
