// Package config loads zot's configuration, layering built-in defaults, an
// optional YAML file, and environment variables (defaults < file < env).
package config

import (
	"fmt"
	"os"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/chatbotkit/zot/agent"
)

// Config is the fully-resolved zot configuration.
type Config struct {
	Agent Agent `yaml:"agent"`
	UI    UI    `yaml:"ui"`
	// Skills discovered from the context directories. Not configured directly:
	// LoadProjectContext fills this from the SKILL.md files it finds, and the
	// engine describes them in the system prompt.
	Skills []agent.SkillDefinition `yaml:"-"`
	// DefaultBackend is the backend used when --backend is not given.
	DefaultBackend string `yaml:"default_backend"`
	// Backends are the named providers a run can target. zot ships with three -
	// a backend for each provider it knows - and a config file can override
	// their credentials or endpoint, or add custom model entries.
	Backends map[string]Backend `yaml:"backends"`
}

// Backend is a provider zot can run against. Every provider authenticates with a
// Bearer credential.
type Backend struct {
	// Provider names the model provider this backend talks to: "openai",
	// "anthropic", "groq", "ollama" and so on. Empty infers it from the
	// backend's own name, so a backend called "groq" needs no further
	// configuration.
	//
	// zot speaks the OpenAI-compatible chat-completions API to every provider, so
	// a backend is a URL and a credential rather than a protocol of its own.
	Provider string `yaml:"provider"`
	// BaseURL overrides the API endpoint. Empty uses the built-in default.
	BaseURL string `yaml:"base_url"`
	// APISecret is an older spelling of APIKey, kept so an existing config still
	// loads. Supports "$ENV_VAR" references.
	APISecret string `yaml:"api_secret"`
	// Authorization is another older spelling of APIKey, kept for the same
	// reason. Supports "$ENV_VAR".
	Authorization string `yaml:"authorization"`
	// APIKey is the provider credential. Preferred spelling; APISecret and
	// Authorization are accepted as equivalents.
	APIKey string `yaml:"api_key"`
	// Models holds custom, named model configurations for this backend. When a
	// run's model name matches a key here, that entry's settings take priority.
	Models map[string]ModelConfig `yaml:"models"`
}

// ModelConfig is a custom model definition under a backend. Any field set here
// overrides the run's defaults when the model is selected.
type ModelConfig struct {
	// Provider overrides the backend's provider for this model, so one backend
	// entry can front several providers.
	Provider string `yaml:"provider"`
	// Model is the underlying model id to send. Lets a custom name alias a real
	// model; leave empty to use the selected name as-is.
	Model string `yaml:"model"`
	// MaxIterations overrides the global iteration cap for this model.
	MaxIterations int `yaml:"max_iterations"`
	// Authorization is this model's own credential, overriding the backend's.
	// Supports "$ENV_VAR".
	Authorization string `yaml:"authorization"`
}

// builtinBackends are the providers zot ships with. Each falls back to its
// provider's conventional environment variable, so exporting that is the whole
// setup. The endpoint is left empty where the provider package already knows it.
var builtinBackends = map[string]struct {
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
}

// BackendProvider resolves which model provider a backend addresses.
//
// A backend named after a provider is that provider, so the common case needs no
// configuration at all. Anything else has to say what it is: zot speaks to model
// providers directly, and a backend that names no provider has no endpoint to
// call.
func BackendProvider(name string, backend Backend) string {
	if backend.Provider != "" {
		return backend.Provider
	}

	return name
}

// BackendCredential returns the credential configured for a backend, whichever
// field it was written in.
//
// `api_key` is the spelling to use. `api_secret` and `authorization` are
// accepted so a config written for an earlier version still loads - they meant
// the same thing to the hosted backends that have since been removed.
func BackendCredential(backend Backend) string {
	for _, candidate := range []string{backend.APIKey, backend.APISecret, backend.Authorization} {
		if candidate != "" {
			return candidate
		}
	}

	return ""
}

// UI holds presentation options for the read-only viewer.
type UI struct {
	// Diff, when true, renders a framed, syntax-highlighted before/after diff
	// panel beneath every edit/write the agent makes.
	Diff bool `yaml:"diff"`
	// Plain forces the unstyled streaming renderer (no full-screen TUI). It is
	// also used automatically when stdout is not a terminal.
	Plain bool `yaml:"plain"`
}

// Agent holds the knobs that shape an autonomous run.
type Agent struct {
	// Model is the model name driving the agent.
	Model string `yaml:"model"`
	// MaxIterations caps how many plan/act/observe cycles the agent may run
	// before it is forced to stop.
	MaxIterations int `yaml:"max_iterations"`
	// Backstory optionally overrides the built-in system instruction. Leave
	// empty to use zot.DefaultBackstory.
	Backstory string `yaml:"backstory"`
}

// Defaults returns the built-in configuration used when nothing else is set.
//
// zot talks to model providers directly, so the default backend is a provider
// rather than a gateway: export the provider's key and it runs, with no account
// anywhere else.
//
// @note the default model and the default backend have to agree - glm-5.2 is
// served natively by Z.AI, so that is the backend. A default pair that cannot
// actually talk to each other is worse than no default, because the failure
// arrives as a provider error rather than as a configuration one.
func Defaults() Config {
	return Config{
		Agent: Agent{
			Model:         "glm-5.2",
			MaxIterations: 1_000_000,
		},
		DefaultBackend: "zai",
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
		if err := yaml.Unmarshal(data, &cfg); err != nil {
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

	resolveBackends(&cfg)

	if cfg.DefaultBackend == "" {
		cfg.DefaultBackend = "zai"
	}

	return cfg, nil
}

// resolveBackends ensures the built-in backends exist, fills their default
// endpoint, and resolves every credential (config "$ENV" reference first, then
// the built-in environment fallback) - the Bearer secret, the backend-level
// model authorization, and each model's own authorization.
func resolveBackends(cfg *Config) {
	if cfg.Backends == nil {
		cfg.Backends = map[string]Backend{}
	}

	for name := range builtinBackends {
		if _, ok := cfg.Backends[name]; !ok {
			cfg.Backends[name] = Backend{}
		}
	}

	for name, b := range cfg.Backends {
		builtin, isBuiltin := builtinBackends[name]
		if b.BaseURL == "" && isBuiltin {
			b.BaseURL = builtin.baseURL
		}

		// The credential, in whichever spelling it was written. Every one is
		// resolved: `api_key` is the documented spelling, so a `$VAR` reference
		// left unexpanded there would send the literal string "$MY_KEY" to the
		// provider and come back as a 401 that reads like a bad key.
		b.APIKey = resolveSecret(b.APIKey)
		b.APISecret = resolveSecret(b.APISecret)
		b.Authorization = resolveSecret(b.Authorization)

		// A built-in backend with nothing configured falls back to its
		// provider's conventional variable, which is what makes `export
		// OPENAI_API_KEY=…` enough on its own.
		if BackendCredential(b) == "" && isBuiltin && builtin.secretEnv != "" {
			b.APIKey = strings.TrimSpace(os.Getenv(builtin.secretEnv))
		}

		// Per-model authorization.
		for mName, mc := range b.Models {
			if mc.Authorization != "" {
				mc.Authorization = resolveSecret(mc.Authorization)
				b.Models[mName] = mc
			}
		}

		cfg.Backends[name] = b
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

// ScrubBackendSecrets removes every resolved backend credential - Bearer
// secrets and provider authorizations, backend-level and per-model - from the
// process environment. Config retains the resolved values used by the SDK
// client, while shell commands launched by the agent no longer inherit those
// credentials.
func ScrubBackendSecrets(cfg Config) {
	secrets := map[string]bool{}
	add := func(v string) {
		if v != "" {
			secrets[v] = true
		}
	}
	for _, backend := range cfg.Backends {
		add(backend.APISecret)
		add(backend.Authorization)
		for _, mc := range backend.Models {
			add(mc.Authorization)
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
	if _, ok := c.Backends[c.DefaultBackend]; !ok {
		return fmt.Errorf("default backend %q is not configured", c.DefaultBackend)
	}
	for name, backend := range c.Backends {
		// A backend either names a provider zot knows how to reach, or supplies
		// its own endpoint. Neither means there is nowhere to send the request,
		// and finding that out mid-run is worse than at load.
		if backend.BaseURL != "" {
			continue
		}

		provider := BackendProvider(name, backend)

		if !slices.Contains(agent.Providers(), provider) {
			return fmt.Errorf(
				"backends.%s: %q is not a known provider and no base_url is set (known: %s)",
				name, provider, strings.Join(agent.Providers(), ", "))
		}
	}
	return nil
}
