package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// max_tokens is only useful if it reaches the provider - the whole point is to
// cap the response, which the model never sees unless it is in the request body.
// These pin the wire, on both transports, and pin that "unset" sends nothing
// rather than a zero that some providers reject.

// captureBody runs one turn and returns the raw request body the client sent.
func captureBody(t *testing.T, config Config, request Request) []byte {
	t.Helper()

	seen := make(chan []byte, 1)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)

		select {
		case seen <- body:
		default:
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\n"))
		w.Write([]byte("data: [DONE]\n\n"))
	}))

	t.Cleanup(server.Close)

	config.BaseURL = server.URL

	client, err := New(config)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	for event := range client.Stream(context.Background(), request) {
		if event.Err != nil {
			t.Fatalf("stream: %v", event.Err)
		}
	}

	select {
	case body := <-seen:
		return body
	default:
		t.Fatal("the provider was never called")

		return nil
	}
}

// The chat-completions transport sends `max_tokens`.
func TestChatSendsMaxTokens(t *testing.T) {
	limit := 4096

	body := captureBody(t,
		Config{Provider: OpenAI, Model: "gpt-4o", APIKey: "k"},
		Request{Messages: []ChatMessage{{Role: RoleUser, Content: "hi"}}, MaxTokens: &limit})

	var payload struct {
		MaxTokens *int `json:"max_tokens"`
	}

	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode request: %v", err)
	}

	if payload.MaxTokens == nil || *payload.MaxTokens != 4096 {
		t.Errorf("request max_tokens = %v, want 4096 on the wire", payload.MaxTokens)
	}
}

// An unset cap must omit the field entirely, not send zero - a provider that
// reads max_tokens:0 as "no output allowed" would return nothing.
func TestChatOmitsMaxTokensWhenUnset(t *testing.T) {
	body := captureBody(t,
		Config{Provider: OpenAI, Model: "gpt-4o", APIKey: "k"},
		Request{Messages: []ChatMessage{{Role: RoleUser, Content: "hi"}}})

	if raw := map[string]json.RawMessage{}; json.Unmarshal(body, &raw) == nil {
		if _, present := raw["max_tokens"]; present {
			t.Errorf("max_tokens must be absent when unset, body: %s", body)
		}
	}
}

// The Responses transport carries the same cap as `max_output_tokens`, the name
// that API uses.
// The Responses transport carries the same cap as `max_output_tokens`, the name
// that API uses.
func TestResponsesSendsMaxOutputTokens(t *testing.T) {
	limit := 2048

	body := captureBody(t,
		// forced, not inferred: captureBody points the connection at its own
		// endpoint, and an overridden endpoint is never assumed to speak
		// Responses (see TestResponsesIsSelectedForReasoningModelsOnOpenAI)
		Config{Provider: OpenAI, Model: "gpt-5.4-mini", APIKey: "k", UseResponses: true},
		Request{Messages: []ChatMessage{{Role: RoleUser, Content: "hi"}}, MaxTokens: &limit})

	var payload struct {
		MaxOutputTokens *int `json:"max_output_tokens"`
	}

	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode request: %v", err)
	}

	if payload.MaxOutputTokens == nil || *payload.MaxOutputTokens != 2048 {
		t.Errorf("request max_output_tokens = %v, want 2048", payload.MaxOutputTokens)
	}
}
