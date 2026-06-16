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
	Agent      Agent      `yaml:"agent"`
	ChatBotKit ChatBotKit `yaml:"chatbotkit"`
	UI         UI         `yaml:"ui"`
	// Features are ChatBotKit conversation features to enable for the run, each a
	// name/options pair passed through to the agent.
	Features []Feature `yaml:"features"`
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
	// Model is the ChatBotKit model alias driving the agent.
	Model string `yaml:"model"`
	// MaxIterations caps how many plan/act/observe cycles the agent may run
	// before it is forced to stop.
	MaxIterations int `yaml:"max_iterations"`
	// Backstory optionally overrides the built-in system instruction. Leave
	// empty to use zot.DefaultBackstory.
	Backstory string `yaml:"backstory"`
}

// ChatBotKit holds the API credentials and endpoint.
type ChatBotKit struct {
	// APISecret is the ChatBotKit API token. Normally supplied via the
	// CHATBOTKIT_API_SECRET environment variable rather than the config file.
	APISecret string `yaml:"api_secret"`
	// BaseURL optionally overrides the API endpoint (handy for staging).
	BaseURL string `yaml:"base_url"`
}

// Defaults returns the built-in configuration used when nothing else is set.
func Defaults() Config {
	return Config{
		Agent: Agent{
			Model:         "kimi-k2.7-code",
			MaxIterations: 1000,
		},
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

	// Honour the canonical ChatBotKit env vars used across the platform, so
	// credentials don't need the ZOT_ prefix.
	if cfg.ChatBotKit.APISecret == "" {
		cfg.ChatBotKit.APISecret = strings.TrimSpace(os.Getenv("CHATBOTKIT_API_SECRET"))
	}
	if cfg.ChatBotKit.BaseURL == "" {
		cfg.ChatBotKit.BaseURL = strings.TrimSpace(os.Getenv("CHATBOTKIT_HOST"))
	}

	return cfg, nil
}

// Validate checks the fully-merged configuration.
func (c Config) Validate() error {
	if strings.TrimSpace(c.Agent.Model) == "" {
		return fmt.Errorf("agent.model must be set")
	}
	if c.Agent.MaxIterations <= 0 {
		return fmt.Errorf("agent.max_iterations must be a positive number")
	}
	for _, f := range c.Features {
		if strings.TrimSpace(f.Name) == "" {
			return fmt.Errorf("features: each feature needs a name")
		}
		if !featureAllowed(f.Name) {
			return fmt.Errorf("features: unknown feature %q (allowed: %s)", f.Name, strings.Join(AllowedFeatures, ", "))
		}
	}
	return nil
}
