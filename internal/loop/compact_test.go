package loop

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openzot/openzot/internal/provider"
)

// The context-limit recovery path: a provider rejecting an oversized prompt is
// not a failure, it is a signal to condense and try again. This is the one
// recovery that has to work without a model call - asking the provider to
// summarise is what just got rejected.

// contextLimitOnce rejects the first request with a context-length error and
// serves a normal turn afterwards.
func contextLimitOnce(t *testing.T) (*provider.Client, *int) {
	t.Helper()

	requests := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++

		if requests == 1 {
			w.WriteHeader(http.StatusBadRequest)

			json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]any{
					"message": "This model's maximum context length is 8192 tokens, however you requested 9000",
				},
			})

			return
		}

		w.Header().Set("Content-Type", "text/event-stream")

		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"recovered"}}]}`+"\n\n")
		fmt.Fprint(w, `data: {"choices":[{"delta":{},"finish_reason":"stop"}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))

	t.Cleanup(server.Close)

	client, err := provider.New(provider.Config{
		Provider: provider.Custom,
		Model:    "test-model",
		APIKey:   "k",
		BaseURL:  server.URL,
	})
	if err != nil {
		t.Fatalf("provider.New: %v", err)
	}

	return client, &requests
}

// longConversation builds enough history to be worth compacting.
func longConversation(turns int) []Message {
	messages := make([]Message, 0, turns)

	for index := 0; index < turns; index++ {
		kind := TypeUser

		if index%2 == 1 {
			kind = TypeBot
		}

		messages = append(messages, Message{
			Type: kind,
			Text: fmt.Sprintf("turn %d: %s", index, strings.Repeat("padding ", 200)),
		})
	}

	return messages
}

func TestContextLimitTriggersCompaction(t *testing.T) {
	client, requests := contextLimitOnce(t)

	engine, err := New(Options{
		Client:   client,
		Messages: longConversation(40),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// force the budget low enough that the history genuinely needs condensing
	engine.inputBudget = 2_000

	result := engine.Run(context.Background(), nil)

	if result.Reason != StopStop {
		t.Fatalf("reason = %q, want the run to recover and stop normally", result.Reason)
	}

	if *requests < 2 {
		t.Errorf("the request was not retried after compaction (%d requests)", *requests)
	}

	if result.Budget.Continuations != 1 {
		t.Errorf("continuations = %d, want the compaction to count as one", result.Budget.Continuations)
	}

	// the condensed history must be in the thread, and the originals gone
	var summarised bool

	for _, message := range result.Messages {
		if message.Type == TypeCheckpoint && strings.Contains(message.Text, "condensed") {
			summarised = true
		}
	}

	if !summarised {
		t.Error("no summary message was inserted")
	}
}

func TestCompactDeclinesWhenThereIsNothingToCondense(t *testing.T) {
	client, _ := contextLimitOnce(t)

	engine, err := New(Options{Client: client})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// a short conversation is below the floor where a summary is worth its cost
	if _, ok := engine.compact([]Message{{Type: TypeUser, Text: "hi"}}); ok {
		t.Error("compaction must decline on a conversation too short to be worth it")
	}
}

func TestCompactCondensesAndKeepsTheTail(t *testing.T) {
	client, _ := contextLimitOnce(t)

	engine, err := New(Options{Client: client})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	engine.inputBudget = 2_000

	original := longConversation(40)

	compacted, ok := engine.compact(original)

	if !ok {
		t.Fatal("expected the conversation to be compacted")
	}

	if len(compacted) >= len(original) {
		t.Errorf("compacted to %d messages from %d; it should be shorter", len(compacted), len(original))
	}

	// the newest turn survives verbatim - it is the one the model has to act on
	newest := original[len(original)-1].Text

	var kept bool

	for _, message := range compacted {
		if message.Text == newest {
			kept = true
		}
	}

	if !kept {
		t.Error("the most recent turn must survive compaction verbatim")
	}
}

// The system prompt must never be summarised away, and must stay ahead of the summary
// so the model reads its instructions before the condensed history.
func TestCompactKeepsInstructionsFirst(t *testing.T) {
	client, _ := contextLimitOnce(t)

	engine, err := New(Options{Client: client})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	engine.inputBudget = 2_000

	messages := append(
		[]Message{{Type: TypeInstructions, Text: "you are an agent with a specific persona"}},
		longConversation(40)...,
	)

	compacted, ok := engine.compact(messages)

	if !ok {
		t.Fatal("expected the conversation to be compacted")
	}

	if compacted[0].Type != TypeInstructions {
		t.Fatalf("first message is %q, want the instructions to stay in front", compacted[0].Type)
	}

	if !strings.Contains(compacted[0].Text, "specific persona") {
		t.Error("the instructions must survive verbatim, not be summarised")
	}

	if compacted[1].Type != TypeCheckpoint {
		t.Errorf("second message is %q, want the summary directly after the instructions", compacted[1].Type)
	}
}

// A context limit that persists is eventually a real failure rather than an
// infinite compaction loop.
func TestPersistentContextLimitGivesUp(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)

		json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{"message": "maximum context length exceeded"},
		})
	}))

	defer server.Close()

	client, err := provider.New(provider.Config{
		Provider: provider.Custom,
		Model:    "test-model",
		APIKey:   "k",
		BaseURL:  server.URL,
	})
	if err != nil {
		t.Fatalf("provider.New: %v", err)
	}

	engine, err := New(Options{
		Client:           client,
		Messages:         longConversation(40),
		MaxContinuations: 3,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	engine.inputBudget = 2_000

	result := engine.Run(context.Background(), nil)

	if result.Reason != StopError {
		t.Errorf("reason = %q, want error once compaction stops helping", result.Reason)
	}

	if result.Err == nil {
		t.Error("the underlying provider error must be reported")
	}
}

// A transient provider failure is retried rather than ending the run.
func TestRetriableProviderErrorIsRetried(t *testing.T) {
	requests := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++

		if requests == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)

			fmt.Fprint(w, `{"error":{"message":"Service temporarily unavailable"}}`)

			return
		}

		w.Header().Set("Content-Type", "text/event-stream")

		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"second time lucky"}}]}`+"\n\n")
		fmt.Fprint(w, `data: {"choices":[{"delta":{},"finish_reason":"stop"}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))

	defer server.Close()

	client, err := provider.New(provider.Config{
		Provider: provider.Custom,
		Model:    "test-model",
		APIKey:   "k",
		BaseURL:  server.URL,
	})
	if err != nil {
		t.Fatalf("provider.New: %v", err)
	}

	engine, err := New(Options{
		Client:   client,
		Messages: []Message{{Type: TypeUser, Text: "go"}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var retried bool

	result := engine.Run(context.Background(), func(event Event) {
		if event.Kind == EventRetry {
			retried = true
		}
	})

	if result.Reason != StopStop {
		t.Errorf("reason = %q, want the retry to succeed", result.Reason)
	}

	if !retried {
		t.Error("a retry must be visible to the caller")
	}
}

// A 4xx that is not a context limit is terminal: retrying a bad key or a missing
// model only burns the budget.
func TestNonRetriableErrorEndsTheRun(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)

		fmt.Fprint(w, `{"error":{"message":"invalid api key"}}`)
	}))

	defer server.Close()

	client, err := provider.New(provider.Config{
		Provider: provider.Custom,
		Model:    "test-model",
		APIKey:   "k",
		BaseURL:  server.URL,
	})
	if err != nil {
		t.Fatalf("provider.New: %v", err)
	}

	engine, err := New(Options{
		Client:   client,
		Messages: []Message{{Type: TypeUser, Text: "go"}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	result := engine.Run(context.Background(), nil)

	if result.Reason != StopError {
		t.Errorf("reason = %q, want error", result.Reason)
	}

	if result.Budget.Continuations != 0 {
		t.Errorf("continuations = %d, want no retries for a credential problem", result.Budget.Continuations)
	}
}

// When a provider rejects for length it states the real window. That is ground
// truth in a way the local catalogue is not, so the retry budgets against it
// rather than guessing again.
func TestContextLimitAdoptsTheProviderStatedWindow(t *testing.T) {
	requests := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++

		if requests == 1 {
			w.WriteHeader(http.StatusBadRequest)

			json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]any{
					"message": "This model's maximum context length is 8192 tokens. However, your messages resulted in 40000 tokens.",
				},
			})

			return
		}

		w.Header().Set("Content-Type", "text/event-stream")

		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"fits now"}}]}`+"\n\n")
		fmt.Fprint(w, `data: {"choices":[{"delta":{},"finish_reason":"stop"}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))

	defer server.Close()

	client, err := provider.New(provider.Config{
		Provider: provider.Custom,
		Model:    "test-model",
		APIKey:   "k",
		BaseURL:  server.URL,
	})
	if err != nil {
		t.Fatalf("provider.New: %v", err)
	}

	engine, err := New(Options{
		Client:   client,
		Messages: longConversation(40),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// the catalogue's guess is wildly optimistic for this model
	before := engine.inputBudget

	result := engine.Run(context.Background(), nil)

	if result.Reason != StopStop {
		t.Fatalf("reason = %q, want the run to recover", result.Reason)
	}

	// 85% of the stated 8192
	if engine.inputBudget != 6963 {
		t.Errorf("input budget = %d, want 6963 (85%% of the stated 8192); was %d",
			engine.inputBudget, before)
	}
}

// A rejection with no number still recovers, using the engine's own estimate.
func TestContextLimitWithoutANumberStillCompacts(t *testing.T) {
	requests := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++

		if requests == 1 {
			w.WriteHeader(http.StatusBadRequest)

			json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]any{"message": "prompt is too long"},
			})

			return
		}

		w.Header().Set("Content-Type", "text/event-stream")

		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"ok"}}]}`+"\n\n")
		fmt.Fprint(w, `data: {"choices":[{"delta":{},"finish_reason":"stop"}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))

	defer server.Close()

	client, _ := provider.New(provider.Config{
		Provider: provider.Custom,
		Model:    "test-model",
		APIKey:   "k",
		BaseURL:  server.URL,
	})

	engine, err := New(Options{Client: client, Messages: longConversation(40)})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	engine.inputBudget = 2_000

	if result := engine.Run(context.Background(), nil); result.Reason != StopStop {
		t.Errorf("reason = %q, want the run to recover", result.Reason)
	}
}
