package provider

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// The OpenAI chat-completions wire format.
//
// Every provider zot supports speaks it, which is why it is the default. Its one
// structural weakness is that it has nowhere to carry a model's reasoning state
// between turns - see transport_responses.go.

func init() {
	RegisterTransport(TransportChatCompletions, func(httpClient *http.Client) Transport {
		return &chatTransport{http: httpClient}
	})
}

type chatTransport struct {
	http *http.Client
}

func (t *chatTransport) Name() string { return TransportChatCompletions }

func (t *chatTransport) Stream(ctx context.Context, config Config, request Request) <-chan Event {
	events := make(chan Event)

	go func() {
		defer close(events)

		body := wireRequest{
			Model:         config.Model,
			Messages:      request.Messages,
			Tools:         request.Tools,
			Stream:        true,
			MaxTokens:     request.MaxTokens,
			Stop:          request.Stop,
			StreamOptions: &streamOptions{IncludeUsage: true},
		}

		response, err := httpPost(ctx, t.http, config, config.completionsURL(), body)
		if err != nil {
			events <- Event{Err: err}

			return
		}

		defer response.Body.Close()

		if err := consumeSSE(ctx, response.Body, events); err != nil {
			events <- Event{Err: err}
		}
	}()

	return events
}

// wireRequest is the JSON body sent to the provider.
type wireRequest struct {
	Model     string        `json:"model"`
	Messages  []ChatMessage `json:"messages"`
	Tools     []Tool        `json:"tools,omitempty"`
	Stream    bool          `json:"stream"`
	MaxTokens *int          `json:"max_tokens,omitempty"`
	Stop      []string      `json:"stop,omitempty"`

	// StreamOptions asks for usage on the final chunk. Providers that do not
	// understand it ignore it.
	StreamOptions *streamOptions `json:"stream_options,omitempty"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

// buildRequest encodes a body and applies auth and headers.

// Stream runs a completion and delivers events as they arrive.
//
// The returned channel is closed when the turn ends. A terminal error arrives as
// an Event with Err set rather than out of band, so a caller draining the
// channel cannot miss it.

// chunk is one SSE payload from the provider.
type chunk struct {
	Choices []struct {
		Delta struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
			Reasoning        string `json:"reasoning"`

			ToolCalls []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`

		FinishReason string `json:"finish_reason"`
	} `json:"choices"`

	Usage *Usage `json:"usage"`

	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    any    `json:"code"`
	} `json:"error"`
}

// consumeSSE parses the event stream and assembles tool calls.
//
// Tool calls arrive fragmented and interleaved: the name in one chunk, the
// arguments split across many more, identified only by an index. They are
// accumulated by that index and emitted once, complete, at the end of the turn -
// a caller must never see a half-built call.
func consumeSSE(ctx context.Context, body io.Reader, events chan<- Event) error {
	scanner := bufio.NewScanner(body)

	// @note a single SSE line can carry a large tool-call argument fragment, so
	// the default 64KiB limit is not enough
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

	assembling := map[int]*ToolCall{}

	var order []int

	finishReason := ""

	var usage *Usage

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		line := strings.TrimSpace(scanner.Text())

		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}

		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))

		if payload == "[DONE]" {
			break
		}

		var parsed chunk

		if err := json.Unmarshal([]byte(payload), &parsed); err != nil {
			// @note a malformed frame is skipped rather than fatal: some
			// gateways interleave keep-alive comments and non-JSON notices
			continue
		}

		if parsed.Error != nil {
			return &Error{Status: 0, Message: parsed.Error.Message}
		}

		if parsed.Usage != nil {
			usage = parsed.Usage
		}

		for _, choice := range parsed.Choices {
			if choice.FinishReason != "" {
				finishReason = choice.FinishReason
			}

			if choice.Delta.Content != "" {
				events <- Event{Token: choice.Delta.Content}
			}

			// providers disagree on the reasoning field name
			if reasoning := choice.Delta.ReasoningContent; reasoning != "" {
				events <- Event{ReasoningToken: reasoning}
			} else if reasoning := choice.Delta.Reasoning; reasoning != "" {
				events <- Event{ReasoningToken: reasoning}
			}

			for _, delta := range choice.Delta.ToolCalls {
				call, seen := assembling[delta.Index]

				if !seen {
					call = &ToolCall{Type: "function"}

					assembling[delta.Index] = call

					order = append(order, delta.Index)
				}

				if delta.ID != "" {
					call.ID = delta.ID
				}

				if delta.Type != "" {
					call.Type = delta.Type
				}

				if delta.Function.Name != "" {
					call.Function.Name = delta.Function.Name
				}

				call.Function.Arguments += delta.Function.Arguments
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return err
	}

	final := Event{FinishReason: finishReason, Usage: usage}

	for _, index := range order {
		final.ToolCalls = append(final.ToolCalls, *assembling[index])
	}

	// @note some providers omit finish_reason when the turn ends in tool calls;
	// infer it so the loop does not read the turn as a plain stop
	if final.FinishReason == "" && len(final.ToolCalls) > 0 {
		final.FinishReason = FinishToolCalls
	}

	events <- final

	return nil
}

// readError turns a non-2xx response into a classified error.
func readError(response *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(response.Body, 64*1024))

	message := strings.TrimSpace(string(body))

	var parsed struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`

		Message string `json:"message"`
	}

	if err := json.Unmarshal(body, &parsed); err == nil {
		switch {
		case parsed.Error.Message != "":
			message = parsed.Error.Message
		case parsed.Message != "":
			message = parsed.Message
		}
	}

	if message == "" {
		message = http.StatusText(response.StatusCode)
	}

	return &Error{Status: response.StatusCode, Message: message}
}

// Complete runs a non-streaming completion, collecting the stream.

// DecodeArguments parses a tool call's JSON arguments.
//
// An empty string decodes to an empty object: a model calling a no-argument tool
// often sends "" rather than "{}", and rejecting that would fail the call for no
// reason.
func DecodeArguments(call ToolCall) (map[string]any, error) {
	raw := strings.TrimSpace(call.Function.Arguments)

	if raw == "" {
		return map[string]any{}, nil
	}

	var arguments map[string]any

	if err := json.Unmarshal([]byte(raw), &arguments); err != nil {
		return nil, fmt.Errorf("tool %q: arguments are not valid JSON: %w", call.Function.Name, err)
	}

	return arguments, nil
}
