package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// responsesRounds serves a scripted Responses exchange, recording every request
// body so a later round can be inspected against what an earlier one returned.
func responsesRounds(t *testing.T, rounds ...[]string) (*Client, *[]responsesRequest) {
	t.Helper()

	var (
		seen  []responsesRequest
		round int
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var captured responsesRequest

		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Errorf("decode request: %v", err)
		}

		seen = append(seen, captured)

		frames := []string{}

		if round < len(rounds) {
			frames = rounds[round]
		}

		round++

		w.Header().Set("Content-Type", "text/event-stream")

		for _, frame := range frames {
			fmt.Fprintf(w, "data: %s\n\n", frame)
		}

		fmt.Fprint(w, "data: [DONE]\n\n")
	}))

	t.Cleanup(server.Close)

	client, err := New(Config{
		Provider:     Custom,
		Model:        "gpt-5.4-mini",
		APIKey:       "k",
		BaseURL:      server.URL + "/v1",
		UseResponses: true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	return client, &seen
}

// With store:false the reasoning item is the only copy there is, and OpenAI
// rejects the whole second request - "item rs_1 was provided without its
// required 'reasoning' item" - when a function_call is replayed without it. So a
// tool round has to carry the item out of the stream and put it back, verbatim
// and ahead of the call it produced.
func TestResponsesReplaysReasoningAheadOfItsToolCall(t *testing.T) {
	client, seen := responsesRounds(t,
		[]string{
			`{"type":"response.output_item.done","item":{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"listing first"}],"encrypted_content":"OPAQUE-STATE"}}`,
			`{"type":"response.output_item.done","item":{"type":"function_call","call_id":"c1","name":"shell","arguments":"{\"cmd\":\"ls\"}"}}`,
			`{"type":"response.completed","response":{"status":"completed"}}`,
		},
		[]string{
			`{"type":"response.output_text.delta","delta":"there is a README"}`,
			`{"type":"response.completed","response":{"status":"completed"}}`,
		},
	)

	first := []ChatMessage{{Role: RoleUser, Content: "list the files"}}

	assistant, finish, _, err := client.Complete(context.Background(), Request{Messages: first})
	if err != nil {
		t.Fatalf("first round: %v", err)
	}

	if finish != FinishToolCalls || len(assistant.ToolCalls) != 1 {
		t.Fatalf("first round returned %q with %d calls", finish, len(assistant.ToolCalls))
	}

	// the state has to reach the caller at all, or there is nothing to replay
	if len(assistant.ReasoningItems) != 1 {
		t.Fatalf("assistant carried %d reasoning items, want 1", len(assistant.ReasoningItems))
	}

	second := append(first, assistant, ChatMessage{
		Role: RoleTool, ToolCallID: "c1", Content: "README.md",
	})

	if _, _, _, err := client.Complete(context.Background(), Request{Messages: second}); err != nil {
		t.Fatalf("second round: %v", err)
	}

	if len(*seen) != 2 {
		t.Fatalf("saw %d requests, want 2", len(*seen))
	}

	input := (*seen)[1].Input

	reasoningAt, callAt := -1, -1

	for index, item := range input {
		switch item.Type {
		case "reasoning":
			reasoningAt = index
		case "function_call":
			callAt = index
		}
	}

	if reasoningAt < 0 {
		t.Fatalf("the second request carried no reasoning item: %+v", input)
	}

	if callAt < 0 {
		t.Fatalf("the second request carried no function_call: %+v", input)
	}

	// order is the requirement: the item has to precede the call it produced
	if reasoningAt > callAt {
		t.Errorf("reasoning item at %d follows its call at %d", reasoningAt, callAt)
	}

	replayed := input[reasoningAt]

	// verbatim: the id links it to the call, and the encrypted blob is the state
	// itself - a re-encoded or dropped field is a rejected request
	if replayed.ID != "rs_1" {
		t.Errorf("reasoning id = %q, want rs_1", replayed.ID)
	}

	if replayed.EncryptedContent != "OPAQUE-STATE" {
		t.Errorf("encrypted_content = %q, want it handed back untouched", replayed.EncryptedContent)
	}

	// the summary field is required on the way back in, so it is always written -
	// and it carries whatever the model actually emitted
	if replayed.Summary == nil {
		t.Fatal("a replayed reasoning item must carry a summary field")
	}

	if len(*replayed.Summary) != 1 {
		t.Errorf("summary = %+v, want the one entry the model returned", *replayed.Summary)
	}
}

// A turn with no reasoning must not gain an empty item: a bare "reasoning" entry
// with no state is not something the API accepts back.
func TestResponsesReplaysNoReasoningWhenNoneWasReturned(t *testing.T) {
	client, seen := responsesRounds(t,
		[]string{
			`{"type":"response.output_item.done","item":{"type":"function_call","call_id":"c1","name":"shell","arguments":"{}"}}`,
			`{"type":"response.completed","response":{"status":"completed"}}`,
		},
		[]string{`{"type":"response.completed","response":{"status":"completed"}}`},
	)

	assistant, _, _, err := client.Complete(context.Background(), Request{
		Messages: []ChatMessage{{Role: RoleUser, Content: "go"}},
	})
	if err != nil {
		t.Fatalf("first round: %v", err)
	}

	if len(assistant.ReasoningItems) != 0 {
		t.Fatalf("reasoning items = %+v, want none", assistant.ReasoningItems)
	}

	if _, _, _, err := client.Complete(context.Background(), Request{
		Messages: []ChatMessage{{Role: RoleUser, Content: "go"}, assistant},
	}); err != nil {
		t.Fatalf("second round: %v", err)
	}

	for _, item := range (*seen)[1].Input {
		if item.Type == "reasoning" {
			t.Errorf("an empty reasoning item was invented: %+v", item)
		}
	}
}
