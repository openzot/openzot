package provider

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
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

// The bounds on a turn.
//
// None of them is a wall-clock cap on the exchange, and that is deliberate: an
// http.Client.Timeout covers the body read as well, so it kills a stream that is
// actively producing tokens - and it does so with "Client.Timeout exceeded while
// reading body", which no retry classifier recognises, ending the run. A
// reasoning model can think for minutes before its first token and stream for
// many more after it; a turn that is still producing is working, not hung.
//
// What is bounded instead is silence: how long to wait for a connection, for the
// response head, and - see stallReader - between two reads of the body.
const (
	dialTimeout           = 30 * time.Second
	responseHeaderTimeout = 10 * time.Minute
)

// streamStallTimeout is how long a stream may say nothing at all before it is
// treated as hung.
//
// As generous as the whole-request cap it replaces, deliberately: this is the
// same backstop, applied to the thing that actually indicates a hang. A model
// that has been silent for ten minutes mid-stream is not thinking - keep-alive
// comments, in-progress frames and reasoning deltas all count as progress.
//
// A variable rather than a constant only so tests can drive it without waiting
// minutes; it is not a configuration knob.
var streamStallTimeout = 10 * time.Minute

// newHTTPClient builds the shared transport, bounding the phases that can hang
// without bounding the one that legitimately takes a long time.
func newHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()

	transport.DialContext = (&net.Dialer{Timeout: dialTimeout, KeepAlive: 30 * time.Second}).DialContext
	transport.TLSHandshakeTimeout = dialTimeout
	transport.ResponseHeaderTimeout = responseHeaderTimeout

	return &http.Client{Transport: transport}
}

// stallReader fails a stream that has gone silent, without bounding one that is
// still producing.
//
// The deadline moves forward on every read that returns data, so a turn is only
// ever cut off once it stops saying anything at all. Firing closes the body,
// which unblocks the read the consumer is parked in; the error that surfaces is
// replaced with one naming the stall, because "use of closed network connection"
// is neither true nor recognisable as transient.
type stallReader struct {
	inner   io.ReadCloser
	timeout time.Duration
	timer   *time.Timer

	mu      sync.Mutex
	stalled bool
}

func newStallReader(inner io.ReadCloser, timeout time.Duration) *stallReader {
	reader := &stallReader{inner: inner, timeout: timeout}

	reader.timer = time.AfterFunc(timeout, reader.fire)

	return reader
}

func (r *stallReader) Read(p []byte) (int, error) {
	n, err := r.inner.Read(p)

	if n > 0 {
		r.timer.Reset(r.timeout)
	}

	if err != nil && r.didStall() {
		return n, &Error{Status: 0, Message: fmt.Sprintf(
			"the stream stalled: nothing arrived for %s", r.timeout)}
	}

	return n, err
}

// stop releases the watchdog once the turn is over.
func (r *stallReader) stop() {
	r.timer.Stop()
}

func (r *stallReader) fire() {
	r.mu.Lock()

	r.stalled = true

	r.mu.Unlock()

	_ = r.inner.Close()
}

func (r *stallReader) didStall() bool {
	r.mu.Lock()

	defer r.mu.Unlock()

	return r.stalled
}

// New creates a client, validating the configuration and resolving its
// transport.
func New(config Config) (*Client, error) {
	resolved, err := config.Resolve()
	if err != nil {
		return nil, err
	}

	httpClient := newHTTPClient()

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
		items     []ReasoningItem
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

		// the opaque state travels with the message, because that is what the
		// next request has to replay it alongside
		if len(event.ReasoningItems) > 0 {
			items = event.ReasoningItems
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
		ReasoningItems:   items,
	}

	return message, finish, usage, nil
}
