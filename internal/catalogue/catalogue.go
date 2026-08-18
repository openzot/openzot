// Package catalogue records what the engine needs to know about a model before
// it calls one: how much context it has, how much of that it may spend on the
// answer, and whether it can use tools or emits a reasoning channel.
//
// None of it can be asked of the provider - the OpenAI-compatible
// chat-completions API has no endpoint that reports a model's limits - so it is
// kept here, with a conservative default for anything unrecognised.
package catalogue

import (
	"sort"
	"strings"
)

// Model describes a model's capabilities.
type Model struct {
	// Provider is who serves it. Informational: a model is usually available
	// from several providers, and this records where it originates.
	Provider string

	// ContextWindow is the total token budget for input plus output.
	ContextWindow int

	// MaxOutputTokens bounds a single response.
	MaxOutputTokens int

	// SupportsTools reports whether the model accepts tool definitions. A model
	// without them cannot drive an agentic loop at all.
	SupportsTools bool

	// SupportsReasoning reports whether the model emits a reasoning channel.
	//
	// Two things key off it: the loop exempts reasoning messages from the
	// runaway-text backstop, being the model's scratchpad rather than an answer;
	// and on OpenAI the provider prefers the Responses API, because
	// chat-completions has nowhere to carry reasoning state between tool rounds.
	SupportsReasoning bool
}

// DefaultContextWindow is assumed for models the catalogue has not heard of.
//
// Deliberately modest: under-estimating a window costs an early compaction,
// while over-estimating gets the request rejected outright. The first is a small
// waste, the second is a failed run.
const DefaultContextWindow = 128_000

// Default is returned for unknown models.
var Default = Model{
	Provider:        "unknown",
	ContextWindow:   DefaultContextWindow,
	MaxOutputTokens: DefaultContextWindow / 4,
	SupportsTools:   true,
}

// models is the catalogue.
//
// Entries are matched exactly first, then by longest prefix - so `gpt-5.4` names
// a specific model, and `gpt-5.4-mini-2026-03-01` resolves through it rather
// than dropping to the default. That is why there is one table and not two:
// providers ship dated and point-release variants constantly, and a name is
// either an entry or a variant of one.
//
// @note there are deliberately no realtime models. Realtime is a WebSocket audio
// protocol, not a chat-completions model, and nothing in a coding loop could
// drive one.
var models = map[string]Model{
	// ---------------------------------------------------------------- OpenAI

	"gpt-5.6-sol":   {Provider: "openai", ContextWindow: 1_050_000, MaxOutputTokens: 128_000, SupportsTools: true, SupportsReasoning: true},
	"gpt-5.6-terra": {Provider: "openai", ContextWindow: 1_050_000, MaxOutputTokens: 128_000, SupportsTools: true, SupportsReasoning: true},
	"gpt-5.6-luna":  {Provider: "openai", ContextWindow: 1_050_000, MaxOutputTokens: 128_000, SupportsTools: true, SupportsReasoning: true},
	"gpt-5.5":       {Provider: "openai", ContextWindow: 1_050_000, MaxOutputTokens: 128_000, SupportsTools: true, SupportsReasoning: true},
	"gpt-5.4":       {Provider: "openai", ContextWindow: 1_050_000, MaxOutputTokens: 128_000, SupportsTools: true, SupportsReasoning: true},
	"gpt-5.4-pro":   {Provider: "openai", ContextWindow: 1_050_000, MaxOutputTokens: 128_000, SupportsTools: true, SupportsReasoning: true},
	"gpt-5.4-mini":  {Provider: "openai", ContextWindow: 400_000, MaxOutputTokens: 128_000, SupportsTools: true, SupportsReasoning: true},
	"gpt-5.4-nano":  {Provider: "openai", ContextWindow: 400_000, MaxOutputTokens: 128_000, SupportsTools: true, SupportsReasoning: true},
	"gpt-5.2":       {Provider: "openai", ContextWindow: 400_000, MaxOutputTokens: 128_000, SupportsTools: true, SupportsReasoning: true},
	"gpt-5.1":       {Provider: "openai", ContextWindow: 400_000, MaxOutputTokens: 128_000, SupportsTools: true, SupportsReasoning: true},
	"gpt-5":         {Provider: "openai", ContextWindow: 400_000, MaxOutputTokens: 128_000, SupportsTools: true, SupportsReasoning: true},
	"gpt-5-mini":    {Provider: "openai", ContextWindow: 400_000, MaxOutputTokens: 128_000, SupportsTools: true, SupportsReasoning: true},
	"gpt-5-nano":    {Provider: "openai", ContextWindow: 400_000, MaxOutputTokens: 128_000, SupportsTools: true, SupportsReasoning: true},
	"o4-mini":       {Provider: "openai", ContextWindow: 200_000, MaxOutputTokens: 100_000, SupportsTools: true, SupportsReasoning: true},
	"o3-mini":       {Provider: "openai", ContextWindow: 200_000, MaxOutputTokens: 100_000, SupportsTools: true, SupportsReasoning: true},
	"o3":            {Provider: "openai", ContextWindow: 200_000, MaxOutputTokens: 100_000, SupportsTools: true, SupportsReasoning: true},
	"gpt-4.1":       {Provider: "openai", ContextWindow: 1_047_576, MaxOutputTokens: 32_768, SupportsTools: true},
	"gpt-4.1-mini":  {Provider: "openai", ContextWindow: 1_047_576, MaxOutputTokens: 32_768, SupportsTools: true},
	"gpt-4.1-nano":  {Provider: "openai", ContextWindow: 1_047_576, MaxOutputTokens: 32_768, SupportsTools: true},
	"gpt-4.5":       {Provider: "openai", ContextWindow: 128_000, MaxOutputTokens: 16_000, SupportsTools: true},
	"gpt-4o":        {Provider: "openai", ContextWindow: 128_000, MaxOutputTokens: 4_096, SupportsTools: true},
	"gpt-4o-mini":   {Provider: "openai", ContextWindow: 128_000, MaxOutputTokens: 4_096, SupportsTools: true},
	"gpt-4-turbo":   {Provider: "openai", ContextWindow: 128_000, MaxOutputTokens: 4_096, SupportsTools: true},
	"gpt-4":         {Provider: "openai", ContextWindow: 8_000, MaxOutputTokens: 2_000, SupportsTools: true},
	"gpt-3.5-turbo": {Provider: "openai", ContextWindow: 16_000, MaxOutputTokens: 4_000, SupportsTools: true},

	// ------------------------------------------------------------- Anthropic

	"claude-5-opus":     {Provider: "anthropic", ContextWindow: 1_000_000, MaxOutputTokens: 128_000, SupportsTools: true, SupportsReasoning: true},
	"claude-5-sonnet":   {Provider: "anthropic", ContextWindow: 1_000_000, MaxOutputTokens: 128_000, SupportsTools: true, SupportsReasoning: true},
	"claude-5-haiku":    {Provider: "anthropic", ContextWindow: 200_000, MaxOutputTokens: 64_000, SupportsTools: true},
	"claude-4.8-opus":   {Provider: "anthropic", ContextWindow: 1_000_000, MaxOutputTokens: 128_000, SupportsTools: true, SupportsReasoning: true},
	"claude-4.7-opus":   {Provider: "anthropic", ContextWindow: 1_000_000, MaxOutputTokens: 128_000, SupportsTools: true, SupportsReasoning: true},
	"claude-4.6-opus":   {Provider: "anthropic", ContextWindow: 1_000_000, MaxOutputTokens: 128_000, SupportsTools: true, SupportsReasoning: true},
	"claude-4.6-sonnet": {Provider: "anthropic", ContextWindow: 1_000_000, MaxOutputTokens: 128_000, SupportsTools: true, SupportsReasoning: true},
	"claude-4.5-opus":   {Provider: "anthropic", ContextWindow: 200_000, MaxOutputTokens: 64_000, SupportsTools: true, SupportsReasoning: true},
	"claude-4.5-sonnet": {Provider: "anthropic", ContextWindow: 1_000_000, MaxOutputTokens: 64_000, SupportsTools: true, SupportsReasoning: true},
	"claude-4.5-haiku":  {Provider: "anthropic", ContextWindow: 200_000, MaxOutputTokens: 64_000, SupportsTools: true},
	"claude-4.1-opus":   {Provider: "anthropic", ContextWindow: 200_000, MaxOutputTokens: 32_000, SupportsTools: true, SupportsReasoning: true},
	"claude-4-opus":     {Provider: "anthropic", ContextWindow: 200_000, MaxOutputTokens: 8_192, SupportsTools: true, SupportsReasoning: true},
	"claude-4-sonnet":   {Provider: "anthropic", ContextWindow: 1_000_000, MaxOutputTokens: 8_192, SupportsTools: true, SupportsReasoning: true},

	// @note broad fallbacks, so an unrecognised Claude release lands somewhere
	// sensible rather than on the default
	"claude-opus":   {Provider: "anthropic", ContextWindow: 200_000, MaxOutputTokens: 32_000, SupportsTools: true, SupportsReasoning: true},
	"claude-sonnet": {Provider: "anthropic", ContextWindow: 200_000, MaxOutputTokens: 64_000, SupportsTools: true, SupportsReasoning: true},
	"claude-haiku":  {Provider: "anthropic", ContextWindow: 200_000, MaxOutputTokens: 64_000, SupportsTools: true},
	"claude":        {Provider: "anthropic", ContextWindow: 200_000, MaxOutputTokens: 32_000, SupportsTools: true, SupportsReasoning: true},

	// ---------------------------------------------------------------- Google

	"gemini-3.6-flash":      {Provider: "google", ContextWindow: 1_000_000, MaxOutputTokens: 64_000, SupportsTools: true, SupportsReasoning: true},
	"gemini-3.5-flash":      {Provider: "google", ContextWindow: 1_000_000, MaxOutputTokens: 64_000, SupportsTools: true, SupportsReasoning: true},
	"gemini-3.1-pro":        {Provider: "google", ContextWindow: 1_000_000, MaxOutputTokens: 64_000, SupportsTools: true, SupportsReasoning: true},
	"gemini-3.1-flash-lite": {Provider: "google", ContextWindow: 1_000_000, MaxOutputTokens: 65_000, SupportsTools: true, SupportsReasoning: true},
	"gemini-3-flash":        {Provider: "google", ContextWindow: 1_000_000, MaxOutputTokens: 64_000, SupportsTools: true, SupportsReasoning: true},
	"gemini-3-pro":          {Provider: "google", ContextWindow: 1_048_576, MaxOutputTokens: 65_536, SupportsTools: true, SupportsReasoning: true},
	"gemini-2.5-pro":        {Provider: "google", ContextWindow: 1_048_576, MaxOutputTokens: 8_192, SupportsTools: true, SupportsReasoning: true},
	"gemini-2.5-flash":      {Provider: "google", ContextWindow: 1_000_000, MaxOutputTokens: 65_536, SupportsTools: true, SupportsReasoning: true},
	"gemini-2.5-flash-lite": {Provider: "google", ContextWindow: 1_048_576, MaxOutputTokens: 65_535, SupportsTools: true},
	"gemini":                {Provider: "google", ContextWindow: 1_000_000, MaxOutputTokens: 64_000, SupportsTools: true, SupportsReasoning: true},
	"gemma-4-31b":           {Provider: "google", ContextWindow: 262_144, MaxOutputTokens: 65_536, SupportsTools: true, SupportsReasoning: true},
	"gemma":                 {Provider: "google", ContextWindow: 262_144, MaxOutputTokens: 65_536, SupportsTools: true, SupportsReasoning: true},

	// -------------------------------------------------------------------- ZAI

	"glm-5.3":       {Provider: "zai", ContextWindow: 1_000_000, MaxOutputTokens: 12_800, SupportsTools: true, SupportsReasoning: true},
	"glm-5.2":       {Provider: "zai", ContextWindow: 1_000_000, MaxOutputTokens: 128_000, SupportsTools: true, SupportsReasoning: true},
	"glm-5.1":       {Provider: "zai", ContextWindow: 202_000, MaxOutputTokens: 64_000, SupportsTools: true, SupportsReasoning: true},
	"glm-5-turbo":   {Provider: "zai", ContextWindow: 202_800, MaxOutputTokens: 128_000, SupportsTools: true, SupportsReasoning: true},
	"glm-5v-turbo":  {Provider: "zai", ContextWindow: 200_000, MaxOutputTokens: 128_000, SupportsTools: true, SupportsReasoning: true},
	"glm-5":         {Provider: "zai", ContextWindow: 202_800, MaxOutputTokens: 64_000, SupportsTools: true, SupportsReasoning: true},
	"glm-4.7-flash": {Provider: "zai", ContextWindow: 200_000, MaxOutputTokens: 128_000, SupportsTools: true, SupportsReasoning: true},
	"glm-4.7":       {Provider: "zai", ContextWindow: 200_000, MaxOutputTokens: 40_000, SupportsTools: true, SupportsReasoning: true},
	"glm":           {Provider: "zai", ContextWindow: 200_000, MaxOutputTokens: 64_000, SupportsTools: true, SupportsReasoning: true},

	// --------------------------------------------------------------- Moonshot

	"kimi-k3":        {Provider: "moonshot", ContextWindow: 1_000_000, MaxOutputTokens: 131_072, SupportsTools: true, SupportsReasoning: true},
	"kimi-k2.7-code": {Provider: "moonshot", ContextWindow: 256_000, MaxOutputTokens: 32_768, SupportsTools: true, SupportsReasoning: true},
	"kimi-k2.6":      {Provider: "moonshot", ContextWindow: 262_000, MaxOutputTokens: 65_500, SupportsTools: true, SupportsReasoning: true},
	"kimi-k2.5":      {Provider: "moonshot", ContextWindow: 262_114, MaxOutputTokens: 65_535, SupportsTools: true, SupportsReasoning: true},
	"kimi":           {Provider: "moonshot", ContextWindow: 262_000, MaxOutputTokens: 65_500, SupportsTools: true, SupportsReasoning: true},

	// ----------------------------------------------------------------- MiniMax

	"minimax-m3":   {Provider: "minimax", ContextWindow: 1_000_000, MaxOutputTokens: 128_000, SupportsTools: true, SupportsReasoning: true},
	"minimax-m2.7": {Provider: "minimax", ContextWindow: 204_800, MaxOutputTokens: 131_000, SupportsTools: true, SupportsReasoning: true},
	"minimax-m2.5": {Provider: "minimax", ContextWindow: 204_800, MaxOutputTokens: 51_200, SupportsTools: true, SupportsReasoning: true},
	"minimax":      {Provider: "minimax", ContextWindow: 204_800, MaxOutputTokens: 51_200, SupportsTools: true, SupportsReasoning: true},

	// -------------------------------------------------------------------- Qwen

	"qwen-3.8-max":  {Provider: "qwen", ContextWindow: 1_000_000, MaxOutputTokens: 128_000, SupportsTools: true, SupportsReasoning: true},
	"qwen-3.7-max":  {Provider: "qwen", ContextWindow: 991_000, MaxOutputTokens: 64_000, SupportsTools: true, SupportsReasoning: true},
	"qwen-3.6-max":  {Provider: "qwen", ContextWindow: 240_000, MaxOutputTokens: 64_000, SupportsTools: true, SupportsReasoning: true},
	"qwen-3.6-plus": {Provider: "qwen", ContextWindow: 1_000_000, MaxOutputTokens: 64_000, SupportsTools: true, SupportsReasoning: true},
	"qwen":          {Provider: "qwen", ContextWindow: 240_000, MaxOutputTokens: 64_000, SupportsTools: true, SupportsReasoning: true},

	// ---------------------------------------------------------------- DeepSeek

	"deepseek-v4-pro":   {Provider: "deepseek", ContextWindow: 1_048_600, MaxOutputTokens: 65_536, SupportsTools: true},
	"deepseek-v4-flash": {Provider: "deepseek", ContextWindow: 1_000_000, MaxOutputTokens: 65_536, SupportsTools: true},
	"deepseek-v3.2":     {Provider: "deepseek", ContextWindow: 164_000, MaxOutputTokens: 32_768, SupportsTools: true},
	"deepseek-r":        {Provider: "deepseek", ContextWindow: 128_000, MaxOutputTokens: 32_768, SupportsTools: true, SupportsReasoning: true},
	"deepseek":          {Provider: "deepseek", ContextWindow: 164_000, MaxOutputTokens: 32_768, SupportsTools: true},

	// -------------------------------------------------------------------- Xiaomi

	"mimo-v2.5-pro": {Provider: "xiaomi", ContextWindow: 1_050_000, MaxOutputTokens: 131_000, SupportsTools: true, SupportsReasoning: true},
	"mimo-v2.5":     {Provider: "xiaomi", ContextWindow: 1_050_000, MaxOutputTokens: 131_000, SupportsTools: true, SupportsReasoning: true},
	"mimo":          {Provider: "xiaomi", ContextWindow: 1_050_000, MaxOutputTokens: 131_000, SupportsTools: true, SupportsReasoning: true},

	// ---------------------------------------------------------------- Mistral

	"mistral-large": {Provider: "mistral", ContextWindow: 256_000, MaxOutputTokens: 64_000, SupportsTools: true},
	"mistral-small": {Provider: "mistral", ContextWindow: 32_000, MaxOutputTokens: 4_000, SupportsTools: true},
	"mistral":       {Provider: "mistral", ContextWindow: 128_000, MaxOutputTokens: 16_384, SupportsTools: true},
	"devstral-2":    {Provider: "mistral", ContextWindow: 256_000, MaxOutputTokens: 64_000, SupportsTools: true},
	"devstral":      {Provider: "mistral", ContextWindow: 256_000, MaxOutputTokens: 64_000, SupportsTools: true},
	"codestral":     {Provider: "mistral", ContextWindow: 256_000, MaxOutputTokens: 64_000, SupportsTools: true},

	// --------------------------------------------------------------------- xAI

	"grok-4.5": {Provider: "xai", ContextWindow: 500_000, MaxOutputTokens: 128_000, SupportsTools: true, SupportsReasoning: true},
	"grok":     {Provider: "xai", ContextWindow: 256_000, MaxOutputTokens: 32_768, SupportsTools: true, SupportsReasoning: true},

	// ------------------------------------------------------------------- Meta

	"llama-4": {Provider: "meta", ContextWindow: 1_000_000, MaxOutputTokens: 16_384, SupportsTools: true},
	"llama-3": {Provider: "meta", ContextWindow: 128_000, MaxOutputTokens: 8_192, SupportsTools: true},
	"llama":   {Provider: "meta", ContextWindow: 128_000, MaxOutputTokens: 8_192, SupportsTools: true},

	// ------------------------------------------------------------- Perplexity
	//
	// @note search-augmented models take no tools, so they cannot drive the
	// agentic loop. Listed so a caller finds out before the run rather than from
	// a provider error.

	"sonar-reasoning-pro": {Provider: "perplexity", ContextWindow: 127_000, MaxOutputTokens: 8_000, SupportsReasoning: true},
	"sonar-reasoning":     {Provider: "perplexity", ContextWindow: 127_000, MaxOutputTokens: 8_000, SupportsReasoning: true},
	"sonar-pro":           {Provider: "perplexity", ContextWindow: 200_000, MaxOutputTokens: 64_000},
	"sonar":               {Provider: "perplexity", ContextWindow: 127_000, MaxOutputTokens: 8_000},
}

// normalize reduces a model string to the name a catalogue entry would use.
func normalize(model string) string {
	name := strings.ToLower(strings.TrimSpace(model))

	// a provider-qualified name ("openrouter/z-ai/glm-5.2") carries the model
	// after the last slash
	if index := strings.LastIndex(name, "/"); index >= 0 {
		name = name[index+1:]
	}

	// a deployment suffix ("gpt-5.4@2026-03-01") is not part of the identity
	if index := strings.Index(name, "@"); index >= 0 {
		name = name[:index]
	}

	return name
}

// Lookup returns what is known about a model.
//
// An exact entry wins; otherwise the longest matching prefix; otherwise Default.
// A caller never has to check whether a model is known - an unrecognised one is
// budgeted conservatively rather than refused, because declining to run against
// a model released last week would be worse than being careful with it.
func Lookup(model string) Model {
	name := normalize(model)

	if entry, ok := models[name]; ok {
		return entry
	}

	best := ""

	for prefix := range models {
		if strings.HasPrefix(name, prefix) && len(prefix) > len(best) {
			best = prefix
		}
	}

	if best == "" {
		return Default
	}

	return models[best]
}

// Known reports whether the catalogue recognises a model. An unknown model still
// runs; this is for diagnostics.
func Known(model string) bool {
	name := normalize(model)

	if _, ok := models[name]; ok {
		return true
	}

	for prefix := range models {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}

	return false
}

// Names lists every catalogued model, sorted. For `--help` output and
// diagnostics.
func Names() []string {
	names := make([]string, 0, len(models))

	for name := range models {
		names = append(names, name)
	}

	sort.Strings(names)

	return names
}

// NamesForProvider lists the catalogued models available from a provider,
// sorted. Gateways expose the whole catalogue because they route models from
// several creators; direct providers expose only models they originate.
func NamesForProvider(provider string) []string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "openrouter" || provider == "vercel" {
		return Names()
	}

	names := make([]string, 0)
	for name, model := range models {
		if model.Provider == provider {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// InputBudget is how many tokens of conversation may be sent to a model.
//
// It is the number the thread builder trims to and compaction triggers against,
// and it is not the same as the context window: the window has to hold the
// answer as well as the prompt. Filling it entirely leaves the model no room to
// reply, which does not present as a budgeting mistake - it presents as an empty
// or truncated turn, and the loop then spends its continuation budget retrying
// something that cannot succeed.
//
// So the output allowance is subtracted, and at least half the window is kept
// for input regardless, so a model advertising an enormous output ceiling cannot
// squeeze the conversation to nothing.
func (m Model) InputBudget() int {
	budget := m.ContextWindow - m.MaxOutputTokens

	if budget < m.ContextWindow/2 {
		budget = m.ContextWindow / 2
	}

	return budget
}

// InputBudget is the package-level convenience for a model name.
func InputBudget(model string) int {
	return Lookup(model).InputBudget()
}
