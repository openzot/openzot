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

func TestResponsesIsSelectedForReasoningModelsOnOpenAI(t *testing.T) {
	tests := []struct {
		name   string
		config Config
		want   bool
	}{
		{
			// the case it exists for: chat-completions has nowhere to carry
			// reasoning state between tool rounds
			name:   "an OpenAI reasoning model",
			config: Config{Provider: OpenAI, Model: "gpt-5.4-mini", APIKey: "k"},
			want:   true,
		},
		{
			name:   "an OpenAI non-reasoning model",
			config: Config{Provider: OpenAI, Model: "gpt-4o", APIKey: "k"},
			want:   false,
		},
		{
			// only OpenAI implements it; sending a Responses request elsewhere
			// fails outright
			name:   "a reasoning model on another provider",
			config: Config{Provider: Groq, Model: "glm-5.2", APIKey: "k"},
			want:   false,
		},
		{
			name:   "explicitly requested anywhere",
			config: Config{Provider: Custom, Model: "m", APIKey: "k", BaseURL: "https://gw.example.com/v1", UseResponses: true},
			want:   true,
		},
		{
			name:   "explicitly disabled beats automatic selection",
			config: Config{Provider: OpenAI, Model: "gpt-5.4-mini", APIKey: "k", DisableResponses: true},
			want:   false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resolved, err := test.config.Resolve()
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}

			if resolved.UseResponses != test.want {
				t.Errorf("UseResponses = %v, want %v", resolved.UseResponses, test.want)
			}
		})
	}
}

// The Responses shape differs from chat-completions in three ways that each
// break the request if got wrong.
func TestToResponsesInput(t *testing.T) {
	instructions, items := toResponsesInput([]ChatMessage{
		{Role: RoleSystem, Content: "you are an agent"},
		{Role: RoleSystem, Content: "and be careful"},
		{Role: RoleUser, Content: "list the files"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{
			ID: "c1", Function: FunctionCall{Name: "shell", Arguments: `{"cmd":"ls"}`},
		}}},
		{Role: RoleTool, ToolCallID: "c1", Content: "README.md"},
		{Role: RoleAssistant, Content: "there is a README"},
	})

	// system turns become top-level instructions, concatenated rather than the
	// last one silently winning
	if instructions != "you are an agent\n\nand be careful" {
		t.Errorf("instructions = %q", instructions)
	}

	if len(items) != 4 {
		t.Fatalf("got %d items, want 4: %+v", len(items), items)
	}

	if items[0].Type != "message" || items[0].Role != RoleUser {
		t.Errorf("items[0] = %+v, want a user message", items[0])
	}

	// a tool call and its result are sibling items linked by call_id, not a
	// nested structure
	if items[1].Type != "function_call" || items[1].CallID != "c1" || items[1].Name != "shell" {
		t.Errorf("items[1] = %+v, want a function_call", items[1])
	}

	if items[2].Type != "function_call_output" || items[2].CallID != "c1" || items[2].Output != "README.md" {
		t.Errorf("items[2] = %+v, want a function_call_output", items[2])
	}

	if items[3].Type != "message" || items[3].Role != RoleAssistant {
		t.Errorf("items[3] = %+v, want an assistant message", items[3])
	}
}

// Tools are flat here - no nested "function" object.
func TestToResponsesTools(t *testing.T) {
	tools := toResponsesTools([]Tool{{
		Type: "function",
		Function: ToolFunction{
			Name:        "shell",
			Description: "run a command",
			Parameters:  map[string]any{"type": "object"},
		},
	}})

	if len(tools) != 1 {
		t.Fatalf("got %d tools, want 1", len(tools))
	}

	if tools[0].Name != "shell" || tools[0].Type != "function" {
		t.Errorf("tool = %+v, want a flat function definition", tools[0])
	}
}

// responsesServer serves typed Responses frames.
func responsesServer(t *testing.T, lines ...string) (*Client, *responsesRequest) {
	t.Helper()

	captured := &responsesRequest{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Errorf("path = %q, want /v1/responses", r.URL.Path)
		}

		json.NewDecoder(r.Body).Decode(captured)

		w.Header().Set("Content-Type", "text/event-stream")

		for _, line := range lines {
			fmt.Fprintf(w, "data: %s\n\n", line)
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

	return client, captured
}

func TestResponsesStreamsText(t *testing.T) {
	client, captured := responsesServer(t,
		`{"type":"response.output_text.delta","delta":"hello "}`,
		`{"type":"response.output_text.delta","delta":"world"}`,
		`{"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":5,"output_tokens":2,"total_tokens":7}}}`,
	)

	var (
		text   string
		finish string
		usage  *Usage
	)

	for event := range client.Stream(context.Background(), Request{
		Messages: []ChatMessage{{Role: RoleUser, Content: "hi"}},
	}) {
		if event.Err != nil {
			t.Fatalf("stream: %v", event.Err)
		}

		text += event.Token

		if event.FinishReason != "" {
			finish = event.FinishReason
		}

		if event.Usage != nil {
			usage = event.Usage
		}
	}

	if text != "hello world" {
		t.Errorf("text = %q", text)
	}

	if finish != FinishStop {
		t.Errorf("finish = %q, want stop", finish)
	}

	if usage == nil || usage.TotalTokens != 7 {
		t.Errorf("usage = %+v, want 7 total tokens", usage)
	}

	// nothing is stored server-side, so reasoning state has to come back inline
	if captured.Store {
		t.Error("store must be false: zot keeps no server-side conversation")
	}

	if len(captured.Include) == 0 {
		t.Error("encrypted reasoning content must be requested so it can be replayed")
	}
}

// Tool calls arrive as complete items here, so there is no index reassembly -
// but the finish reason still has to match what the loop expects.
func TestResponsesStreamsToolCalls(t *testing.T) {
	client, _ := responsesServer(t,
		`{"type":"response.output_item.done","item":{"type":"function_call","call_id":"c1","name":"shell","arguments":"{\"cmd\":\"ls\"}"}}`,
		`{"type":"response.completed","response":{"status":"completed"}}`,
	)

	var (
		calls  []ToolCall
		finish string
	)

	for event := range client.Stream(context.Background(), Request{}) {
		if event.Err != nil {
			t.Fatalf("stream: %v", event.Err)
		}

		if len(event.ToolCalls) > 0 {
			calls = event.ToolCalls
		}

		if event.FinishReason != "" {
			finish = event.FinishReason
		}
	}

	if len(calls) != 1 {
		t.Fatalf("got %d calls, want 1", len(calls))
	}

	if calls[0].ID != "c1" || calls[0].Function.Name != "shell" {
		t.Errorf("call = %+v", calls[0])
	}

	if finish != FinishToolCalls {
		t.Errorf("finish = %q, want tool_calls", finish)
	}
}

func TestResponsesStreamsReasoning(t *testing.T) {
	client, _ := responsesServer(t,
		`{"type":"response.reasoning_summary_text.delta","delta":"considering"}`,
		`{"type":"response.completed","response":{"status":"completed"}}`,
	)

	var reasoning string

	for event := range client.Stream(context.Background(), Request{}) {
		if event.Err != nil {
			t.Fatalf("stream: %v", event.Err)
		}

		reasoning += event.ReasoningToken
	}

	if reasoning != "considering" {
		t.Errorf("reasoning = %q", reasoning)
	}
}

// An incomplete response that ran out of output must map onto the same
// truncation recovery the chat path uses, or the loop would treat it as a
// finished answer.
func TestResponsesMapsTruncation(t *testing.T) {
	client, _ := responsesServer(t,
		`{"type":"response.output_text.delta","delta":"half an answ"}`,
		`{"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}}`,
	)

	var finish string

	for event := range client.Stream(context.Background(), Request{}) {
		if event.Err != nil {
			t.Fatalf("stream: %v", event.Err)
		}

		if event.FinishReason != "" {
			finish = event.FinishReason
		}
	}

	if finish != FinishLength {
		t.Errorf("finish = %q, want length", finish)
	}
}

func TestResponsesSurfacesFailure(t *testing.T) {
	client, _ := responsesServer(t,
		`{"type":"response.failed","error":{"message":"the model is overloaded"}}`,
	)

	var got error

	for event := range client.Stream(context.Background(), Request{}) {
		if event.Err != nil {
			got = event.Err
		}
	}

	if got == nil {
		t.Fatal("a failed response must surface")
	}

	// "overloaded" is one of the transient wordings worth retrying
	if !IsRetriable(got) {
		t.Errorf("an overload should be retriable: %v", got)
	}
}

func TestTransportIsReported(t *testing.T) {
	client, _ := responsesServer(t)

	if got := client.Transport(); got != "responses" {
		t.Errorf("Transport = %q, want responses", got)
	}

	chat, err := New(Config{Provider: OpenAI, Model: "gpt-4o", APIKey: "k"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if got := chat.Transport(); got != "chat-completions" {
		t.Errorf("Transport = %q, want chat-completions", got)
	}
}

// Request.Stop is public API and the chat transport sends it. The Responses API
// has no stop-sequence parameter at all, so dropping the sequences silently
// would let a caller believe generation is bounded when nothing bounds it.
func TestResponsesRefusesStopSequences(t *testing.T) {
	client, _ := responsesServer(t,
		`{"type":"response.completed","response":{"status":"completed"}}`,
	)

	var failed error

	for event := range client.Stream(context.Background(), Request{Stop: []string{"\n\nUser:"}}) {
		if event.Err != nil {
			failed = event.Err
		}
	}

	if failed == nil {
		t.Fatal("stop sequences the transport cannot honour must be reported, not dropped")
	}

	if !strings.Contains(failed.Error(), "stop") {
		t.Errorf("error = %v, want it to name what could not be honoured", failed)
	}
}
