package loop

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/openzot/openzot/internal/provider"
)

// The Responses transport asks for reasoning state with store:false, which makes
// the copy it streams back the only copy there is. The loop is what has to carry
// it across the turn boundary: a function_call replayed without its reasoning
// item is rejected with "item … was provided without its required 'reasoning'
// item", and that is the second request of every tool round - so a reasoning
// model could not complete a single tool call.
func TestReasoningStateSurvivesAToolRound(t *testing.T) {
	var (
		mu     sync.Mutex
		bodies []string
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)

		mu.Lock()
		round := len(bodies)
		bodies = append(bodies, string(body))
		mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")

		if round == 0 {
			// a reasoning item, then the call it produced
			fmt.Fprint(w, `data: {"type":"response.output_item.done","item":{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"listing first"}],"encrypted_content":"OPAQUE-STATE"}}`+"\n\n")
			fmt.Fprint(w, `data: {"type":"response.output_item.done","item":{"type":"function_call","call_id":"c1","name":"echo","arguments":"{}"}}`+"\n\n")
			fmt.Fprint(w, `data: {"type":"response.completed","response":{"status":"completed"}}`+"\n\n")
			fmt.Fprint(w, "data: [DONE]\n\n")

			return
		}

		fmt.Fprint(w, `data: {"type":"response.output_item.done","item":{"type":"function_call","call_id":"c2","name":"success","arguments":"{\"summary\":\"done\"}"}}`+"\n\n")
		fmt.Fprint(w, `data: {"type":"response.completed","response":{"status":"completed"}}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))

	t.Cleanup(server.Close)

	client, err := provider.New(provider.Config{
		Provider:     provider.Custom,
		Model:        "gpt-5.4-mini",
		APIKey:       "k",
		BaseURL:      server.URL + "/v1",
		UseResponses: true,
	})
	if err != nil {
		t.Fatalf("provider.New: %v", err)
	}

	calls := 0

	result := run(t, Options{
		Client:     client,
		Tools:      echoTool(&calls),
		Messages:   []Message{{Type: TypeUser, Text: "go"}},
		MaxSettles: 5,
	})

	if result.Reason != StopSettled {
		t.Fatalf("reason = %q (%v), want the tool round to complete", result.Reason, result.Err)
	}

	mu.Lock()
	defer mu.Unlock()

	if len(bodies) < 2 {
		t.Fatalf("the run made %d requests, want a second one carrying the replay", len(bodies))
	}

	var second struct {
		Input []struct {
			Type             string `json:"type"`
			ID               string `json:"id"`
			CallID           string `json:"call_id"`
			EncryptedContent string `json:"encrypted_content"`
		} `json:"input"`
	}

	if err := json.Unmarshal([]byte(bodies[1]), &second); err != nil {
		t.Fatalf("decode second request: %v", err)
	}

	var reasoningAt, callAt = -1, -1

	for i, item := range second.Input {
		switch {
		case item.Type == "reasoning" && item.ID == "rs_1":
			reasoningAt = i

			if item.EncryptedContent != "OPAQUE-STATE" {
				t.Errorf("reasoning state = %q, want it replayed verbatim", item.EncryptedContent)
			}

		case item.Type == "function_call" && item.CallID == "c1":
			callAt = i
		}
	}

	if reasoningAt < 0 {
		t.Fatalf("the second request replayed no reasoning item: %s", bodies[1])
	}

	if callAt < 0 {
		t.Fatalf("the second request replayed no function call: %s", bodies[1])
	}

	// order is load-bearing: the item has to precede the call it produced
	if reasoningAt > callAt {
		t.Errorf("reasoning item at %d came after its call at %d", reasoningAt, callAt)
	}

	// and it must be replayed once, not once per call in the turn
	if got := strings.Count(bodies[1], "OPAQUE-STATE"); got != 1 {
		t.Errorf("the reasoning state appears %d times in the replay, want exactly 1", got)
	}
}

// The chat-completions counterpart of reasoning-item replay: a gateway's
// reasoning_details come out of one turn's stream and go back on the assistant
// message that carries that turn's calls - the whole loop away and back. A
// reasoning model whose thinking is dropped between tool rounds degrades, and
// some upstreams reject the request outright once the chain has grown.
func TestReasoningDetailsSurviveTheToolRound(t *testing.T) {
	var replayed string

	turn := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)

		if strings.Contains(string(body), "reasoning_details") {
			replayed = string(body)
		}

		w.Header().Set("Content-Type", "text/event-stream")

		if turn == 0 {
			turn++

			for _, frame := range []string{
				`{"choices":[{"delta":{"reasoning_details":[{"type":"reasoning.text","index":0,"text":"plan the read","signature":"sig-1"}]}}]}`,
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"probe","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`,
			} {
				fmt.Fprintf(w, "data: %s\n\n", frame)
			}
		} else {
			fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"done"},"finish_reason":"stop"}]}`+"\n\n")
		}

		fmt.Fprint(w, "data: [DONE]\n\n")
	}))

	t.Cleanup(server.Close)

	client, err := provider.New(provider.Config{Provider: provider.Custom, Model: "m", APIKey: "k", BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}

	engine, err := New(Options{
		Client:   client,
		Messages: []Message{{Type: TypeUser, Text: "go"}},
		Tools: map[string]ToolDefinition{"probe": {
			Name:       "probe",
			Parameters: map[string]any{"type": "object"},
			Handler:    func(context.Context, map[string]any) (any, error) { return "ok", nil },
		}},
		MaxIterations: 4,
	})
	if err != nil {
		t.Fatal(err)
	}

	result := engine.Run(context.Background(), nil)

	if result.Reason != StopStop {
		t.Fatalf("reason = %q (%s)", result.Reason, result.Message)
	}

	for _, want := range []string{"plan the read", "sig-1"} {
		if !strings.Contains(replayed, want) {
			t.Errorf("the follow-up request is missing %q:\n%s", want, replayed)
		}
	}
}
