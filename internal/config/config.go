// Package config loads zot's configuration, layering built-in defaults, an
// optional YAML file, and environment variables (defaults < file < env).
package config

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config is the fully-resolved zot configuration.
type Config struct {
	Agent Agent `yaml:"agent"`
	UI    UI    `yaml:"ui"`
	// Features are ChatBotKit conversation features to enable for the run, each a
	// name/options pair passed through to the agent.
	Features []Feature `yaml:"features"`
	// DefaultBackend is the backend used when --backend is not given.
	DefaultBackend string `yaml:"default_backend"`
	// Backends are the named providers a run can target. zot ships with three -
	// "relay" (CBK Relay, the default), "cbk" (ChatBotKit at api.cbk.ai) and
	// "chatbotkit" (ChatBotKit at api.chatbotkit.com) - and a config file can
	// override their credentials/endpoint or add custom model entries.
	Backends map[string]Backend `yaml:"backends"`
}

// Backend is a provider zot can run against. How a run authenticates depends on
// the backend's style: the ChatBotKit backends send a Bearer credential
// (APISecret); the relay carries the provider credential per model, inside the
// model string (Authorization), because on the relay each model is a different
// provider with its own key.
type Backend struct {
	// BaseURL overrides the API endpoint. Empty uses the built-in default.
	BaseURL string `yaml:"base_url"`
	// APISecret is the Bearer credential for the ChatBotKit backends. Supports
	// "$ENV_VAR" references; for the built-ins it defaults from the environment.
	APISecret string `yaml:"api_secret"`
	// Authorization is the relay's default model credential, applied to every
	// model on this backend that does not set its own. Supports "$ENV_VAR" (point
	// it at your own provider key). It has no built-in environment default.
	// Ignored by the Bearer backends.
	Authorization string `yaml:"authorization"`
	// Models holds custom, named model configurations for this backend. When a
	// run's model name matches a key here, that entry's settings take priority.
	Models map[string]ModelConfig `yaml:"models"`
}

// ModelConfig is a custom model definition under a backend. Any field set here
// overrides the run's defaults when the model is selected.
type ModelConfig struct {
	// Model is the underlying model id to send. Lets a custom name alias a real
	// model; leave empty to use the selected name as-is.
	Model string `yaml:"model"`
	// MaxIterations overrides the global iteration cap for this model.
	MaxIterations int `yaml:"max_iterations"`
	// Authorization is this model's provider credential on the relay, composed
	// into the model string as "<model>/authorization=<value>". Supports
	// "$ENV_VAR". Overrides the backend-level Authorization. Ignored by the
	// Bearer backends.
	Authorization string `yaml:"authorization"`
	// Features are extra conversation features enabled for this model.
	Features []Feature `yaml:"features"`
}

// AuthStyle is how a backend authenticates a run.
type AuthStyle int

const (
	// AuthBearer sends the credential as an Authorization: Bearer header.
	AuthBearer AuthStyle = iota
	// AuthModelParam carries the credential inside the model string, as
	// "<model>/authorization=<value>" - the CBK Relay's convention, where each
	// model is a distinct provider authenticated with its own key.
	AuthModelParam
)

// builtinBackends are the providers zot ships with: their default endpoint and
// how they authenticate. The Bearer backends fall back to a brand-named
// environment variable for their credential; the relay has no such default -
// its per-model provider credential comes from config (or is inlined into the
// model). "cbk" and "chatbotkit" are the same platform on its two hosts and
// take the same credential value under their own variable.
var builtinBackends = map[string]struct {
	baseURL   string
	style     AuthStyle
	secretEnv string // Bearer credential fallback (AuthBearer)
}{
	"relay":      {baseURL: "https://relay.cbk.ai", style: AuthModelParam},
	"cbk":        {baseURL: "https://api.cbk.ai", style: AuthBearer, secretEnv: "CBK_API_SECRET"},
	"chatbotkit": {baseURL: "https://api.chatbotkit.com", style: AuthBearer, secretEnv: "CHATBOTKIT_API_SECRET"},
}

// BackendStyle reports how the named backend authenticates. Unknown/custom
// backends default to Bearer.
func BackendStyle(name string) AuthStyle {
	if b, ok := builtinBackends[name]; ok {
		return b.style
	}
	return AuthBearer
}

// SecretEnvName returns the environment variable a built-in Bearer backend's
// credential falls back to, for use in actionable error messages.
func SecretEnvName(backend string) string {
	if b, ok := builtinBackends[backend]; ok && b.secretEnv != "" {
		return b.secretEnv
	}
	return "its credential"
}

// Feature is a ChatBotKit conversation feature enabled for the run: a name plus
// optional, feature-specific options.
type Feature struct {
	Name    string                 `yaml:"name"`
	Options map[string]interface{} `yaml:"options"`
}

// AllowedFeatures is the set of feature names zot currently exposes.
var AllowedFeatures = []string{"web", "chunking"}

func featureAllowed(name string) bool {
	for _, a := range AllowedFeatures {
		if a == name {
			return true
		}
	}
	return false
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
// zot defaults to the CBK Relay: a run brings its own provider key and reaches
// models through relay.cbk.ai.
func Defaults() Config {
	return Config{
		Agent: Agent{
			Model:         "glm-5.2",
			MaxIterations: 1000,
		},
		DefaultBackend: "relay",
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
		cfg.DefaultBackend = "relay"
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

		// Bearer credential (AuthBearer backends).
		if s := strings.TrimSpace(b.APISecret); s != "" {
			b.APISecret = resolveSecret(s)
		} else if isBuiltin && builtin.secretEnv != "" {
			b.APISecret = strings.TrimSpace(os.Getenv(builtin.secretEnv))
		}

		// Backend-level model authorization (AuthModelParam backends). No
		// environment default - the relay's provider credential comes from config
		// (or is inlined into the model).
		if a := strings.TrimSpace(b.Authorization); a != "" {
			b.Authorization = resolveSecret(a)
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
func resolveSecret(v string) string {
	v = strings.TrimSpace(v)
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
	if err := validateFeatures(c.Features); err != nil {
		return err
	}
	for name, b := range c.Backends {
		for model, mc := range b.Models {
			if err := validateFeatures(mc.Features); err != nil {
				return fmt.Errorf("backends.%s.models.%s: %w", name, model, err)
			}
		}
	}
	return nil
}

func validateFeatures(features []Feature) error {
	for _, f := range features {
		if strings.TrimSpace(f.Name) == "" {
			return fmt.Errorf("features: each feature needs a name")
		}
		if !featureAllowed(f.Name) {
			return fmt.Errorf("features: unknown feature %q (allowed: %s)", f.Name, strings.Join(AllowedFeatures, ", "))
		}
	}
	return nil
}
