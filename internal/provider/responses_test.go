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
			// an overridden endpoint is somebody else's, however openai-shaped
			// its driver: assuming it implements Responses fails the run on its
			// first turn with a bare 404, and the catalogue knows nothing about
			// what the endpoint behind the base_url actually serves
			name:   "an OpenAI reasoning model on an overridden endpoint",
			config: Config{Provider: OpenAI, Model: "gpt-5.4-mini", APIKey: "k", BaseURL: "https://proxy.example.com/v1"},
			want:   false,
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

// /responses with a bare 404 - the exact failure this selection rule exists to
// prevent - so a single stray path here is the bug again.
func TestAnOverriddenEndpointIsSpokenToInChatCompletions(t *testing.T) {
	var paths []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)

		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))

	t.Cleanup(server.Close)

	// glm-5.2 is catalogued as a reasoning model, so under the old rule an
	// openai-driver connection carrying it selected Responses whatever the
	// endpoint was
	client, err := New(Config{
		Provider: OpenAI,
		Model:    "glm-5.2",
		APIKey:   "k",
		BaseURL:  server.URL,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	//nolint:errcheck // the stub answers with an empty stream; the path is the point
	_, _, _, _ = client.Complete(context.Background(), Request{
		Messages: []ChatMessage{{Role: "user", Content: "hi"}},
	})

	if len(paths) == 0 {
		t.Fatal("the server saw no request")
	}

	for _, path := range paths {
		if !strings.HasSuffix(path, "/chat/completions") {
			t.Errorf("the run sent %q to an overridden endpoint; want chat-completions", path)
		}
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

func TestResponsesInputCarriesImagesAsInputImageParts(t *testing.T) {
	image := NewImage([]byte{7, 7, 7}, "image/png", 40, 20)
	image.Detail = "high"

	_, items := toResponsesInput([]ChatMessage{
		{Role: RoleUser, Content: "Attached: /tmp/shot.png", Images: []Image{image}},
	})

	if len(items) != 1 {
		t.Fatalf("got %d items, want one message", len(items))
	}

	content, ok := items[0].Content.([]map[string]any)

	if !ok {
		t.Fatalf("content is %T, want an array of parts", items[0].Content)
	}

	if len(content) != 2 {
		t.Fatalf("got %d parts, want the text and the image", len(content))
	}

	if content[0]["type"] != "input_text" {
		t.Errorf("first part = %v, want the text", content[0])
	}

	// the Responses spelling differs from chat-completions: a flat image_url
	// string rather than a nested object
	if content[1]["type"] != "input_image" {
		t.Fatalf("second part = %v, want input_image", content[1])
	}

	url, _ := content[1]["image_url"].(string)

	if !strings.HasPrefix(url, "data:image/png;base64,") {
		t.Errorf("image_url = %q, want a data URL", url)
	}

	if content[1]["detail"] != "high" {
		t.Errorf("detail = %v, want the hint carried", content[1]["detail"])
	}
}

func TestResponsesInputSkipsAnImageWithNoBytes(t *testing.T) {
	_, items := toResponsesInput([]ChatMessage{
		{Role: RoleUser, Content: "described only", Images: []Image{{MediaType: "image/png", Digest: "sha256:gone"}}},
	})

	content := items[0].Content.([]map[string]any)

	if len(content) != 1 || content[0]["type"] != "input_text" {
		t.Errorf("content = %v, want just the text when the bytes are gone", content)
	}
}
