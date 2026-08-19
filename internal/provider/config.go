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
	"net"
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

	// Vercel is the Vercel AI Gateway, an OpenAI-compatible proxy with a fixed
	// endpoint. Like OpenRouter, it routes by a provider-qualified model name.
	Vercel = "vercel"

	// Cloudflare is the Cloudflare AI Gateway. Recognised, but with no fixed
	// endpoint: its URL embeds the account and gateway ids, so a run must supply
	// one. See gatewayProviders.
	Cloudflare = "cloudflare"

	Custom = "custom"
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
	Vercel:     "https://ai-gateway.vercel.sh/v1",
}

// gatewayProviders are recognised providers whose endpoint is account-specific,
// so they carry no fixed URL and a run must give a base_url. They are listed for
// discoverability and a tailored error; the value is the URL shape to show.
//
// Cloudflare's AI Gateway is the case: the account and gateway ids live in the
// path, so there is nothing to hardcode.
var gatewayProviders = map[string]string{
	Cloudflare: "https://gateway.ai.cloudflare.com/v1/<account>/<gateway>/compat",
}

// gatewaysQualifyModels are the providers that route by a creator-qualified
// model name ("openai/gpt-4o"). For these, a bare model the user typed is
// completed from the catalogue - see qualifyModel.
var gatewaysQualifyModels = map[string]bool{
	OpenRouter: true,
	Vercel:     true,
	Cloudflare: true,
}

// gatewaySlugs maps zot's provider name to a gateway's own creator slug, for the
// cases where they differ. A provider absent from a gateway's map keeps its own
// name, which is the common case.
//
// These are each gateway's naming convention, and they drift as gateways rename
// creators - they are the one part of auto-qualification that has to be verified
// against a gateway's published model list rather than derived. A wrong or
// missing entry does not corrupt anything: it produces a slug the gateway
// rejects, which is the same failure the user would have had typing the bare
// name, only now it is one line to fix here.
var gatewaySlugs = map[string]map[string]string{
	OpenRouter: {
		"zai":      "z-ai",
		"meta":     "meta-llama",
		"xai":      "x-ai",
		"mistral":  "mistralai",
		"moonshot": "moonshotai",
	},
	Vercel: {
		// Vercel's slugs largely match zot's names; add mismatches as found.
	},
	Cloudflare: {
		// Cloudflare's compat endpoint uses provider names close to zot's; add
		// mismatches (e.g. google-ai-studio) as found.
	},
}

// qualifyModel completes a bare model name for a gateway that routes by
// creator/model.
//
// It acts only when all three hold: the provider is such a gateway, the model
// carries no slash already, and the catalogue recognises it. Any of those
// failing leaves the name exactly as the user typed it - guessing a prefix zot
// cannot justify is worse than passing the name through, because a wrong prefix
// misroutes silently where a bare name merely fails cleanly at the gateway.
//
// The point is that a model is the same model whichever gateway serves it: a
// user should be able to say "glm-5.2" and have zot supply the "z-ai/" or "zai/"
// each gateway happens to want.
func qualifyModel(provider, model string) string {
	if !gatewaysQualifyModels[provider] {
		return model
	}

	if strings.Contains(model, "/") {
		return model // already creator-qualified; respect it
	}

	origin := catalogue.Lookup(model).Provider

	if origin == "" || origin == catalogue.Default.Provider {
		return model // unknown model - nothing to qualify it with
	}

	slug := origin

	if aliased, ok := gatewaySlugs[provider][origin]; ok {
		slug = aliased
	}

	return slug + "/" + model
}

// Providers lists the known provider identifiers.
func Providers() []string {
	names := make([]string, 0, len(baseURLs)+len(gatewayProviders)+1)

	for name := range baseURLs {
		names = append(names, name)
	}

	for name := range gatewayProviders {
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

	// On a gateway, complete a bare model name ("glm-5.2") into the
	// creator-qualified form it routes by ("z-ai/glm-5.2"), so the user does not
	// have to know each gateway's prefix.
	resolved.Model = qualifyModel(resolved.Provider, resolved.Model)

	base, known := baseURLs[resolved.Provider]

	// whether the endpoint was typed rather than looked up, which is what makes
	// the credential rule apply
	overridden := false

	switch {
	case resolved.BaseURL != "":
		parsed, err := url.Parse(resolved.BaseURL)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return Config{}, fmt.Errorf("provider: invalid base URL %q", resolved.BaseURL)
		}

		if parsed.Scheme != "https" && !isLoopbackHost(parsed.Hostname()) {
			return Config{}, fmt.Errorf("provider: base URL must use https (got %q)", resolved.BaseURL)
		}

		resolved.BaseURL = strings.TrimRight(resolved.BaseURL, "/")

		overridden = true

	case known:
		resolved.BaseURL = base

	default:
		if hint, ok := gatewayProviders[resolved.Provider]; ok {
			return Config{}, fmt.Errorf(
				"provider: %q needs a base URL - its endpoint is account-specific (e.g. %s); set base_url on the provider",
				resolved.Provider, hint)
		}

		return Config{}, fmt.Errorf("provider: unknown provider %q and no base URL given", resolved.Provider)
	}

	if resolved.APIKey == "" && resolved.Provider != Ollama && !isLoopbackURL(resolved.BaseURL) {
		if overridden {
			// naming the endpoint is the whole point: the reader's instinct is to
			// go looking for a missing export, when the thing that withdrew the
			// ambient key is the base_url they just added
			return Config{}, fmt.Errorf(
				"%w: %s overrides the default endpoint with %s, which needs a key of its own - a credential is scoped to the host it was issued for",
				ErrMissingCredential, resolved.Provider, resolved.BaseURL)
		}

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

// isLoopbackHost reports whether a hostname is local, which is the one case
// where a plaintext endpoint is reasonable.
//
// It takes a hostname rather than a host: url.URL.Hostname() is what strips both
// the port and the brackets an IPv6 address is written in. Splitting the raw host
// on its last colon cannot do that - "[::1]" has no port and every colon in it
// belongs to the address - and got the bracketed form wrong in both directions.
func isLoopbackHost(hostname string) bool {
	if strings.EqualFold(hostname, "localhost") {
		return true
	}

	// an address rather than a name: 127.0.0.0/8 and ::1 are all local
	if ip := net.ParseIP(hostname); ip != nil {
		return ip.IsLoopback()
	}

	return false
}

func isLoopbackURL(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil {
		return false
	}

	return isLoopbackHost(parsed.Hostname())
}

// completionsURL is the chat-completions endpoint for this configuration.
func (c Config) completionsURL() string {
	return c.BaseURL + "/chat/completions"
}
