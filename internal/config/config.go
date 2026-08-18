// Package config loads zot's configuration, layering built-in defaults, an
// optional YAML file, and environment variables (defaults < file < env).
package config

import (
	"bytes"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/openzot/openzot/agent"
	"github.com/openzot/openzot/internal/catalogue"
	"github.com/openzot/openzot/tui"
)

// Config is the fully-resolved zot configuration.
type Config struct {
	Agent Agent `yaml:"agent"`
	UI    UI    `yaml:"ui"`
	// Skills discovered from the context directories. Not configured directly:
	// LoadProjectContext fills this from the SKILL.md files it finds, and the
	// engine describes them in the system prompt.
	Skills []agent.SkillDefinition `yaml:"-"`
	// DefaultProvider is the provider used when --provider is not given.
	DefaultProvider string `yaml:"default_provider"`
	// Providers are the named model-provider connections a run can target. zot
	// ships with one for each provider it knows, and a config file can override
	// their credentials or endpoint, or add custom model entries.
	Providers map[string]ProviderConfig `yaml:"providers"`
}

// ProviderConfig is a named model-provider connection zot can run against.
// Every provider authenticates with a Bearer credential.
type ProviderConfig struct {
	// Driver names the provider implementation this connection uses: "openai",
	// "anthropic", "groq", "ollama" and so on. Empty infers it from the
	// provider's own name, so a provider called "groq" needs no further
	// configuration.
	//
	// zot speaks the OpenAI-compatible chat-completions API to every provider, so
	// a provider connection is a URL and a credential rather than a protocol of
	// its own. The driver selects endpoint defaults and provider-specific quirks.
	Driver string `yaml:"driver"`
	// BaseURL overrides the API endpoint. Empty uses the built-in default.
	BaseURL string `yaml:"base_url"`
	// APIKey is the provider credential. Supports "$ENV_VAR" references, so no
	// secret need be written to disk.
	APIKey string `yaml:"api_key"`
	// Models is an optional custom model list for this provider. When omitted,
	// callers can use the built-in catalogue. When present, its keys are the
	// selectable names and each entry may alias or override that model.
	Models map[string]ModelConfig `yaml:"models"`
}

// ModelConfig is a custom model definition under a provider. Any field set here
// overrides the run's defaults when the model is selected.
type ModelConfig struct {
	// Driver overrides the provider's driver for this model, so one provider
	// connection can front several implementations.
	Driver string `yaml:"driver"`
	// Model is the underlying model id to send. Lets a custom name alias a real
	// model; leave empty to use the selected name as-is.
	Model string `yaml:"model"`
	// MaxIterations overrides the global iteration cap for this model.
	MaxIterations int `yaml:"max_iterations"`
	// APIKey is this model's own credential, overriding the provider's. Useful
	// where one gateway fronts several providers, each wanting its own key.
	// Supports "$ENV_VAR".
	APIKey string `yaml:"api_key"`
}

// builtinProviders are the providers zot ships with. Each falls back to its
// provider's conventional environment variable, so exporting that is the whole
// setup. The endpoint is left empty where the provider package already knows it.
var builtinProviders = map[string]struct {
	baseURL   string
	secretEnv string // the provider's conventional credential variable
}{
	// providers zot talks to directly, each reading its conventional key
	"openai":     {secretEnv: "OPENAI_API_KEY"},
	"anthropic":  {secretEnv: "ANTHROPIC_API_KEY"},
	"groq":       {secretEnv: "GROQ_API_KEY"},
	"mistral":    {secretEnv: "MISTRAL_API_KEY"},
	"deepseek":   {secretEnv: "DEEPSEEK_API_KEY"},
	"openrouter": {secretEnv: "OPENROUTER_API_KEY"},
	"together":   {secretEnv: "TOGETHER_API_KEY"},
	"cerebras":   {secretEnv: "CEREBRAS_API_KEY"},
	"xai":        {secretEnv: "XAI_API_KEY"},
	"moonshot":   {secretEnv: "MOONSHOT_API_KEY"},
	"zai":        {secretEnv: "ZAI_API_KEY"},
	"qwen":       {secretEnv: "DASHSCOPE_API_KEY"},
	"ollama":     {},

	// The Vercel AI Gateway: a fixed OpenAI-compatible endpoint, so it works
	// with just the key. Cloudflare's is deliberately absent - its endpoint is
	// account-specific, so a run configures it as a provider with a base_url
	// rather than reaching for it by name with no setup.
	"vercel": {secretEnv: "AI_GATEWAY_API_KEY"},
}

// ProviderDriver resolves which implementation a named provider uses.
//
// A provider named after a driver uses that driver, so the common case needs no
// configuration at all. An aliased connection states its driver explicitly.
func ProviderDriver(name string, provider ProviderConfig) string {
	if provider.Driver != "" {
		return provider.Driver
	}

	return name
}

// ProviderCredential returns the credential configured for a provider.
func ProviderCredential(provider ProviderConfig) string {
	return provider.APIKey
}

// ProviderModels returns the names exposed by a provider. An explicit custom
// list replaces the built-in catalogue for that connection; otherwise the
// provider's resolved driver selects its built-in models.
func ProviderModels(name string, provider ProviderConfig) []string {
	if len(provider.Models) > 0 {
		names := make([]string, 0, len(provider.Models))
		for name := range provider.Models {
			names = append(names, name)
		}
		slices.Sort(names)
		return names
	}
	return catalogue.NamesForProvider(ProviderDriver(name, provider))
}

// UI holds presentation options for the read-only viewer.
type UI struct {
	// Diff, when true, renders a framed, syntax-highlighted before/after diff
	// panel beneath every edit/write the agent makes.
	Diff bool `yaml:"diff"`
	// Plain forces the unstyled streaming renderer (no full-screen TUI). Without
	// a terminal Zot also streams, with styling decided separately by Color.
	Plain bool `yaml:"plain"`
	// Color controls ANSI styling for automatic non-interactive streams: auto,
	// always, or never. Plain still forces unstyled output, and Color never
	// enables terminal input.
	Color string `yaml:"color"`
	// Scrollback caps how many log lines the full-screen viewer keeps on screen.
	// Zero uses the built-in default; raise it to keep more of a long run visible
	// (at more memory). The full run is always in the session log regardless.
	Scrollback int `yaml:"scrollback"`
	// Stats selects which fields the header bar shows, and in what order (see
	// tui.KnownStats: model, provider, dir, iter, tools, edits, elapsed). Empty
	// uses the default set.
	Stats []string `yaml:"stats"`
}

// Agent holds the knobs that shape an autonomous run.
type Agent struct {
	// Model is the model name driving the agent.
	Model string `yaml:"model"`
	// MaxIterations caps how many plan/act/observe cycles the agent may run
	// before it is forced to stop.
	MaxIterations int `yaml:"max_iterations"`
	// MaxSettles bounds how many times the agent is nudged to record an outcome
	// (call success or failure) before the run is surfaced as unsettled. This
	// is "how hard we push the model to finish properly". Zero uses the built-in
	// default.
	MaxSettles int `yaml:"max_settles"`
	// MaxCalls caps the total number of tool calls across a run, independently of
	// iterations (one iteration can request several). Zero is unbounded - only
	// max_iterations is a finite default.
	MaxCalls int `yaml:"max_calls"`
	// MaxTime caps the wall-clock time of a run, as a duration string ("30m",
	// "2h", "90s"). Empty is unbounded.
	MaxTime string `yaml:"max_time"`
	// MaxTokens caps the output tokens of a single model response. Zero is
	// unbounded - like max_calls and max_time, zot sends no cap, so the model
	// produces its full output. A positive value caps a single response.
	MaxTokens int `yaml:"max_tokens"`
	// MaxContinuations caps recovery attempts within a run - a truncated
	// response, an empty turn, or a retriable provider error. Zero uses the
	// built-in default.
	MaxContinuations int `yaml:"max_continuations"`
	// MaxCycles is how many times the loop nudges the model out of a detected
	// repetition before giving up. Zero uses the built-in default. A safety
	// guard - the default encodes a real failure, so raise it with care.
	MaxCycles int `yaml:"max_cycles"`
	// MaxEmpties caps consecutive empty turns before the run bails. Zero uses the
	// built-in default.
	MaxEmpties int `yaml:"max_empties"`
	// LimitCheckpoints are the percentages of a bounded limit (iterations, calls,
	// time) at which the model is told it is approaching that limit, so it can
	// pace itself. Unset uses the built-in default (50, 80, 90); an explicit
	// empty list turns the notices off.
	LimitCheckpoints []int `yaml:"limit_checkpoints"`
	// ContextStrategy decides what happens when the conversation approaches the
	// model's context window: "compact" summarises the older history into a
	// checkpoint (an extra model call, higher fidelity), "truncate" simply drops
	// the oldest messages to fit. Empty uses the default, "compact".
	ContextStrategy string `yaml:"context_strategy"`
	// CompactMinTokens is the floor of estimated input tokens below which the
	// compact strategy does not bother summarising - a short conversation is
	// cheaper to carry whole than to summarise. Zero uses the built-in default.
	CompactMinTokens int `yaml:"compact_min_tokens"`
	// CompactMinMessages is the floor on how many messages must be eligible for
	// summarising before the compact strategy runs. Zero uses the default.
	CompactMinMessages int `yaml:"compact_min_messages"`
	// CompactTriggerRatio is the fraction of the context window at which the
	// compact strategy fires (0.9 = compact once the estimate reaches 90% of the
	// window). Zero uses the default. Must be within (0, 1].
	CompactTriggerRatio float64 `yaml:"compact_trigger_ratio"`
	// Instructions optionally overrides the built-in system prompt. Leave
	// empty to use zot.DefaultInstructions.
	Instructions string `yaml:"instructions"`
}

// MaxDuration parses Agent.MaxTime into a duration. An empty value is zero
// (unbounded); a malformed value is an error so a typo in the config is caught
// at load rather than silently ignored.
func (a Agent) MaxDuration() (time.Duration, error) {
	value := strings.TrimSpace(a.MaxTime)
	if value == "" {
		return 0, nil
	}

	d, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("%q is not a duration (use forms like \"30m\", \"2h\", \"90s\")", a.MaxTime)
	}

	if d < 0 {
		return 0, fmt.Errorf("%q is negative", a.MaxTime)
	}

	return d, nil
}

// Defaults returns the built-in configuration used when nothing else is set.
//
// zot talks to model providers directly: export the provider's key and it runs,
// with no account anywhere else.
//
// @note the default model and provider have to agree - glm-5.2 is served
// natively by Z.AI, so that is the provider. A default pair that cannot
// actually talk to each other is worse than no default, because the failure
// arrives as a provider error rather than as a configuration one.
func Defaults() Config {
	return Config{
		Agent: Agent{
			Model:           "glm-5.2",
			MaxIterations:   1_000_000,
			ContextStrategy: agent.StrategyCompact,
		},
		UI:              UI{Color: "auto"},
		DefaultProvider: "zai",
	}
}

// Load resolves the configuration: defaults, then the YAML file (if present),
// then environment overrides. A missing file at the default path is fine -
// env vars alone can configure zot; a bad explicit --config file is an error.
func Load(path string) (Config, error) {
	cfg := Defaults()

	explicit := path != ""
	if path == "" {
		path = DefaultConfigPath()
	}

	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		decoder := yaml.NewDecoder(bytes.NewReader(data))
		decoder.KnownFields(true)
		if err := decoder.Decode(&cfg); err != nil {
			return cfg, fmt.Errorf("parse %s: %w", path, err)
		}
	case os.IsNotExist(err) && !explicit:
		// No default config file: rely on defaults + env.
	default:
		return cfg, fmt.Errorf("read %s: %w", path, err)
	}

	if err := applyEnv(&cfg); err != nil {
		return cfg, err
	}

	// A portable build carries a configuration compiled into the binary. It is
	// applied last - after the file and the environment - so the values an
	// operator baked in are authoritative: the whole point of a portable build
	// is that the runtime environment cannot redirect it. Fields the overlay
	// leaves unset fall through to the file, env and defaults, so a key can still
	// come from the environment if it was deliberately not baked in.
	if err := applyPortableOverlay(&cfg, portableConfig()); err != nil {
		return cfg, err
	}

	resolveProviders(&cfg)

	if cfg.DefaultProvider == "" {
		cfg.DefaultProvider = "zai"
	}

	return cfg, nil
}

// applyPortableOverlay merges a compiled-in configuration layer onto cfg,
// overriding only the fields the document actually sets - an omitted field
// leaves whatever the file, env or defaults resolved. Empty data is a no-op, so
// a standard build (which carries none) passes straight through.
func applyPortableOverlay(cfg *Config, data []byte) error {
	if len(data) == 0 {
		return nil
	}

	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(cfg); err != nil {
		return fmt.Errorf("parse compiled-in config: %w", err)
	}

	return nil
}

// Portable reports whether this binary carries a compiled-in configuration - a
// portable build (see portableConfig). Surfaced on `zot --version` so "why is it
// ignoring my config file" is answerable without reading the source.
func Portable() bool {
	return len(portableConfig()) > 0
}

// resolveProviders ensures the built-in providers exist, fills their default
// endpoint, and resolves every credential (config "$ENV" reference first, then
// the built-in environment fallback) - the provider-level key and each model's
// own key.
func resolveProviders(cfg *Config) {
	if cfg.Providers == nil {
		cfg.Providers = map[string]ProviderConfig{}
	}

	for name := range builtinProviders {
		if _, ok := cfg.Providers[name]; !ok {
			cfg.Providers[name] = ProviderConfig{}
		}
	}

	for name, p := range cfg.Providers {
		builtin, isBuiltin := builtinProviders[name]
		if p.BaseURL == "" && isBuiltin {
			p.BaseURL = builtin.baseURL
		}

		// The credential, in whichever spelling it was written. Every one is
		// resolved: `api_key` is the documented spelling, so a `$VAR` reference
		// left unexpanded there would send the literal string "$MY_KEY" to the
		// provider and come back as a 401 that reads like a bad key.
		p.APIKey = resolveSecret(p.APIKey)

		// A built-in provider with nothing configured falls back to its
		// provider's conventional variable, which is what makes `export
		// OPENAI_API_KEY=…` enough on its own.
		if ProviderCredential(p) == "" && isBuiltin && builtin.secretEnv != "" {
			p.APIKey = strings.TrimSpace(os.Getenv(builtin.secretEnv))
		}

		// Per-model authorization.
		for mName, mc := range p.Models {
			if mc.APIKey != "" {
				mc.APIKey = resolveSecret(mc.APIKey)
				p.Models[mName] = mc
			}
		}

		cfg.Providers[name] = p
	}
}

// resolveSecret expands a "$ENV_VAR" / "${ENV_VAR}" reference; a literal value
// is returned unchanged.
//
// An unset variable resolves to empty rather than to its own name, so a missing
// credential is reported as a missing credential instead of being sent to the
// provider as the literal text "$MY_KEY".
func resolveSecret(v string) string {
	v = strings.TrimSpace(v)

	if v == "" {
		return ""
	}

	if strings.HasPrefix(v, "$") {
		name := strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(v, "$"), "{"), "}")
		return strings.TrimSpace(os.Getenv(strings.TrimSpace(name)))
	}
	return v
}

// ScrubProviderSecrets removes every resolved provider credential, both
// provider-level and per-model, from the process environment. Config retains
// the resolved values used by the SDK client, while shell commands launched by
// the agent no longer inherit those credentials.
func ScrubProviderSecrets(cfg Config) {
	secrets := map[string]bool{}
	add := func(v string) {
		if v != "" {
			secrets[v] = true
		}
	}
	for _, provider := range cfg.Providers {
		add(provider.APIKey)
		for _, mc := range provider.Models {
			add(mc.APIKey)
		}
	}
	if len(secrets) == 0 {
		return
	}

	for _, entry := range os.Environ() {
		name, value, ok := strings.Cut(entry, "=")
		if ok && secrets[value] {
			_ = os.Unsetenv(name)
		}
	}
}

// Validate checks the fully-merged configuration.
func (c Config) Validate() error {
	if strings.TrimSpace(c.Agent.Model) == "" {
		return fmt.Errorf("agent.model must be set")
	}
	if c.Agent.MaxIterations <= 0 {
		return fmt.Errorf("agent.max_iterations must be a positive number")
	}
	if _, err := c.Agent.MaxDuration(); err != nil {
		return fmt.Errorf("agent.max_time: %w", err)
	}
	for _, p := range c.Agent.LimitCheckpoints {
		if p < 1 || p > 99 {
			return fmt.Errorf("agent.limit_checkpoints: %d is out of range (each must be 1-99)", p)
		}
	}
	switch c.Agent.ContextStrategy {
	case "", agent.StrategyCompact, agent.StrategyTruncate:
	default:
		return fmt.Errorf("agent.context_strategy: %q is not valid (use %q or %q)",
			c.Agent.ContextStrategy, agent.StrategyCompact, agent.StrategyTruncate)
	}
	if r := c.Agent.CompactTriggerRatio; r != 0 && (r <= 0 || r > 1) {
		return fmt.Errorf("agent.compact_trigger_ratio: %g is out of range (must be within (0, 1])", r)
	}
	if c.Agent.CompactMinTokens < 0 || c.Agent.CompactMinMessages < 0 {
		return fmt.Errorf("agent.compact_min_tokens / compact_min_messages must not be negative")
	}
	if c.UI.Scrollback < 0 {
		return fmt.Errorf("ui.scrollback must not be negative")
	}
	switch strings.ToLower(strings.TrimSpace(c.UI.Color)) {
	case "", "auto", "always", "never":
	default:
		return fmt.Errorf("ui.color: %q is not valid (use auto, always, or never)", c.UI.Color)
	}
	for _, s := range c.UI.Stats {
		if !tui.IsKnownStat(s) {
			return fmt.Errorf("ui.stats: %q is not a known field (valid: %s)",
				s, strings.Join(tui.KnownStats, ", "))
		}
	}
	if _, ok := c.Providers[c.DefaultProvider]; !ok {
		return fmt.Errorf("default provider %q is not configured", c.DefaultProvider)
	}
	if provider := c.Providers[c.DefaultProvider]; len(provider.Models) > 0 {
		if _, ok := provider.Models[c.Agent.Model]; !ok {
			return fmt.Errorf("model %q is not configured for provider %q (available: %s)",
				c.Agent.Model, c.DefaultProvider, strings.Join(ProviderModels(c.DefaultProvider, provider), ", "))
		}
	}
	for name, provider := range c.Providers {
		// A provider either names a driver zot knows how to reach, or supplies
		// its own endpoint. Neither means there is nowhere to send the request,
		// and finding that out mid-run is worse than at load.
		if provider.BaseURL != "" {
			continue
		}

		driver := ProviderDriver(name, provider)

		if !slices.Contains(agent.Providers(), driver) {
			return fmt.Errorf(
				"providers.%s: driver %q is not known and no base_url is set (known: %s)",
				name, driver, strings.Join(agent.Providers(), ", "))
		}
	}
	return nil
}
