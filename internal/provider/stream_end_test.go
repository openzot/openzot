package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// newTestServer starts a stand-in provider and returns its base URL.
func newTestServer(t *testing.T, handler http.HandlerFunc) string {
	t.Helper()

	server := httptest.NewServer(handler)

	t.Cleanup(server.Close)

	return server.URL
}

// halfAnAnswer serves frames and then closes the body cleanly, with no terminal
// frame - what a proxy does when it drops a chunked response mid-generation.
func halfAnAnswer(t *testing.T, responses bool, lines ...string) *Client {
	t.Helper()

	return serveTransport(t, responses, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")

		for _, line := range lines {
			fmt.Fprintf(w, "data: %s\n\n", line)
		}
	})
}

// serveTransport points a client of the requested wire format at a handler.
func serveTransport(t *testing.T, responses bool, handler http.HandlerFunc) *Client {
	t.Helper()

	server := newTestServer(t, handler)

	client, err := New(Config{
		Provider:     Custom,
		Model:        "test-model",
		APIKey:       "test-key",
		BaseURL:      server,
		UseResponses: responses,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	return client
}

// collect drains a stream, returning the text and the terminal error, if any.
func collect(client *Client) (string, string, error) {
	var (
		text   string
		finish string
		failed error
	)

	for event := range client.Stream(context.Background(), Request{}) {
		text += event.Token

		if event.FinishReason != "" {
			finish = event.FinishReason
		}

		if event.Err != nil {
			failed = event.Err
		}
	}

	return text, finish, failed
}

// A proxy that closes a chunked response mid-generation produces a clean EOF
// with no [DONE] and no finish_reason. Reporting that as a finished turn hands
// the consumer half an answer and records it as the model's complete output, so
// it has to be an error - and a retriable one, because the next attempt usually
// works.
func TestChatRejectsAStreamThatEndedWithoutATerminalFrame(t *testing.T) {
	client := halfAnAnswer(t, false,
		`{"choices":[{"delta":{"content":"the answer is "}}]}`,
		`{"choices":[{"delta":{"content":"forty t"}}]}`,
	)

	_, finish, err := collect(client)

	if err == nil {
		t.Fatalf("a truncated stream must not read as a finished turn (finish = %q)", finish)
	}

	if !IsRetriable(err) {
		t.Errorf("a truncated stream should be retriable: %v", err)
	}
}

// The same rule on the Responses transport: it defaulted to "stop" whenever no
// status frame arrived, which turned the identical truncation into a silent
// success.
func TestResponsesRejectsAStreamThatEndedWithoutATerminalFrame(t *testing.T) {
	client := halfAnAnswer(t, true,
		`{"type":"response.output_text.delta","delta":"half an answ"}`,
	)

	_, finish, err := collect(client)

	if err == nil {
		t.Fatalf("a truncated stream must not read as a finished turn (finish = %q)", finish)
	}

	if !IsRetriable(err) {
		t.Errorf("a truncated stream should be retriable: %v", err)
	}
}

// A terminal status frame is enough on its own: the Responses API ends its
// stream with response.completed and need not send [DONE].
func TestResponsesAcceptsAStatusFrameAsTerminal(t *testing.T) {
	client := halfAnAnswer(t, true,
		`{"type":"response.output_text.delta","delta":"done"}`,
		`{"type":"response.completed","response":{"status":"completed"}}`,
	)

	text, finish, err := collect(client)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}

	if text != "done" || finish != FinishStop {
		t.Errorf("text = %q, finish = %q, want a clean stop", text, finish)
	}
}

// And on the chat transport a finish_reason is terminal even where the provider
// never sends [DONE].
func TestChatAcceptsAFinishReasonAsTerminal(t *testing.T) {
	client := halfAnAnswer(t, false,
		`{"choices":[{"delta":{"content":"done"}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
	)

	text, finish, err := collect(client)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}

	if text != "done" || finish != FinishStop {
		t.Errorf("text = %q, finish = %q, want a clean stop", text, finish)
	}
}
