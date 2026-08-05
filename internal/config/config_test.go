package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

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
	if c.DefaultBackend != "relay" {
		t.Errorf("default backend = %q, want relay", c.DefaultBackend)
	}
}

// The three built-in backends are seeded with their endpoints and each resolves
// its own credential: the relay's provider key from RELAY_API_KEY (its
// backend-level authorization), and the Bearer secrets from their own variables.
func TestLoadSeedsBackends(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("ZOT_CONFIG", "")
	t.Setenv("RELAY_API_KEY", "relay-key")
	t.Setenv("CBK_API_SECRET", "cbk-secret")
	t.Setenv("CHATBOTKIT_API_SECRET", "chatbotkit-secret")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DefaultBackend != "relay" {
		t.Errorf("default backend = %q, want relay", cfg.DefaultBackend)
	}

	relay := cfg.Backends["relay"]
	if relay.BaseURL != "https://relay.cbk.ai" {
		t.Errorf("relay base_url = %q, want https://relay.cbk.ai", relay.BaseURL)
	}
	if relay.Authorization != "relay-key" {
		t.Errorf("relay authorization = %q, want it from RELAY_API_KEY", relay.Authorization)
	}
	// The relay uses no Bearer secret; its credential rides in the model string.
	if relay.APISecret != "" {
		t.Errorf("relay APISecret = %q, want empty", relay.APISecret)
	}

	cases := map[string]struct{ url, secret string }{
		"cbk":        {"https://api.cbk.ai", "cbk-secret"},
		"chatbotkit": {"https://api.chatbotkit.com", "chatbotkit-secret"},
	}
	for name, want := range cases {
		b := cfg.Backends[name]
		if b.BaseURL != want.url {
			t.Errorf("%s base_url = %q, want %q", name, b.BaseURL, want.url)
		}
		if b.APISecret != want.secret {
			t.Errorf("%s secret = %q, want %q", name, b.APISecret, want.secret)
		}
	}
}

// Env vars override the file (defaults < file < env). CLI flags override env,
// but that layer lives in main.
func TestLoadEnvOverrides(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("ZOT_CONFIG", "")
	t.Setenv("CBK_API_SECRET", "sk-test")
	path := writeConfig(t, `
agent:
  model: from-file
default_backend: relay
`)
	t.Setenv("ZOT_AGENT_MODEL", "gpt-4o")
	t.Setenv("ZOT_AGENT_MAX_ITERATIONS", "12")
	t.Setenv("ZOT_DEFAULT_BACKEND", "cbk")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Agent.Model != "gpt-4o" {
		t.Errorf("model = %q, want gpt-4o (env overrides file)", cfg.Agent.Model)
	}
	if cfg.Agent.MaxIterations != 12 {
		t.Errorf("max_iterations = %d, want 12", cfg.Agent.MaxIterations)
	}
	if cfg.DefaultBackend != "cbk" {
		t.Errorf("default backend = %q, want cbk (env overrides file)", cfg.DefaultBackend)
	}
	if cfg.Backends["cbk"].APISecret != "sk-test" {
		t.Errorf("cbk secret = %q, want it from CBK_API_SECRET", cfg.Backends["cbk"].APISecret)
	}
}

func TestLoadExplicitMissingIsError(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "nope.yaml")); err == nil {
		t.Error("expected an error for a missing explicit --config file")
	}
}

// api_secret in the file may name an env var with $VAR.
func TestSecretEnvReference(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("MY_CBK_KEY", "sk-from-env")
	path := writeConfig(t, `
default_backend: cbk
backends:
  cbk:
    api_secret: '$MY_CBK_KEY'
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Backends["cbk"].APISecret; got != "sk-from-env" {
		t.Errorf("resolved secret = %q, want sk-from-env", got)
	}
}

// Backend-level and per-model authorization in the file may name env vars with
// $VAR, and each is resolved.
func TestAuthorizationEnvReference(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("RELAY_DEFAULT_KEY", "sk-relay-default")
	t.Setenv("OPENAI_API_KEY", "sk-openai")
	path := writeConfig(t, `
default_backend: relay
backends:
  relay:
    authorization: '$RELAY_DEFAULT_KEY'
    models:
      gpt-4:
        authorization: $OPENAI_API_KEY
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Backends["relay"].Authorization; got != "sk-relay-default" {
		t.Errorf("backend authorization = %q, want sk-relay-default", got)
	}
	if got := cfg.Backends["relay"].Models["gpt-4"].Authorization; got != "sk-openai" {
		t.Errorf("model authorization = %q, want sk-openai", got)
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

// Scrubbing removes every resolved credential - Bearer secrets and provider
// authorizations, backend-level and per-model - from the environment, while
// leaving unrelated variables intact.
func TestScrubBackendSecrets(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("RELAY_API_KEY", "relay-default")
	t.Setenv("OPENAI_API_KEY", "sk-openai")
	t.Setenv("ZOT_TEST_UNRELATED", "keep-me")
	path := writeConfig(t, `
default_backend: relay
backends:
  relay:
    models:
      gpt-4:
        authorization: $OPENAI_API_KEY
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	ScrubBackendSecrets(cfg)

	if _, ok := os.LookupEnv("RELAY_API_KEY"); ok {
		t.Error("RELAY_API_KEY should be removed after scrub")
	}
	if _, ok := os.LookupEnv("OPENAI_API_KEY"); ok {
		t.Error("OPENAI_API_KEY should be removed after scrub")
	}
	if got := os.Getenv("ZOT_TEST_UNRELATED"); got != "keep-me" {
		t.Errorf("ZOT_TEST_UNRELATED = %q, want keep-me", got)
	}
}
