package config

import (
	"path/filepath"
	"testing"
)

// validConfig returns a minimal config that passes Validate, optionally tweaked.
func validConfig(tweak func(*Config)) Config {
	c := Config{
		Agent:          Agent{Model: "m", MaxIterations: 1},
		DefaultBackend: "cbk",
		Backends:       map[string]Backend{"cbk": {APISecret: "x"}},
	}
	if tweak != nil {
		tweak(&c)
	}
	return c
}

func TestDefaults(t *testing.T) {
	c := Defaults()
	if c.Agent.Model == "" {
		t.Error("expected a default model")
	}
	if c.Agent.MaxIterations <= 0 {
		t.Error("expected a positive default max_iterations")
	}
	if c.DefaultBackend != "cbk" {
		t.Errorf("default backend = %q, want cbk", c.DefaultBackend)
	}
}

func TestLoadSeedsBackends(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("ZOT_CONFIG", "")
	t.Setenv("CHATBOTKIT_API_SECRET", "cbk-secret")
	t.Setenv("RELAY_API_KEY", "relay-key")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DefaultBackend != "cbk" {
		t.Errorf("default backend = %q, want cbk", cfg.DefaultBackend)
	}
	if got := cfg.Backends["cbk"].APISecret; got != "cbk-secret" {
		t.Errorf("cbk secret = %q, want it from CHATBOTKIT_API_SECRET", got)
	}
	relay := cfg.Backends["relay"]
	if relay.APISecret != "relay-key" {
		t.Errorf("relay secret = %q, want it from RELAY_API_KEY", relay.APISecret)
	}
	if relay.BaseURL != "https://relay.cbk.ai" {
		t.Errorf("relay base_url = %q, want the relay endpoint", relay.BaseURL)
	}
}

func TestLoadEnvOverrides(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("ZOT_CONFIG", "")
	t.Setenv("ZOT_AGENT_MODEL", "gpt-4o")
	t.Setenv("ZOT_AGENT_MAX_ITERATIONS", "12")
	t.Setenv("CHATBOTKIT_API_SECRET", "sk-test")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Agent.Model != "gpt-4o" {
		t.Errorf("model = %q, want gpt-4o", cfg.Agent.Model)
	}
	if cfg.Agent.MaxIterations != 12 {
		t.Errorf("max_iterations = %d, want 12", cfg.Agent.MaxIterations)
	}
	if cfg.Backends["cbk"].APISecret != "sk-test" {
		t.Errorf("cbk secret = %q, want it from CHATBOTKIT_API_SECRET", cfg.Backends["cbk"].APISecret)
	}
}

func TestLoadExplicitMissingIsError(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "nope.yaml")); err == nil {
		t.Error("expected an error for a missing explicit --config file")
	}
}

func TestValidate(t *testing.T) {
	if err := validConfig(nil).Validate(); err != nil {
		t.Errorf("unexpected error for a valid config: %v", err)
	}
	if err := validConfig(func(c *Config) { c.Agent.Model = "" }).Validate(); err == nil {
		t.Error("expected an error for an empty model")
	}
	if err := validConfig(func(c *Config) { c.Agent.MaxIterations = 0 }).Validate(); err == nil {
		t.Error("expected an error for non-positive max_iterations")
	}
	if err := validConfig(func(c *Config) { c.DefaultBackend = "nope" }).Validate(); err == nil {
		t.Error("expected an error for an unknown default backend")
	}
}

func TestValidateFeatures(t *testing.T) {
	if err := validConfig(func(c *Config) {
		c.Features = []Feature{{Name: "web"}, {Name: "chunking"}}
	}).Validate(); err != nil {
		t.Errorf("unexpected error for allowed features: %v", err)
	}
	if err := validConfig(func(c *Config) {
		c.Features = []Feature{{Name: "bash"}}
	}).Validate(); err == nil {
		t.Error("expected an error for a feature outside the allow-list")
	}
	if err := validConfig(func(c *Config) {
		c.Backends["cbk"] = Backend{APISecret: "x", Models: map[string]ModelConfig{
			"custom": {Features: []Feature{{Name: "bash"}}},
		}}
	}).Validate(); err == nil {
		t.Error("expected an error for a disallowed per-model feature")
	}
}
