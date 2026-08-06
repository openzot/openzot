package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openzot/openzot/internal/catalogue"
)

// Gateways route by a provider-qualified model name - "openai/gpt-4o",
// "z-ai/glm-5.2" - so the prefix is not decoration, it is the routing. These
// tests pin the things that have to be true for a prefixed name to work: it
// reaches the wire unchanged, it still resolves its real context window, and it
// uses chat-completions, the one wire format these gateways speak. OpenRouter is
// the worked example; Vercel and Cloudflare route the same way.

// captureModel serves one successful turn and records the "model" field of the
// request body the client sent.
func captureModel(t *testing.T, config Config) string {
	t.Helper()

	seen := make(chan string, 1)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)

		var payload struct {
			Model string `json:"model"`
		}

		_ = json.Unmarshal(body, &payload)

		select {
		case seen <- payload.Model:
		default:
		}

		w.Header().Set("Content-Type", "text/event-stream")

		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\n"))
		w.Write([]byte("data: [DONE]\n\n"))
	}))

	t.Cleanup(server.Close)

	config.BaseURL = server.URL

	client, err := New(config)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	for event := range client.Stream(context.Background(), Request{}) {
		if event.Err != nil {
			t.Fatalf("stream: %v", event.Err)
		}
	}

	select {
	case model := <-seen:
		return model
	default:
		t.Fatal("the provider was never called")

		return ""
	}
}

// The prefix is the routing, so it has to arrive intact. Stripping it would send
// OpenRouter a model it cannot resolve.
func TestOpenRouterSendsThePrefixedModelVerbatim(t *testing.T) {
	for _, model := range []string{
		"openai/gpt-4o",
		"anthropic/claude-5-sonnet",
		"z-ai/glm-5.2",
		"meta-llama/llama-4-70b-instruct",
	} {
		t.Run(model, func(t *testing.T) {
			got := captureModel(t, Config{
				Provider: OpenRouter,
				Model:    model,
				APIKey:   "sk-test",
			})

			if got != model {
				t.Errorf("provider received model %q, want %q sent unchanged", got, model)
			}
		})
	}
}

// The prefixed name still has to resolve its real limits. A budget decision made
// against the conservative default window - because the lookup could not see
// past the prefix - would compact far too early on a large-context model.
func TestOpenRouterResolvesTheRealContextWindow(t *testing.T) {
	tests := []struct {
		model      string
		wantWindow int
	}{
		{model: "z-ai/glm-5.2", wantWindow: 1_000_000},
		{model: "openai/gpt-5.4-mini", wantWindow: 400_000},
		{model: "openai/gpt-4o", wantWindow: 128_000},
	}

	for _, test := range tests {
		t.Run(test.model, func(t *testing.T) {
			config, err := Config{
				Provider: OpenRouter,
				Model:    test.model,
				APIKey:   "sk-test",
			}.Resolve()
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}

			if got := catalogue.InputBudget(config.Model); got <= 0 {
				t.Fatalf("input budget for %q is %d", test.model, got)
			}

			// the budget is a fraction of the window, so a prefixed name that
			// fell through to the default (128k) window would be capped far
			// below a large-context model's real budget
			if test.wantWindow > 200_000 && catalogue.InputBudget(config.Model) <= 128_000 {
				t.Errorf("%q resolved to a small budget %d - the prefix was not stripped for the lookup",
					test.model, catalogue.InputBudget(config.Model))
			}
		})
	}
}

// OpenRouter speaks chat-completions, not OpenAI's Responses API. The transport
// is chosen by provider, so even a reasoning model behind an "openai/" prefix
// must stay on chat-completions - routing it to /responses would 404.
func TestOpenRouterAlwaysUsesChatCompletions(t *testing.T) {
	for _, model := range []string{
		"openai/gpt-5.4-mini", // a reasoning model - the tempting case to get wrong
		"openai/gpt-4o",
		"z-ai/glm-5.2",
	} {
		t.Run(model, func(t *testing.T) {
			client, err := New(Config{
				Provider: OpenRouter,
				Model:    model,
				APIKey:   "sk-test",
			})
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			if got := client.Transport(); got != TransportChatCompletions {
				t.Errorf("transport = %q, want %q", got, TransportChatCompletions)
			}
		})
	}
}

// The same reasoning model on the OpenAI backend directly does use the Responses
// API - this is the contrast that proves the choice is keyed on the provider,
// not on the model name.
func TestTheSameModelUsesResponsesOnOpenAIDirectly(t *testing.T) {
	client, err := New(Config{
		Provider: OpenAI,
		Model:    "gpt-5.4-mini",
		APIKey:   "sk-test",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if got := client.Transport(); got != TransportResponses {
		t.Errorf("transport = %q, want %q on OpenAI directly", got, TransportResponses)
	}
}

// The openrouter backend resolves to OpenRouter's endpoint with no base URL
// configured - which is what makes `--backend openrouter` work on its own.
func TestOpenRouterHasABuiltinEndpoint(t *testing.T) {
	config, err := Config{
		Provider: OpenRouter,
		Model:    "openai/gpt-4o",
		APIKey:   "sk-test",
	}.Resolve()
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if config.BaseURL != "https://openrouter.ai/api/v1" {
		t.Errorf("base URL = %q, want OpenRouter's", config.BaseURL)
	}
}

// Vercel's AI Gateway is a fixed-endpoint gateway that routes by the same
// provider-qualified model names, so the prefix handling has to hold for it too.
func TestVercelIsAFixedEndpointGateway(t *testing.T) {
	config, err := Config{Provider: Vercel, Model: "openai/gpt-4o", APIKey: "sk-test"}.Resolve()
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if config.BaseURL != "https://ai-gateway.vercel.sh/v1" {
		t.Errorf("base URL = %q, want Vercel's fixed endpoint", config.BaseURL)
	}

	// the prefixed name reaches the wire unchanged, exactly as on OpenRouter
	if got := captureModel(t, Config{Provider: Vercel, Model: "anthropic/claude-5-sonnet", APIKey: "sk-test"}); got != "anthropic/claude-5-sonnet" {
		t.Errorf("Vercel received model %q, want it verbatim", got)
	}

	// and it is a gateway, so chat-completions
	client, _ := New(Config{Provider: Vercel, Model: "openai/gpt-5.4-mini", APIKey: "sk-test"})
	if got := client.Transport(); got != TransportChatCompletions {
		t.Errorf("transport = %q, want chat-completions", got)
	}
}

// Cloudflare's AI Gateway has no fixed endpoint - its URL carries the account
// and gateway ids - so a run must supply base_url. It is recognised, listed, and
// gives an actionable error rather than a bare "unknown provider".
func TestCloudflareRequiresABaseURL(t *testing.T) {
	_, err := Config{Provider: Cloudflare, Model: "openai/gpt-4o", APIKey: "sk-test"}.Resolve()
	if err == nil {
		t.Fatal("Cloudflare with no base URL must be rejected")
	}

	// the error has to say what to do, not just that something is wrong
	for _, want := range []string{"base URL", "account", "gateway.ai.cloudflare.com"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}

	// with a base URL it resolves and routes like any other gateway
	config, err := Config{
		Provider: Cloudflare,
		Model:    "openai/gpt-4o",
		APIKey:   "sk-test",
		BaseURL:  "https://gateway.ai.cloudflare.com/v1/acct/gw/compat",
	}.Resolve()
	if err != nil {
		t.Fatalf("Resolve with base URL: %v", err)
	}

	if config.BaseURL != "https://gateway.ai.cloudflare.com/v1/acct/gw/compat" {
		t.Errorf("base URL = %q", config.BaseURL)
	}
}

// Both gateways have to be in the provider list, or the error that offers
// alternatives would omit them.
func TestGatewaysAreListed(t *testing.T) {
	listed := map[string]bool{}

	for _, name := range Providers() {
		listed[name] = true
	}

	for _, want := range []string{OpenRouter, Vercel, Cloudflare} {
		if !listed[want] {
			t.Errorf("%q is not in the provider list", want)
		}
	}
}

// The point of auto-qualification: a model is the same model whichever gateway
// serves it, so the user says "glm-5.2" and zot supplies each gateway's own
// prefix. This is the case the user cannot be expected to know - that OpenRouter
// calls it "z-ai" and Vercel calls it "zai".
func TestBareModelIsQualifiedPerGateway(t *testing.T) {
	tests := []struct {
		provider string
		model    string
		want     string
	}{
		// the headline case: same bare name, gateway-specific prefix
		{provider: OpenRouter, model: "glm-5.2", want: "z-ai/glm-5.2"},
		{provider: Vercel, model: "glm-5.2", want: "zai/glm-5.2"},

		// a provider whose slug matches our name needs no alias
		{provider: OpenRouter, model: "gpt-4o", want: "openai/gpt-4o"},
		{provider: Vercel, model: "gpt-5.4-mini", want: "openai/gpt-5.4-mini"},

		// the other known OpenRouter slug mismatches
		{provider: OpenRouter, model: "llama-4-scout", want: "meta-llama/llama-4-scout"},
	}

	for _, test := range tests {
		t.Run(test.provider+"/"+test.model, func(t *testing.T) {
			config, err := Config{
				Provider: test.provider,
				Model:    test.model,
				APIKey:   "sk-test",
				// Cloudflare needs a base URL; the others have fixed endpoints
				BaseURL: gatewayBaseURL(test.provider),
			}.Resolve()
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}

			if config.Model != test.want {
				t.Errorf("qualified %q on %s to %q, want %q", test.model, test.provider, config.Model, test.want)
			}
		})
	}
}

// gatewayBaseURL supplies Cloudflare's required URL and leaves the fixed-endpoint
// gateways to resolve their own.
func gatewayBaseURL(provider string) string {
	if provider == Cloudflare {
		return "https://gateway.ai.cloudflare.com/v1/acct/gw/compat"
	}

	return ""
}

// A name the user already qualified is theirs - never second-guessed. Someone
// who typed "z-ai/glm-5.2" knows their gateway better than the catalogue does.
func TestAnAlreadyQualifiedModelIsUntouched(t *testing.T) {
	for _, model := range []string{"z-ai/glm-5.2", "openai/gpt-4o", "some-vendor/some-model"} {
		config, err := Config{Provider: OpenRouter, Model: model, APIKey: "sk-test"}.Resolve()
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}

		if config.Model != model {
			t.Errorf("a qualified name was rewritten: %q -> %q", model, config.Model)
		}
	}
}

// Auto-qualification is a gateway convenience only. A direct provider or a custom
// backend must send exactly what the user typed - prefixing there would corrupt
// a name the provider expects bare.
func TestDirectProvidersAreNotQualified(t *testing.T) {
	for _, provider := range []string{OpenAI, Anthropic, ZAI, Custom} {
		config := Config{Provider: provider, Model: "glm-5.2", APIKey: "sk-test"}

		if provider == Custom {
			config.BaseURL = "https://example.com/v1"
		}

		resolved, err := config.Resolve()
		if err != nil {
			t.Fatalf("Resolve on %s: %v", provider, err)
		}

		if resolved.Model != "glm-5.2" {
			t.Errorf("%s rewrote a bare model to %q - only gateways qualify", provider, resolved.Model)
		}
	}
}

// A model the catalogue does not know cannot be qualified - there is nothing to
// prefix it with. It passes through and fails cleanly at the gateway, which is
// strictly better than inventing a prefix that misroutes.
func TestAnUnknownModelPassesThroughUnqualified(t *testing.T) {
	config, err := Config{
		Provider: OpenRouter,
		Model:    "brand-new-model-we-have-not-catalogued",
		APIKey:   "sk-test",
	}.Resolve()
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if config.Model != "brand-new-model-we-have-not-catalogued" {
		t.Errorf("an unknown model was given an invented prefix: %q", config.Model)
	}
}

// End to end: the qualified name is what actually reaches the gateway on the
// wire, not just what Resolve computed.
func TestTheQualifiedNameIsWhatIsSent(t *testing.T) {
	got := captureModel(t, Config{Provider: OpenRouter, Model: "glm-5.2", APIKey: "sk-test"})

	if got != "z-ai/glm-5.2" {
		t.Errorf("gateway received %q, want the qualified z-ai/glm-5.2", got)
	}
}
