package provider

import (
	"context"
	"net/http"
	"time"
)

// Client is a configured connection to a model.
//
// It owns the configuration and the HTTP client, and delegates the actual
// conversation to a Transport. Choosing the transport is the only decision it
// makes - everything about how a turn is encoded belongs to the transport, which
// is what keeps adding a non-OpenAI provider from touching anything here.
type Client struct {
	config    Config
	http      *http.Client
	transport Transport
}

// requestTimeout bounds a single turn.
//
// Generous, because a reasoning model can think for minutes before emitting its
// first token. The context the caller passes is the real cancellation path; this
// is only a backstop against a connection that hangs forever.
const requestTimeout = 10 * time.Minute

// New creates a client, validating the configuration and resolving its
// transport.
func New(config Config) (*Client, error) {
	resolved, err := config.Resolve()
	if err != nil {
		return nil, err
	}

	httpClient := &http.Client{Timeout: requestTimeout}

	name := TransportChatCompletions

	if resolved.UseResponses {
		name = TransportResponses
	}

	transport, err := lookupTransport(name, httpClient)
	if err != nil {
		return nil, err
	}

	return &Client{config: resolved, http: httpClient, transport: transport}, nil
}

// Config returns the resolved configuration.
func (c *Client) Config() Config {
	return c.config
}

// Transport names the wire format in use, for diagnostics and the UI header.
func (c *Client) Transport() string {
	return c.transport.Name()
}

// Stream runs one turn.
func (c *Client) Stream(ctx context.Context, request Request) <-chan Event {
	return c.transport.Stream(ctx, c.config, request)
}

// Complete runs a turn and collects the whole stream, for callers that do not
// want to render it as it arrives.
func (c *Client) Complete(ctx context.Context, request Request) (ChatMessage, string, *Usage, error) {
	var (
		text      textBuilder
		reasoning textBuilder
		calls     []ToolCall
		finish    string
		usage     *Usage
	)

	for event := range c.Stream(ctx, request) {
		if event.Err != nil {
			return ChatMessage{}, "", nil, event.Err
		}

		text.WriteString(event.Token)
		reasoning.WriteString(event.ReasoningToken)

		if event.FinishReason != "" {
			finish = event.FinishReason
		}

		if len(event.ToolCalls) > 0 {
			calls = event.ToolCalls
		}

		if event.Usage != nil {
			usage = event.Usage
		}
	}

	message := ChatMessage{
		Role:             RoleAssistant,
		Content:          text.String(),
		ReasoningContent: reasoning.String(),
		ToolCalls:        calls,
	}

	return message, finish, usage, nil
}
