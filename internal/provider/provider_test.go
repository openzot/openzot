package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestResolveDefaultsAndValidation(t *testing.T) {
	tests := []struct {
		name    string
		config  Config
		wantErr bool
		wantURL string
	}{
		{
			name:    "a known provider fills in its endpoint",
			config:  Config{Provider: OpenAI, Model: "gpt-5.4", APIKey: "k"},
			wantURL: "https://api.openai.com/v1",
		},
		{
			name:    "no provider defaults to openai",
			config:  Config{Model: "gpt-5.4", APIKey: "k"},
			wantURL: "https://api.openai.com/v1",
		},
		{
			name:    "ollama needs no key",
			config:  Config{Provider: Ollama, Model: "llama-4"},
			wantURL: "http://localhost:11434/v1",
		},
		{
			name:    "a custom https endpoint is accepted",
			config:  Config{Provider: Custom, Model: "m", APIKey: "k", BaseURL: "https://gw.example.com/v1/"},
			wantURL: "https://gw.example.com/v1",
		},
		{
			name:    "a loopback endpoint may be plaintext",
			config:  Config{Provider: Custom, Model: "m", BaseURL: "http://127.0.0.1:8080/v1"},
			wantURL: "http://127.0.0.1:8080/v1",
		},
		{
			name:    "a plaintext remote endpoint is refused",
			config:  Config{Provider: Custom, Model: "m", APIKey: "k", BaseURL: "http://gw.example.com/v1"},
			wantErr: true,
		},
		{
			name:    "an unknown provider without a base URL is refused",
			config:  Config{Provider: "nope", Model: "m", APIKey: "k"},
			wantErr: true,
		},
		{
			name:    "a provider that needs a key is refused without one",
			config:  Config{Provider: OpenAI, Model: "m"},
			wantErr: true,
		},
		{
			name:    "no model is refused",
			config:  Config{Provider: OpenAI, APIKey: "k"},
			wantErr: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resolved, err := test.config.Resolve()

			if test.wantErr {
				if err == nil {
					t.Fatal("expected an error")
				}

				return
			}

			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}

			if resolved.BaseURL != test.wantURL {
				t.Errorf("base URL = %q, want %q", resolved.BaseURL, test.wantURL)
			}
		})
	}
}

func TestIsRetriableUsesStatusOverProse(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"a 500 retries", &Error{Status: 500, Message: "boom"}, true},
		{"a 503 retries", &Error{Status: 503, Message: "Service temporarily unavailable"}, true},
		{
			// the status is authoritative in both directions: a 400 does not
			// retry however transient its message sounds
			"a 400 does not retry even when it sounds transient",
			&Error{Status: 400, Message: "internal server error"},
			false,
		},
		{
			// a rate limit needs Retry-After backoff, not a tight loop
			"a 429 does not retry",
			&Error{Status: 429, Message: "rate limited"},
			false,
		},
		{
			// the wording that slipped a narrower regex in production
			"a status-less transient message retries",
			errors.New("Service temporarily unavailable"),
			true,
		},
		{"a status-less permanent message does not", errors.New("invalid model"), false},
		{"nil does not", nil, false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := IsRetriable(test.err); got != test.want {
				t.Errorf("IsRetriable = %v, want %v", got, test.want)
			}
		})
	}
}

// A stalled upstream is the one condition allowed to outrank the status,
// because gateways report it with whatever status they please - and at least
// one of them picks a 4xx, which the status rule would refuse forever.
//
// This is not hypothetical: an OpenRouter shift died to exactly the first case
// here, with no retry attempted, sixteen minutes of work lost to an upstream
// that had simply gone quiet.
func TestIsRetriableTreatsAStalledUpstreamAsTransient(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			"a gateway's idle timeout retries whatever status it wears",
			&Error{Status: 400, Message: "Upstream idle timeout exceeded"},
			true,
		},
		{"a 408 retries", &Error{Status: 408, Message: "Request Timeout"}, true},
		{"a status-less stream timeout retries", errors.New("read timeout"), true},
		{"a bare request timed out retries", errors.New("upstream connection timed out"), true},
		{
			// the reason the patterns name what went quiet rather than matching
			// "timeout": this one is a rejected parameter, and retrying it
			// burns the budget on a request that can never succeed
			"a rejected timeout parameter does not retry",
			&Error{Status: 400, Message: "timeout must be a positive integer"},
			false,
		},
		{
			"an auth failure does not retry however it is worded",
			&Error{Status: 401, Message: "invalid api key"},
			false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := IsRetriable(test.err); got != test.want {
				t.Errorf("IsRetriable = %v, want %v", got, test.want)
			}
		})
	}
}

func TestIsContextLimit(t *testing.T) {
	limit := &Error{Status: 400, Message: "This model's maximum context length is 128000 tokens"}

	if !IsContextLimit(limit) {
		t.Error("a context-length rejection must be recognised so the run can compact and retry")
	}

	if IsContextLimit(&Error{Status: 400, Message: "invalid api key"}) {
		t.Error("an unrelated 400 must not be treated as a context limit")
	}
}

func TestDecodeArgumentsToleratesEmpty(t *testing.T) {
	// a model calling a no-argument tool commonly sends "" rather than "{}"
	args, err := DecodeArguments(ToolCall{Function: FunctionCall{Name: "t", Arguments: "  "}})
	if err != nil {
		t.Fatalf("DecodeArguments: %v", err)
	}

	if len(args) != 0 {
		t.Errorf("args = %v, want empty", args)
	}

	if _, err := DecodeArguments(ToolCall{Function: FunctionCall{Name: "t", Arguments: "{nope"}}); err == nil {
		t.Error("malformed arguments must be reported")
	}
}

// Cases ported from the platform's own retry tests. Each one is a wording that
// actually reached production, so the value is in the specific strings rather
// than in the shape of the check.
func TestRetryClassificationPortedCases(t *testing.T) {
	retriable := []error{
		// the gateway 503 that hard-failed task runs: "temporarily" between the
		// two words defeated a narrower regex
		errors.New("Service temporarily unavailable. Please try again shortly. (503)"),
		errors.New("Internal server error (500)"),
		errors.New("Bad gateway (502)"),
		errors.New("Service unavailable (503)"),
		errors.New("Gateway timeout (504)"),
		&Error{Status: 500, Message: "boom"},

		// status-less, matched on prose
		errors.New("Provider returned error"),
		errors.New("Internal server error"),
		errors.New("The model is overloaded"),

		// case-insensitively
		errors.New("INTERNAL SERVER ERROR"),
		errors.New("provider RETURNED error"),
	}

	for _, err := range retriable {
		if !IsRetriable(err) {
			t.Errorf("should be retriable: %v", err)
		}
	}

	terminal := []error{
		errors.New("Invalid API key"),
		errors.New("Rate limit exceeded"),
		errors.New("Model not found"),
		errors.New("Unauthorized (401)"),
		nil,

		// the case that makes the status authoritative: a missing model is
		// terminal however transient the prose sounds
		errors.New("Publisher model gemini-3.1-flash-lite-preview was not found or your project does not have access to it. (404)"),
		&Error{Status: 400, Message: "the service is temporarily unavailable"},
	}

	for _, err := range terminal {
		if IsRetriable(err) {
			t.Errorf("should not be retriable: %v", err)
		}
	}
}

// A status embedded in the message is recovered when the error carries no status
// field, so prose can never overrule it.
func TestStatusIsRecoveredFromTheMessage(t *testing.T) {
	status, ok := statusOf(errors.New("something went wrong (503)"))

	if !ok || status != 503 {
		t.Errorf("statusOf = %d/%v, want 503/true", status, ok)
	}

	if _, ok := statusOf(errors.New("no status here")); ok {
		t.Error("a message with no status must not report one")
	}

	// a number that is not a status must not be read as one
	if _, ok := statusOf(errors.New("processed (999)")); ok {
		t.Error("999 is not an HTTP status")
	}
}

// Ported from the platform's token-limit detection: the provider states the real
// window, which is ground truth when the local catalogue was wrong.
func TestDetectContextLimitExtractsTheRealWindow(t *testing.T) {
	tests := []struct {
		message   string
		maxTokens int
		used      int
		suggested int
	}{
		{
			message:   "This model's maximum context length is 8192 tokens. However, your messages resulted in 8265 tokens (7873 in the messages, 392 in the functions). Please reduce the length of the messages or functions.",
			maxTokens: 8192, used: 8265, suggested: 6963,
		},
		{
			message:   "This model's maximum context length is 4096 tokens. However, your messages resulted in 4200 tokens.",
			maxTokens: 4096, used: 4200, suggested: 3481,
		},
		{
			// case-insensitively
			message:   "this model's Maximum Context Length is 16384 tokens. However, your messages resulted in 16500 tokens.",
			maxTokens: 16384, used: 16500, suggested: 13926,
		},
		{
			// irregular spacing and punctuation
			message:   "This model's maximum context length is 32768 tokens. However,  your messages resulted in  33000 tokens ( in messages and functions ).",
			maxTokens: 32768, used: 33000, suggested: 27852,
		},
	}

	for _, test := range tests {
		limit, ok := DetectContextLimit(errors.New(test.message))

		if !ok {
			t.Errorf("not detected: %.60q", test.message)

			continue
		}

		if limit.MaxTokens != test.maxTokens {
			t.Errorf("MaxTokens = %d, want %d", limit.MaxTokens, test.maxTokens)
		}

		if limit.UsedTokens != test.used {
			t.Errorf("UsedTokens = %d, want %d", limit.UsedTokens, test.used)
		}

		if limit.SuggestedLimit != test.suggested {
			t.Errorf("SuggestedLimit = %d, want %d", limit.SuggestedLimit, test.suggested)
		}
	}
}

func TestDetectContextLimitIgnoresUnrelatedErrors(t *testing.T) {
	for _, message := range []string{
		"Invalid API key provided",
		"Something about tokens but not a limit error",
		"",
	} {
		if _, ok := DetectContextLimit(errors.New(message)); ok {
			t.Errorf("should not be a context limit: %q", message)
		}
	}

	if _, ok := DetectContextLimit(nil); ok {
		t.Error("nil is not a context limit")
	}
}

// A rejection with no stated number is still recognised; the caller then falls
// back to its own estimate.
func TestDetectContextLimitWithoutANumber(t *testing.T) {
	limit, ok := DetectContextLimit(errors.New("prompt is too long"))

	if !ok {
		t.Fatal("should be detected as a context limit")
	}

	if limit.SuggestedLimit != 0 {
		t.Errorf("SuggestedLimit = %d, want 0 when the provider gave no number", limit.SuggestedLimit)
	}
}

func TestRateLimitIsNotRetriable(t *testing.T) {
	err := &Error{Status: 429, Message: "rate limit exceeded"}

	if IsRetriable(err) {
		t.Error("a 429 needs Retry-After backoff, not a tight retry loop")
	}

	if !IsRateLimited(err) {
		t.Error("a 429 must be identifiable as a rate limit")
	}
}

func TestAuthErrorsAreIdentified(t *testing.T) {
	for _, err := range []error{
		&Error{Status: 401, Message: "unauthorized"},
		&Error{Status: 403, Message: "forbidden"},
		&Error{Status: 400, Message: "Incorrect API key provided"},
	} {
		if !IsAuth(err) {
			t.Errorf("should be an auth failure: %v", err)
		}
	}

	if IsAuth(&Error{Status: 500, Message: "boom"}) {
		t.Error("a 500 is not an auth failure")
	}
}

func TestClientExposesItsConfig(t *testing.T) {
	client, err := New(Config{Provider: Groq, Model: "glm-5.2", APIKey: "k"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	config := client.Config()

	if config.Provider != Groq || config.Model != "glm-5.2" {
		t.Errorf("Config = %+v", config)
	}

	if config.BaseURL == "" {
		t.Error("the resolved endpoint must be visible")
	}
}

// The provider list is what an error message offers, so it must be complete and
// include the escape hatch.
func TestProvidersListsEveryProvider(t *testing.T) {
	providers := Providers()

	seen := map[string]bool{}

	for _, name := range providers {
		seen[name] = true
	}

	for _, want := range []string{OpenAI, Anthropic, Groq, Mistral, Ollama, Custom} {
		if !seen[want] {
			t.Errorf("%q should be listed", want)
		}
	}
}

// A wrapped transport failure must stay inspectable through errors.Is/As.
func TestErrorUnwrapsItsCause(t *testing.T) {
	cause := errors.New("dial tcp: connection refused")

	err := &Error{Status: 0, Message: cause.Error(), cause: cause}

	if !errors.Is(err, cause) {
		t.Error("the underlying cause must remain reachable")
	}

	if got := (&Error{Status: 0, Message: "boom"}).Error(); !strings.Contains(got, "boom") {
		t.Errorf("Error = %q", got)
	}

	if got := (&Error{Status: 503, Message: "boom"}).Error(); !strings.Contains(got, "503") {
		t.Errorf("a status must appear in the message: %q", got)
	}
}

// url.Parse("http://[::1]/v1") yields the host "[::1]", and splitting that on its
// last colon finds one inside the address rather than a port separator. The
// bracketed no-port form therefore matched nothing, so a local endpoint was
// refused as plaintext and lost the loopback waiver on the API key - while the
// same address with a port worked.
func TestLoopbackIsRecognisedInEveryForm(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		wantErr bool
	}{
		{name: "IPv6 loopback with a port", baseURL: "http://[::1]:11434/v1"},
		{name: "IPv6 loopback without a port", baseURL: "http://[::1]/v1"},
		{name: "IPv4 loopback with a port", baseURL: "http://127.0.0.1:8080/v1"},
		{name: "IPv4 loopback without a port", baseURL: "http://127.0.0.1/v1"},
		{name: "the whole 127/8 block is local", baseURL: "http://127.0.0.53:8080/v1"},
		{name: "localhost by name", baseURL: "http://localhost:1234/v1"},
		{
			name:    "a routable IPv6 address is not loopback",
			baseURL: "http://[2001:db8::1]:8080/v1",
			wantErr: true,
		},
		{
			name:    "a routable name is not loopback",
			baseURL: "http://gw.example.com/v1",
			wantErr: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// no key: a loopback endpoint carries the credential waiver too, so
			// both halves of the rule are exercised at once
			_, err := Config{Provider: Custom, Model: "m", BaseURL: test.baseURL}.Resolve()

			if test.wantErr {
				if err == nil {
					t.Fatalf("%s should be refused over plaintext", test.baseURL)
				}

				return
			}

			if err != nil {
				t.Fatalf("%s: %v", test.baseURL, err)
			}
		})
	}
}

// The credential rule for a custom endpoint has a reason worth stating, and a
// bare "no API key configured" sends the reader looking for a missing export
// rather than at the base_url they just added.
func TestACustomEndpointExplainsWhyItNeedsItsOwnKey(t *testing.T) {
	_, err := Config{
		Provider: OpenAI,
		Model:    "gpt-4o",
		BaseURL:  "https://proxy.example.com/v1",
	}.Resolve()

	if err == nil {
		t.Fatal("an overridden endpoint with no key must be refused")
	}

	if !errors.Is(err, ErrMissingCredential) {
		t.Errorf("error = %v, want it to classify as a missing credential", err)
	}

	if !strings.Contains(err.Error(), "proxy.example.com") {
		t.Errorf("error = %v, want it to name the endpoint that needs the key", err)
	}
}

// zot calls OpenRouter and the Vercel AI Gateway, both of which publish
// rankings of the apps calling them and both of which read the same two
// headers. Sending nothing meant zot was invisible on both.
func TestAttributionIsSentToRankingGatewaysOnly(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		want     bool
	}{
		{"openrouter ranks apps", OpenRouter, true},
		{"vercel ranks apps", Vercel, true},
		{
			// a first-party API reads neither header, so sending one only tells
			// the model provider which tool is calling
			"a first-party provider is told nothing",
			OpenAI,
			false,
		},
		{"a local provider is told nothing", Ollama, false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resolved, err := Config{Provider: test.provider, Model: "m", APIKey: "k"}.Resolve()
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}

			referer, title := resolved.Headers[headerReferer], resolved.Headers[headerTitle]

			if !test.want {
				if referer != "" || title != "" {
					t.Errorf("attribution sent to %s: %q / %q", test.provider, referer, title)
				}

				return
			}

			if referer != DefaultAttributionURL {
				t.Errorf("%s = %q, want %q", headerReferer, referer, DefaultAttributionURL)
			}

			if title != DefaultAttributionName {
				t.Errorf("%s = %q, want %q", headerTitle, title, DefaultAttributionName)
			}
		})
	}
}

// The default is a courtesy, not a policy: a tool built on zot attributes
// itself, and a user who wants to appear nowhere says so.
func TestAttributionIsConfigurable(t *testing.T) {
	custom, err := Config{
		Provider:    OpenRouter,
		Model:       "m",
		APIKey:      "k",
		Attribution: Attribution{Name: "acme-bot", URL: "https://acme.example"},
	}.Resolve()
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if got := custom.Headers[headerTitle]; got != "acme-bot" {
		t.Errorf("%s = %q, want the configured name", headerTitle, got)
	}

	if got := custom.Headers[headerReferer]; got != "https://acme.example" {
		t.Errorf("%s = %q, want the configured URL", headerReferer, got)
	}

	off, err := Config{
		Provider:    OpenRouter,
		Model:       "m",
		APIKey:      "k",
		Attribution: Attribution{Disabled: true},
	}.Resolve()
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if len(off.Headers) != 0 {
		t.Errorf("headers = %v, want nothing sent when attribution is disabled", off.Headers)
	}
}

// An explicit header is the caller having already said what they want, so a
// default that overrode it would be a bug rather than a courtesy. Matched
// case-insensitively because that is what an HTTP header is - otherwise a
// hand-written "http-referer" would be joined by zot's own, not replaced by it.
func TestAnExplicitHeaderBeatsAttribution(t *testing.T) {
	resolved, err := Config{
		Provider: OpenRouter,
		Model:    "m",
		APIKey:   "k",
		Headers:  map[string]string{"http-referer": "https://mine.example"},
	}.Resolve()
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if got := resolved.Headers["http-referer"]; got != "https://mine.example" {
		t.Errorf("http-referer = %q, want the caller's value kept", got)
	}

	if _, duplicated := resolved.Headers[headerReferer]; duplicated {
		t.Error("zot added its own referer alongside the caller's; one header, one value")
	}

	// the title was not set by the caller, so it still gets the default
	if got := resolved.Headers[headerTitle]; got != DefaultAttributionName {
		t.Errorf("%s = %q, want the default to still apply", headerTitle, got)
	}
}

// Resolving must not write through to the caller's map: a Config is a value,
// and a caller that resolved twice would otherwise accumulate zot's headers in
// the map it passed in.
func TestResolveDoesNotMutateTheCallersHeaders(t *testing.T) {
	original := map[string]string{"X-Route": "eu"}

	if _, err := (Config{
		Provider: OpenRouter,
		Model:    "m",
		APIKey:   "k",
		Headers:  original,
	}).Resolve(); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if len(original) != 1 {
		t.Errorf("the caller's header map grew to %v", original)
	}
}

// Resolving the headers is not the same as sending them. This drives a real
// request at a local server and reads what arrived - which is also the only way
// to see what Go's header canonicalisation does to "HTTP-Referer" on the wire.
func TestAttributionReachesTheWire(t *testing.T) {
	var got http.Header

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()

		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))

	t.Cleanup(server.Close)

	// Custom with an openrouter base URL would not attribute - the provider
	// identifier is what selects it - so name the provider and point it here.
	client, err := New(Config{
		Provider: OpenRouter,
		Model:    "m",
		APIKey:   "k",
		BaseURL:  server.URL,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	//nolint:errcheck // the stub answers with an empty stream; the headers are the point
	_, _, _, _ = client.Complete(context.Background(), Request{
		Messages: []ChatMessage{{Role: "user", Content: "hi"}},
	})

	if got == nil {
		t.Fatal("the server saw no request")
	}

	// http.Header.Get is case-insensitive, which is what an HTTP header is and
	// what both gateways match on - the canonical form Go writes ("Http-Referer")
	// is the same header as the "HTTP-Referer" their docs spell.
	if referer := got.Get(headerReferer); referer != DefaultAttributionURL {
		t.Errorf("%s = %q, want %q", headerReferer, referer, DefaultAttributionURL)
	}

	if title := got.Get(headerTitle); title != DefaultAttributionName {
		t.Errorf("%s = %q, want %q", headerTitle, title, DefaultAttributionName)
	}
}

// A failure the provider wrote into a stream it had already begun answering on
// is retriable on structure, not on wording. The wording is the point: this
// exact message - OpenRouter's, not zot's - ended an unattended shift with no
// retry attempted, because it matched no pattern and carried no status.
func TestAMidStreamFailureIsRetriableWhateverItSays(t *testing.T) {
	for _, message := range []string{
		"JSON error injected into SSE stream",
		"the provider reported a failure",
		"something no pattern here has ever seen",
	} {
		if !IsRetriable(streamFailure(message)) {
			t.Errorf("a mid-stream failure saying %q was treated as permanent", message)
		}
	}

	// the same prose arriving as a plain refusal is *not* retriable: no stream
	// was ever opened, so the request itself is what the provider objected to
	if IsRetriable(&Error{Status: 400, Message: "JSON error injected into SSE stream"}) {
		t.Error("a 400 must stay permanent; only an error inside an open stream is structural")
	}
}

// The transports are where the classification is actually applied, so assert it
// there rather than only on the constructor - a transport that built a bare
// Error would compile and silently lose every retry.
func TestTheChatTransportMarksAMidStreamError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		// a stream that starts normally and then carries an error object
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"error\":{\"message\":\"JSON error injected into SSE stream\"}}\n\n")
	}))

	t.Cleanup(server.Close)

	client, err := New(Config{Provider: Custom, Model: "m", APIKey: "k", BaseURL: server.URL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_, _, _, err = client.Complete(context.Background(), Request{
		Messages: []ChatMessage{{Role: "user", Content: "go"}},
	})

	if err == nil {
		t.Fatal("an error object in the stream must fail the turn")
	}

	if !IsRetriable(err) {
		t.Errorf("the turn failed with %v, which the loop will not retry", err)
	}
}
