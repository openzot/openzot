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

// serve returns a client pointed at a handler standing in for a provider.
func serve(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()

	server := httptest.NewServer(handler)

	t.Cleanup(server.Close)

	client, err := New(Config{
		Provider: Custom,
		Model:    "test-model",
		APIKey:   "test-key",
		BaseURL:  server.URL,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	return client
}

// frames serves a fixed set of SSE frames.
func frames(t *testing.T, lines ...string) *Client {
	t.Helper()

	return serve(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")

		for _, line := range lines {
			fmt.Fprintf(w, "data: %s\n\n", line)
		}

		fmt.Fprint(w, "data: [DONE]\n\n")
	})
}

func drain(t *testing.T, client *Client) (text, reasoning string, calls []ToolCall, finish string, usage *Usage) {
	t.Helper()

	for event := range client.Stream(context.Background(), Request{}) {
		if event.Err != nil {
			t.Fatalf("stream: %v", event.Err)
		}

		text += event.Token
		reasoning += event.ReasoningToken

		if len(event.ToolCalls) > 0 {
			calls = event.ToolCalls
		}

		if event.FinishReason != "" {
			finish = event.FinishReason
		}

		if event.Usage != nil {
			usage = event.Usage
		}
	}

	return text, reasoning, calls, finish, usage
}

func TestStreamAssemblesText(t *testing.T) {
	client := frames(t,
		`{"choices":[{"delta":{"content":"hello "}}]}`,
		`{"choices":[{"delta":{"content":"world"}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
	)

	text, _, _, finish, _ := drain(t, client)

	if text != "hello world" {
		t.Errorf("text = %q, want %q", text, "hello world")
	}

	if finish != FinishStop {
		t.Errorf("finish = %q, want stop", finish)
	}
}

// Tool calls arrive fragmented and identified only by index; a caller must never
// see a half-built call.
func TestStreamAssemblesFragmentedToolCalls(t *testing.T) {
	client := frames(t,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"search","arguments":"{\"q\":"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"go\"}"}}]}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
	)

	_, _, calls, finish, _ := drain(t, client)

	if len(calls) != 1 {
		t.Fatalf("got %d calls, want 1", len(calls))
	}

	if calls[0].Function.Arguments != `{"q":"go"}` {
		t.Errorf("arguments = %q, want them joined", calls[0].Function.Arguments)
	}

	if finish != FinishToolCalls {
		t.Errorf("finish = %q, want tool_calls", finish)
	}
}

// Several calls in one turn must come back in the order the indices imply.
func TestStreamAssemblesParallelToolCalls(t *testing.T) {
	client := frames(t,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"a","function":{"name":"first","arguments":"{}"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":1,"id":"b","function":{"name":"second","arguments":"{}"}}]}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
	)

	_, _, calls, _, _ := drain(t, client)

	if len(calls) != 2 {
		t.Fatalf("got %d calls, want 2", len(calls))
	}

	if calls[0].Function.Name != "first" || calls[1].Function.Name != "second" {
		t.Errorf("calls out of order: %+v", calls)
	}
}

// A provider that ends a turn with tool calls but omits finish_reason must not
// read as a plain stop.
func TestStreamInfersToolCallFinish(t *testing.T) {
	client := frames(t,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"a","function":{"name":"t","arguments":"{}"}}]}}]}`,
	)

	_, _, calls, finish, _ := drain(t, client)

	if len(calls) != 1 {
		t.Fatalf("got %d calls, want 1", len(calls))
	}

	if finish != FinishToolCalls {
		t.Errorf("finish = %q, want it inferred as tool_calls", finish)
	}
}

// Providers disagree on the reasoning field name.
func TestStreamAcceptsBothReasoningFields(t *testing.T) {
	for _, field := range []string{"reasoning_content", "reasoning"} {
		client := frames(t,
			fmt.Sprintf(`{"choices":[{"delta":{%q:"thinking"}}]}`, field),
			`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
		)

		_, reasoning, _, _, _ := drain(t, client)

		if reasoning != "thinking" {
			t.Errorf("%s: reasoning = %q, want %q", field, reasoning, "thinking")
		}
	}
}

func TestStreamCapturesUsage(t *testing.T) {
	client := frames(t,
		`{"choices":[{"delta":{"content":"x"}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`,
	)

	_, _, _, _, usage := drain(t, client)

	if usage == nil {
		t.Fatal("usage was not reported")
	}

	if usage.TotalTokens != 12 {
		t.Errorf("total tokens = %d, want 12", usage.TotalTokens)
	}
}

// Gateways interleave keep-alives and non-JSON notices; a malformed frame must
// not kill the stream.
func TestStreamSkipsMalformedFrames(t *testing.T) {
	client := frames(t,
		`not json at all`,
		`{"choices":[{"delta":{"content":"survived"}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
	)

	text, _, _, _, _ := drain(t, client)

	if text != "survived" {
		t.Errorf("text = %q, want the stream to continue past a bad frame", text)
	}
}

// An error delivered inside a 200 stream must still terminate the turn.
func TestStreamSurfacesInBandError(t *testing.T) {
	client := frames(t, `{"error":{"message":"upstream exploded","type":"server_error"}}`)

	var got error

	for event := range client.Stream(context.Background(), Request{}) {
		if event.Err != nil {
			got = event.Err
		}
	}

	if got == nil {
		t.Fatal("an in-band error must surface")
	}

	if !strings.Contains(got.Error(), "upstream exploded") {
		t.Errorf("error = %v, want the provider's message", got)
	}
}

func TestStreamClassifiesHTTPErrors(t *testing.T) {
	client := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)

		json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{"message": "Service temporarily unavailable"},
		})
	})

	var got error

	for event := range client.Stream(context.Background(), Request{}) {
		if event.Err != nil {
			got = event.Err
		}
	}

	if got == nil {
		t.Fatal("a 503 must surface as an error")
	}

	if !IsRetriable(got) {
		t.Errorf("a 503 must be retriable: %v", got)
	}
}

func TestStreamSendsCredentialAndModel(t *testing.T) {
	var (
		auth string
		body map[string]any
	)

	client := serve(t, func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")

		json.NewDecoder(r.Body).Decode(&body)

		w.Header().Set("Content-Type", "text/event-stream")

		fmt.Fprint(w, "data: [DONE]\n\n")
	})

	for range client.Stream(context.Background(), Request{}) {
	}

	if auth != "Bearer test-key" {
		t.Errorf("Authorization = %q", auth)
	}

	if body["model"] != "test-model" {
		t.Errorf("model = %v, want test-model", body["model"])
	}

	if body["stream"] != true {
		t.Errorf("stream = %v, want true", body["stream"])
	}
}

func TestStreamCancellation(t *testing.T) {
	client := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")

		flusher, _ := w.(http.Flusher)

		// never send [DONE]
		for i := 0; i < 1000; i++ {
			fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"x"}}]}`+"\n\n")

			if flusher != nil {
				flusher.Flush()
			}
		}
	})

	ctx, cancel := context.WithCancel(context.Background())

	events := client.Stream(ctx, Request{})

	<-events

	cancel()

	// draining must terminate rather than hang
	for range events {
	}
}

func TestCompleteCollectsTheStream(t *testing.T) {
	client := frames(t,
		`{"choices":[{"delta":{"content":"the "}}]}`,
		`{"choices":[{"delta":{"content":"answer"}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
	)

	message, finish, _, err := client.Complete(context.Background(), Request{})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if message.Content != "the answer" {
		t.Errorf("content = %q", message.Content)
	}

	if message.Role != RoleAssistant {
		t.Errorf("role = %q, want assistant", message.Role)
	}

	if finish != FinishStop {
		t.Errorf("finish = %q", finish)
	}
}
