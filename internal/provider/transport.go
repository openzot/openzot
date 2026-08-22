package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"time"
)

// Transport is one way of talking to a model.
//
// The engine only ever sees a Request and a stream of Events; how those become
// bytes on a wire is entirely a transport's business. That is the seam this
// interface exists to create.
//
// Today there are two, both speaking OpenAI-shaped HTTP: chat-completions and
// the Responses API. They are peers rather than one bolted onto the other,
// because the next ones will not be OpenAI-shaped at all - Anthropic's Messages
// API and Bedrock's Converse both differ in the request body, the streaming
// envelope and the tool-call representation. A transport that implements this
// interface needs no changes anywhere else to be usable.
type Transport interface {
	// Name identifies the wire format, for diagnostics and the UI header.
	Name() string

	// Stream runs one turn and delivers events until the turn ends.
	//
	// The returned channel is closed when the turn is over. A terminal error
	// arrives as an Event with Err set rather than out of band, so a caller
	// draining the channel cannot miss it.
	Stream(ctx context.Context, config Config, request Request) <-chan Event
}

// TransportFactory builds a transport for a resolved configuration.
type TransportFactory func(*http.Client) Transport

var (
	transportsMu sync.RWMutex
	transports   = map[string]TransportFactory{}
)

// RegisterTransport makes a transport available by name.
//
// Registration rather than a switch so a new wire format is one file plus one
// init, with nothing in the client to edit. A duplicate name is a programming
// error and panics at init rather than silently shadowing.
func RegisterTransport(name string, factory TransportFactory) {
	transportsMu.Lock()

	defer transportsMu.Unlock()

	if _, taken := transports[name]; taken {
		panic(fmt.Sprintf("provider: transport %q registered twice", name))
	}

	transports[name] = factory
}

// Transports lists the registered wire formats, sorted.
func Transports() []string {
	transportsMu.RLock()

	defer transportsMu.RUnlock()

	names := make([]string, 0, len(transports))

	for name := range transports {
		names = append(names, name)
	}

	sort.Strings(names)

	return names
}

// lookupTransport resolves a transport by name.
func lookupTransport(name string, httpClient *http.Client) (Transport, error) {
	transportsMu.RLock()

	factory, ok := transports[name]

	transportsMu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("provider: no transport named %q (have: %v)", name, Transports())
	}

	return factory(httpClient), nil
}

// The wire formats zot ships with.
const (
	// TransportChatCompletions is the OpenAI /chat/completions API, which every
	// provider zot supports speaks.
	TransportChatCompletions = "chat-completions"

	// TransportResponses is the OpenAI /responses API. It carries reasoning
	// state between turns, which chat-completions cannot.
	TransportResponses = "responses"
)

// errTruncatedStream is what a stream that merely stopped produces.
//
// A body reaching a clean EOF with no terminal frame - no [DONE], no
// finish_reason, no terminal status - is a turn that was cut off rather than one
// that ended, and a proxy closing a chunked response mid-generation looks exactly
// like this. It has to be an error: a consumer cannot tell the difference, so it
// would record half an answer as the model's complete turn. Retriable, because
// the next attempt usually completes.
var errTruncatedStream = &Error{
	Status:  0,
	Message: "the stream ended without a terminal frame, so the turn was cut off mid-answer",
}

// send delivers one event, or gives up if the turn has been cancelled.
//
// Every send in every transport goes through this. The event channel is
// unbuffered, so a consumer that stops draining mid-turn - which is exactly what
// the loop's runaway guard does - would park the producing goroutine on a bare
// channel send for the life of the process, holding the response body open with
// it. Checking ctx.Done() once per scan is not enough: the goroutine blocks
// between those checks, not at them.
//
// It reports whether the event was delivered, so a caller can stop early rather
// than keep parsing a stream nobody is reading.
//
// It is for mid-stream events only. The turn's last event goes through
// sendTerminal: a cancelled ctx is exactly when there is a terminal error to
// report, and this select would drop it.
func send(ctx context.Context, events chan<- Event, event Event) bool {
	select {
	case events <- event:
		return true
	case <-ctx.Done():
		return false
	}
}

// terminalSendGrace is how long a terminal event waits for a consumer that has
// stopped draining before it is dropped.
//
// Long enough that a consumer parked on the channel always wins the race; short
// enough that an abandoned turn releases its goroutine and response body
// promptly rather than never.
const terminalSendGrace = 250 * time.Millisecond

// sendTerminal delivers the turn's last word - usually the terminal error -
// even though the ctx that produced it is typically already cancelled.
//
// send is the wrong tool here: it selects against ctx.Done(), and with a
// cancelled ctx and a waiting receiver both ready, the select picks at random -
// so half of all cancelled turns would end with the channel closing silently
// and the consumer reading a partial answer as a complete, error-free turn. A
// consumer that is still draining (Complete, or any range-until-close loop)
// must receive the terminal event unconditionally; only one that walked away
// forfeits it, after a short grace, so the goroutine cannot park forever.
func sendTerminal(events chan<- Event, event Event) {
	timer := time.NewTimer(terminalSendGrace)

	defer timer.Stop()

	select {
	case events <- event:
	case <-timer.C:
	}
}

// httpPost builds a POST with the configuration's auth and headers applied.
//
// Shared by the transports so authentication is one implementation rather than
// one per wire format - a transport that forgot the header would fail in a way
// that looks like a bad key.
func httpPost(ctx context.Context, httpClient *http.Client, config Config, url string, body any) (*http.Response, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(encoded))
	if err != nil {
		return nil, err
	}

	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "text/event-stream")

	if config.APIKey != "" {
		request.Header.Set("Authorization", "Bearer "+config.APIKey)
	}

	for key, value := range config.Headers {
		request.Header.Set(key, value)
	}

	response, err := httpClient.Do(request)
	if err != nil {
		return nil, &Error{Status: 0, Message: err.Error(), cause: err}
	}

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		defer response.Body.Close()

		err := readError(response)

		// the refused request's size travels with the error: against a
		// suspected context ceiling it is the difference between a correlation
		// and a diagnosis
		var providerErr *Error
		if errors.As(err, &providerErr) {
			providerErr.RequestBytes = len(encoded)
			providerErr.RequestBody = clip(string(encoded), maxDumpBody)
		}

		return nil, err
	}

	return response, nil
}
