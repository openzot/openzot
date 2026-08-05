// Package provider talks to OpenAI-compatible chat-completions endpoints.
//
// Every provider zot supports speaks the same wire format, so this is one client
// plus per-provider configuration rather than a dozen integrations. What differs
// between them is the base URL, the auth header and a handful of quirks - not the
// protocol.
package provider

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/openzot/openzot/internal/catalogue"
)

// Known provider identifiers.
const (
	OpenAI     = "openai"
	Anthropic  = "anthropic"
	Groq       = "groq"
	Mistral    = "mistral"
	DeepSeek   = "deepseek"
	OpenRouter = "openrouter"
	Perplexity = "perplexity"
	Together   = "together"
	Cerebras   = "cerebras"
	XAI        = "xai"
	Moonshot   = "moonshot"
	ZAI        = "zai"
	Qwen       = "qwen"
	Ollama     = "ollama"
	Custom     = "custom"
)

// baseURLs maps a provider to its OpenAI-compatible endpoint root.
//
// Anything not listed here can still be reached: name the provider `custom` and
// give it a base URL. The list is a convenience, not a boundary.
var baseURLs = map[string]string{
	OpenAI:     "https://api.openai.com/v1",
	Anthropic:  "https://api.anthropic.com/v1",
	Groq:       "https://api.groq.com/openai/v1",
	Mistral:    "https://api.mistral.ai/v1",
	DeepSeek:   "https://api.deepseek.com/v1",
	OpenRouter: "https://openrouter.ai/api/v1",
	Perplexity: "https://api.perplexity.ai",
	Together:   "https://api.together.xyz/v1",
	Cerebras:   "https://api.cerebras.ai/v1",
	XAI:        "https://api.x.ai/v1",
	Moonshot:   "https://api.moonshot.cn/v1",
	ZAI:        "https://api.z.ai/api/paas/v4",
	Qwen:       "https://dashscope.aliyuncs.com/compatible-mode/v1",
	Ollama:     "http://localhost:11434/v1",
}

// Providers lists the known provider identifiers.
func Providers() []string {
	names := make([]string, 0, len(baseURLs)+1)

	for name := range baseURLs {
		names = append(names, name)
	}

	names = append(names, Custom)

	return names
}

// Config identifies which endpoint to call and with what credential.
type Config struct {
	// Provider is one of the identifiers above. Custom requires BaseURL.
	Provider string

	// Model is the provider's own model name.
	Model string

	// APIKey authenticates the request. Required for every provider except
	// Ollama, which is local.
	APIKey string

	// BaseURL overrides the provider's default endpoint. Required for Custom.
	BaseURL string

	// Headers are merged into every request, for gateways that need extra
	// routing or attribution headers.
	Headers map[string]string

	// UseResponses selects the OpenAI Responses API instead of
	// chat-completions.
	//
	// It matters for reasoning models: Responses carries reasoning state between
	// turns as an opaque item the model resumes from, whereas chat-completions
	// has nowhere to put it and the model re-derives its thinking on every tool
	// round. Resolve turns this on automatically for providers and models known
	// to support it; set it explicitly to override.
	UseResponses bool

	// DisableResponses forces chat-completions even where Responses would be
	// selected automatically - an escape hatch for a gateway that advertises
	// OpenAI compatibility but implements only the older endpoint.
	DisableResponses bool
}

// ErrMissingCredential is returned when a provider that needs a key has none.
var ErrMissingCredential = errors.New("provider: no API key configured")

// Resolve validates the configuration and fills in the endpoint.
//
// A custom base URL requires an explicit key rather than inheriting one. The
// rule exists because a credential is scoped to the host it was issued for:
// silently forwarding a key to a URL the user just typed is how a provider
// credential ends up in someone else's logs.
func (c Config) Resolve() (Config, error) {
	resolved := c

	resolved.Provider = strings.ToLower(strings.TrimSpace(c.Provider))

	if resolved.Provider == "" {
		resolved.Provider = OpenAI
	}

	if resolved.Model == "" {
		return Config{}, errors.New("provider: no model specified")
	}

	base, known := baseURLs[resolved.Provider]

	switch {
	case resolved.BaseURL != "":
		parsed, err := url.Parse(resolved.BaseURL)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return Config{}, fmt.Errorf("provider: invalid base URL %q", resolved.BaseURL)
		}

		if parsed.Scheme != "https" && !isLoopback(parsed.Host) {
			return Config{}, fmt.Errorf("provider: base URL must use https (got %q)", resolved.BaseURL)
		}

		resolved.BaseURL = strings.TrimRight(resolved.BaseURL, "/")

	case known:
		resolved.BaseURL = base

	default:
		return Config{}, fmt.Errorf("provider: unknown provider %q and no base URL given", resolved.Provider)
	}

	if resolved.APIKey == "" && resolved.Provider != Ollama && !isLoopbackURL(resolved.BaseURL) {
		return Config{}, ErrMissingCredential
	}

	resolved.UseResponses = resolved.wantsResponses()

	return resolved, nil
}

// wantsResponses decides which wire format to use.
//
// Only OpenAI itself implements the Responses API today; the other
// OpenAI-compatible providers implement chat-completions only, and sending them
// a Responses request fails outright. So this stays conservative and opt-in
// everywhere else - a caller pointing at a gateway that does support it can set
// UseResponses directly.
func (c Config) wantsResponses() bool {
	if c.DisableResponses {
		return false
	}

	if c.UseResponses {
		return true
	}

	if c.Provider != OpenAI {
		return false
	}

	// reasoning models are the ones that gain from it
	return catalogue.Lookup(c.Model).SupportsReasoning
}

// responsesURL is the Responses endpoint for this configuration.
func (c Config) responsesURL() string {
	return c.BaseURL + "/responses"
}

// isLoopback reports whether a host is local, which is the one case where a
// plaintext endpoint is reasonable.
func isLoopback(host string) bool {
	name := host

	if index := strings.LastIndex(host, ":"); index >= 0 {
		name = host[:index]
	}

	switch strings.ToLower(name) {
	case "localhost", "127.0.0.1", "::1", "[::1]":
		return true
	}

	return false
}

func isLoopbackURL(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil {
		return false
	}

	return isLoopback(parsed.Host)
}

// completionsURL is the chat-completions endpoint for this configuration.
func (c Config) completionsURL() string {
	return c.BaseURL + "/chat/completions"
}
