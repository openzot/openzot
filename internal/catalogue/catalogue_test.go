package catalogue

import (
	"slices"
	"strings"
	"testing"
)

func TestLookupMatchesLongestPrefix(t *testing.T) {
	tests := []struct {
		model string
		want  int
	}{
		// exact entries win
		{"gpt-5.4-mini", 400_000},
		{"gpt-5.4", 1_050_000},
		{"glm-5.3", 1_000_000},
		{"glm-5.2", 1_000_000},
		{"kimi-k3", 1_000_000},
		{"claude-5-sonnet", 1_000_000},

		// a dated point release falls through to its family rather than the
		// default - the case an exact-name map would miss
		{"gpt-5.4-mini-2026-03-01", 400_000},
		{"gemini-3-pro-preview", 1_048_576},

		// longest prefix wins: deepseek-r is a reasoning variant with its own
		// smaller window
		{"deepseek-r2", 128_000},
		{"deepseek-v4-pro", 1_048_600},

		// the full OpenAI range is present, not just the current flagship
		{"gpt-5.6-sol", 1_050_000},
		{"o3", 200_000},

		// a provider-qualified name resolves on the segment after the last slash
		{"openrouter/gpt-5.4", 1_050_000},
		{"openrouter/z-ai/glm-5.2", 1_000_000},

		{"something-nobody-has-heard-of", DefaultContextWindow},
	}

	for _, test := range tests {
		t.Run(test.model, func(t *testing.T) {
			if got := Lookup(test.model).ContextWindow; got != test.want {
				t.Errorf("ContextWindow = %d, want %d", got, test.want)
			}
		})
	}
}

// A gateway-qualified name names the creator's model: "vercel/zai/glm-5.3" has
// to reach the same entry as "glm-5.3", and a resolver that stopped at the first
// slash would budget a million-token model on the default 128K window instead.
func TestGatewayQualifiedNameResolvesToTheCreatorEntry(t *testing.T) {
	qualified := Lookup("vercel/zai/glm-5.3")
	direct := Lookup("glm-5.3")

	if qualified != direct {
		t.Fatalf("vercel/zai/glm-5.3 = %+v, glm-5.3 = %+v", qualified, direct)
	}

	if qualified.Provider != "zai" {
		t.Errorf("provider = %q, want the creator rather than the gateway", qualified.Provider)
	}

	if qualified == Default {
		t.Errorf("vercel/zai/glm-5.3 resolved to the Default entry - the name fell through")
	}

	if !qualified.SupportsTools || !qualified.SupportsReasoning {
		t.Errorf("GLM 5.3 capabilities = %+v", qualified)
	}
}

// The budget is derived from the entry, not carried alongside it: raise or lower
// a model's output ceiling and the input budget has to move with it, or the
// thread builder trims to a number the catalogue no longer says.
func TestInputBudgetTracksTheEntry(t *testing.T) {
	for _, name := range []string{"glm-5.3", "glm-5.2", "gpt-5.4", "claude-5-opus", "sonar-pro"} {
		entry := Lookup(name)

		want := entry.ContextWindow - entry.MaxOutputTokens
		if half := entry.ContextWindow / 2; want < half {
			want = half
		}

		if got := entry.InputBudget(); got != want {
			t.Errorf("%s: input budget = %d, want %d for a %d window with a %d output ceiling",
				name, got, want, entry.ContextWindow, entry.MaxOutputTokens)
		}
	}
}

func TestReasoningModelsAreFlagged(t *testing.T) {
	// the loop exempts reasoning from the runaway backstop, and the provider
	// prefers the Responses API where reasoning state can survive a tool round
	for _, model := range []string{"deepseek-r2", "gpt-5.4-mini", "glm-5.3", "kimi-k3", "claude-5-opus"} {
		if !Lookup(model).SupportsReasoning {
			t.Errorf("%s should be flagged as a reasoning model", model)
		}
	}

	for _, model := range []string{"mistral-large-2", "devstral-2"} {
		if Lookup(model).SupportsReasoning {
			t.Errorf("%s does not emit a reasoning channel", model)
		}
	}
}

// Realtime is a WebSocket audio protocol, not something a coding harness can
// drive, so it is deliberately absent.
func TestNoRealtimeEntries(t *testing.T) {
	for name := range models {
		if strings.Contains(name, "realtime") {
			t.Errorf("catalogue should not carry realtime model %q", name)
		}
	}
}

// A search-augmented model cannot drive an agentic loop, and the caller needs to
// know before it tries.
func TestModelsWithoutToolsAreFlagged(t *testing.T) {
	if Lookup("sonar-reasoning-pro").SupportsTools {
		t.Error("sonar does not accept tool definitions")
	}

	if !Lookup("gpt-5.4-mini").SupportsTools {
		t.Error("gpt-5.4-mini accepts tools")
	}
}

func TestKnownReportsCoverage(t *testing.T) {
	for _, model := range []string{"glm-5.3", "gpt-5.4-mini-2026-01-01", "claude-5-sonnet", "qwen-3.8-max"} {
		if !Known(model) {
			t.Errorf("%s should be recognised", model)
		}
	}

	if Known("totally-made-up-model") {
		t.Error("an unknown model must not report as known")
	}
}

func TestLegacyGPTModelsAreNotOffered(t *testing.T) {
	for _, model := range []string{
		"gpt-4.1", "gpt-4.1-mini", "gpt-4.5", "gpt-4o", "gpt-4o-mini",
		"gpt-4-turbo", "gpt-4", "gpt-3.5-turbo",
	} {
		if Known(model) {
			t.Errorf("legacy model %s should not be in the catalogue", model)
		}
	}
}

// Every entry must be coherent, or the budget maths downstream is nonsense.
func TestEntriesAreCoherent(t *testing.T) {
	check := func(name string, entry Model) {
		if entry.ContextWindow <= 0 {
			t.Errorf("%s: context window %d", name, entry.ContextWindow)
		}

		if entry.MaxOutputTokens <= 0 {
			t.Errorf("%s: max output %d", name, entry.MaxOutputTokens)
		}

		if entry.MaxOutputTokens >= entry.ContextWindow {
			t.Errorf("%s: output %d does not fit in a %d window", name, entry.MaxOutputTokens, entry.ContextWindow)
		}

		if entry.Provider == "" {
			t.Errorf("%s: no provider", name)
		}
	}

	for name, entry := range models {
		check(name, entry)
	}
}

// The package-level budget has to agree with the entry's own, including for a
// model nobody has catalogued - which is the case that decides how much history
// an unrecognised model gets, and the one a sweep over the table cannot reach.
func TestInputBudgetByNameAgreesWithTheEntry(t *testing.T) {
	for _, model := range []string{"gpt-5.4", "claude-sonnet-5", "gpt-5.4-mini-2026-03-01", "unknown-model"} {
		if got, want := InputBudget(model), Lookup(model).InputBudget(); got != want {
			t.Errorf("%s: InputBudget = %d, the entry says %d", model, got, want)
		}
	}

	if got := InputBudget("unknown-model"); got >= DefaultContextWindow || got <= 0 {
		t.Errorf("an unknown model got a budget of %d in a %d window", got, DefaultContextWindow)
	}
}

// The whole OpenAI range is catalogued, not only the models of the moment.
func TestOpenAICoverage(t *testing.T) {
	for _, model := range []string{
		"gpt-5.6-sol", "gpt-5.5", "gpt-5.4", "gpt-5.4-mini", "gpt-5.4-nano",
		"gpt-5.2", "gpt-5.1", "gpt-5", "gpt-5-mini", "gpt-5-nano",
		"o4-mini", "o3-mini", "o3",
	} {
		if _, ok := models[model]; !ok {
			t.Errorf("%s is missing from the catalogue", model)
		}
	}
}

// Every entry names the provider it originates from, and the sections are not
// just cosmetic - a caller can group by it.
// Every provider section has to stay populated. The catalogue is organised by
// provider, and a section emptied by a bad edit would otherwise just look like
// a provider nobody uses.
func TestEveryProviderSectionIsPopulated(t *testing.T) {
	providers := map[string]int{}

	for _, entry := range models {
		providers[entry.Provider]++
	}

	for _, want := range []string{
		"openai", "anthropic", "google", "zai", "moonshot",
		"minimax", "qwen", "deepseek", "mistral", "xai", "meta", "perplexity",
	} {
		if providers[want] == 0 {
			t.Errorf("no models catalogued for provider %q", want)
		}
	}
}

func TestNamesIsSorted(t *testing.T) {
	names := Names()

	if len(names) != len(models) {
		t.Fatalf("got %d names, want %d", len(names), len(models))
	}

	for index := 1; index < len(names); index++ {
		if names[index-1] > names[index] {
			t.Fatalf("names are not sorted at %d: %q > %q", index, names[index-1], names[index])
		}
	}
}

// Direct providers expose only their own catalogue section, while model
// gateways expose the whole catalogue they can route.
func TestNamesForProvider(t *testing.T) {
	openai := NamesForProvider(" openai ")
	if len(openai) == 0 {
		t.Fatal("OpenAI has no built-in models")
	}
	for _, name := range openai {
		if models[name].Provider != "openai" {
			t.Fatalf("OpenAI list contains %q from %q", name, models[name].Provider)
		}
	}
	if unknown := NamesForProvider("custom"); len(unknown) != 0 {
		t.Fatalf("unknown provider models = %v", unknown)
	}
}

// A gateway originates nothing, so grouping by Provider would leave it with an
// empty list - and an empty list is not "no models known", it is a connection
// whose every model config validation rejects and whose listing UI shows
// nothing. Every gateway that routes by a creator-qualified name has to be
// offered the whole catalogue.
func TestGatewaysExposeTheWholeCatalogue(t *testing.T) {
	for _, gateway := range []string{"openrouter", "vercel", "cloudflare"} {
		t.Run(gateway, func(t *testing.T) {
			names := NamesForProvider(gateway)

			if len(names) != len(Names()) {
				t.Fatalf("%s lists %d models, want the whole catalogue (%d)", gateway, len(names), len(Names()))
			}

			// models from creators the gateway is not, which is the point of a
			// gateway and what grouping by Provider would drop
			for _, want := range []string{"gpt-5.4", "claude-5-sonnet", "glm-5.3"} {
				if !slices.Contains(names, want) {
					t.Errorf("%s cannot route %q", gateway, want)
				}
			}
		})
	}
}

// The budget has to leave room for the answer, or a full thread produces an
// empty turn and the loop burns its continuations retrying it.
// Every catalogued model must leave room to answer. A thread built right up to
// the window presents as an empty or truncated turn rather than a budgeting
// error, which is much harder to diagnose from a log.
func TestEveryModelLeavesRoomForTheAnswer(t *testing.T) {
	for name, entry := range models {
		budget := entry.InputBudget()

		if budget >= entry.ContextWindow {
			t.Errorf("%s: budget %d fills the whole %d window", name, budget, entry.ContextWindow)
		}

		if budget < entry.ContextWindow/2 {
			t.Errorf("%s: budget %d is under half the window", name, budget)
		}

		if budget+entry.MaxOutputTokens > entry.ContextWindow && budget != entry.ContextWindow/2 {
			t.Errorf("%s: budget %d plus output %d exceeds the window", name, budget, entry.MaxOutputTokens)
		}
	}
}
