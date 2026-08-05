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
		DefaultBackend: "openai",
		Backends:       map[string]Backend{"openai": {APIKey: "x"}},
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
	if c.DefaultBackend != "zai" {
		t.Errorf("default backend = %q, want zai", c.DefaultBackend)
	}
}

// The built-in backends are seeded for every provider zot knows, each reading
// its provider's conventional credential variable.
func TestLoadSeedsBackends(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("ZOT_CONFIG", "")
	t.Setenv("OPENAI_API_KEY", "sk-openai")
	t.Setenv("ANTHROPIC_API_KEY", "sk-anthropic")
	t.Setenv("GROQ_API_KEY", "sk-groq")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.DefaultBackend != "zai" {
		t.Errorf("default backend = %q, want zai", cfg.DefaultBackend)
	}

	for name, want := range map[string]string{
		"openai":    "sk-openai",
		"anthropic": "sk-anthropic",
		"groq":      "sk-groq",
	} {
		backend, ok := cfg.Backends[name]

		if !ok {
			t.Fatalf("backend %q was not seeded", name)
		}

		if got := BackendCredential(backend); got != want {
			t.Errorf("%s credential = %q, want %q", name, got, want)
		}

		if got := BackendProvider(name, backend); got != name {
			t.Errorf("%s provider = %q, want %q", name, got, name)
		}
	}

	// a local provider needs no credential
	if got := BackendCredential(cfg.Backends["ollama"]); got != "" {
		t.Errorf("ollama credential = %q, want none", got)
	}
}

// The hosted backends are gone: zot reaches providers directly, so nothing named
// after the platform or the relay is seeded any more.
func TestHostedBackendsAreNotSeeded(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("ZOT_CONFIG", "")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	for _, name := range []string{"cbk", "chatbotkit", "relay"} {
		if _, ok := cfg.Backends[name]; ok {
			t.Errorf("backend %q should no longer be built in", name)
		}
	}
}

// Env vars override the file (defaults < file < env). CLI flags override env,
// but that layer lives in main.
func TestLoadEnvOverrides(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("ZOT_CONFIG", "")
	t.Setenv("GROQ_API_KEY", "sk-test")
	path := writeConfig(t, `
agent:
  model: from-file
default_backend: relay
`)
	t.Setenv("ZOT_AGENT_MODEL", "gpt-4o")
	t.Setenv("ZOT_AGENT_MAX_ITERATIONS", "12")
	t.Setenv("ZOT_DEFAULT_BACKEND", "groq")

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
	if cfg.DefaultBackend != "groq" {
		t.Errorf("default backend = %q, want groq (env overrides file)", cfg.DefaultBackend)
	}
	if got := BackendCredential(cfg.Backends["groq"]); got != "sk-test" {
		t.Errorf("groq credential = %q, want it from GROQ_API_KEY", got)
	}
}

func TestLoadExplicitMissingIsError(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "nope.yaml")); err == nil {
		t.Error("expected an error for a missing explicit --config file")
	}
}

// A credential in the file may name an env var with $VAR, so no secret has to be
// written to disk.
func TestSecretEnvReference(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("MY_PROVIDER_KEY", "sk-from-env")
	path := writeConfig(t, `
default_backend: openai
backends:
  openai:
    api_secret: '$MY_PROVIDER_KEY'
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Backends["openai"].APISecret; got != "sk-from-env" {
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

// Scrubbing removes every resolved credential - Bearer secrets and provider
// authorizations, backend-level and per-model - from the environment, while
// leaving unrelated variables intact.
func TestScrubBackendSecrets(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("ZAI_API_KEY", "sk-zai")
	t.Setenv("OPENAI_API_KEY", "sk-openai")
	t.Setenv("ZOT_TEST_UNRELATED", "keep-me")
	path := writeConfig(t, `
default_backend: relay
backends:
  relay:
    authorization: $ZAI_API_KEY
    models:
      gpt-4:
        authorization: $OPENAI_API_KEY
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	ScrubBackendSecrets(cfg)

	if _, ok := os.LookupEnv("ZAI_API_KEY"); ok {
		t.Error("ZAI_API_KEY should be removed after scrub")
	}
	if _, ok := os.LookupEnv("OPENAI_API_KEY"); ok {
		t.Error("OPENAI_API_KEY should be removed after scrub")
	}
	if got := os.Getenv("ZOT_TEST_UNRELATED"); got != "keep-me" {
		t.Errorf("ZOT_TEST_UNRELATED = %q, want keep-me", got)
	}
}

// The config path resolves through an explicit override, then XDG, then the
// home fallback - so a container that sets neither still lands somewhere real.
func TestConfigPathResolution(t *testing.T) {
	t.Run("an explicit ZOT_CONFIG wins", func(t *testing.T) {
		t.Setenv("ZOT_CONFIG", "/custom/zot.yaml")
		t.Setenv("XDG_CONFIG_HOME", "/xdg")

		if got := DefaultConfigPath(); got != "/custom/zot.yaml" {
			t.Errorf("DefaultConfigPath = %q", got)
		}
	})

	t.Run("then XDG_CONFIG_HOME", func(t *testing.T) {
		t.Setenv("ZOT_CONFIG", "")
		t.Setenv("XDG_CONFIG_HOME", "/xdg")

		if got := DefaultConfigPath(); got != "/xdg/zot/config.yaml" {
			t.Errorf("DefaultConfigPath = %q", got)
		}
	})

	t.Run("then the home directory", func(t *testing.T) {
		t.Setenv("ZOT_CONFIG", "")
		t.Setenv("XDG_CONFIG_HOME", "")
		t.Setenv("HOME", "/home/someone")

		if got := DefaultConfigPath(); got != "/home/someone/.config/zot/config.yaml" {
			t.Errorf("DefaultConfigPath = %q", got)
		}
	})

	t.Run("whitespace counts as unset", func(t *testing.T) {
		t.Setenv("ZOT_CONFIG", "   ")
		t.Setenv("XDG_CONFIG_HOME", "/xdg")

		if got := DefaultConfigPath(); got != "/xdg/zot/config.yaml" {
			t.Errorf("DefaultConfigPath = %q, want the blank override ignored", got)
		}
	})
}

// A container with no HOME still needs somewhere to look, or the binary cannot
// start at all.
func TestHomeDirHasAFallback(t *testing.T) {
	t.Setenv("HOME", "")

	if got := homeDir(); got == "" {
		t.Error("homeDir must always return something")
	}

	t.Setenv("HOME", "/home/real")

	if got := homeDir(); got != "/home/real" {
		t.Errorf("homeDir = %q", got)
	}
}

// ConfigDir is where a global AGENT.md and skills live, so it has to track
// whichever config path is in play.
func TestConfigDir(t *testing.T) {
	if got := ConfigDir("/somewhere/zot.yaml"); got != "/somewhere" {
		t.Errorf("ConfigDir = %q", got)
	}

	t.Setenv("ZOT_CONFIG", "/fallback/zot.yaml")

	if got := ConfigDir("   "); got != "/fallback" {
		t.Errorf("ConfigDir with a blank path = %q, want the default's directory", got)
	}
}

// Env overrides are typed, and a bad value has to be reported rather than
// silently zeroed - a max_iterations of 0 would end every run immediately.
func TestEnvOverrideTypeErrors(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("ZOT_CONFIG", "")
	t.Setenv("OPENAI_API_KEY", "k")

	t.Setenv("ZOT_AGENT_MAX_ITERATIONS", "not-a-number")

	if _, err := Load(""); err == nil {
		t.Error("a non-numeric max_iterations must be reported")
	}

	t.Setenv("ZOT_AGENT_MAX_ITERATIONS", "42")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Agent.MaxIterations != 42 {
		t.Errorf("max_iterations = %d, want 42", cfg.Agent.MaxIterations)
	}
}

func TestEnvOverrideBooleans(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("ZOT_CONFIG", "")
	t.Setenv("OPENAI_API_KEY", "k")
	t.Setenv("ZOT_UI_PLAIN", "true")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if !cfg.UI.Plain {
		t.Error("ui.plain should be set from ZOT_UI_PLAIN")
	}

	t.Setenv("ZOT_UI_PLAIN", "maybe")

	if _, err := Load(""); err == nil {
		t.Error("a non-boolean ui.plain must be reported")
	}
}

// A backend either names a provider zot can reach or brings its own endpoint.
// Neither means there is nowhere to send the request, and that is worth catching
// at load rather than mid-run.
func TestValidateRejectsAnUnreachableBackend(t *testing.T) {
	cfg := Defaults()
	cfg.DefaultBackend = "mygateway"

	// a name nobody knows, and no endpoint
	cfg.Backends = map[string]Backend{"mygateway": {}}

	if err := cfg.Validate(); err == nil {
		t.Error("a backend with no known provider and no base_url must be rejected")
	}

	// an endpoint of its own is enough
	cfg.Backends["mygateway"] = Backend{BaseURL: "https://gw.example.com/v1"}

	if err := cfg.Validate(); err != nil {
		t.Errorf("a backend with its own endpoint is valid: %v", err)
	}

	// or a provider zot knows
	cfg.Backends["mygateway"] = Backend{Provider: "openai"}

	if err := cfg.Validate(); err != nil {
		t.Errorf("a backend naming a known provider is valid: %v", err)
	}

	// a backend named after a provider needs nothing at all
	cfg.DefaultBackend = "groq"
	cfg.Backends = map[string]Backend{"groq": {}}

	if err := cfg.Validate(); err != nil {
		t.Errorf("a backend named after a provider is valid: %v", err)
	}
}

// A `$VAR` reference is the documented way to keep a key out of the config
// file, so it has to work in every spelling the credential can be written in.
// It silently did not for `api_key` - the documented one - which sent the
// literal text "$MY_KEY" to the provider and came back as a 401 that reads like
// a bad key rather than a config that never expanded.
func TestEverySpellingExpandsAnEnvReference(t *testing.T) {
	t.Setenv("ZOT_TEST_PROVIDER_KEY", "sk-resolved")

	tests := []struct {
		name    string
		backend Backend
	}{
		{name: "api_key", backend: Backend{APIKey: "$ZOT_TEST_PROVIDER_KEY"}},
		{name: "api_secret", backend: Backend{APISecret: "$ZOT_TEST_PROVIDER_KEY"}},
		{name: "authorization", backend: Backend{Authorization: "$ZOT_TEST_PROVIDER_KEY"}},
		{name: "braced", backend: Backend{APIKey: "${ZOT_TEST_PROVIDER_KEY}"}},
		{name: "padded", backend: Backend{APIKey: "  $ZOT_TEST_PROVIDER_KEY  "}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := Config{Backends: map[string]Backend{"mine": test.backend}}

			resolveBackends(&cfg)

			if got := BackendCredential(cfg.Backends["mine"]); got != "sk-resolved" {
				t.Errorf("credential = %q, want the expanded value", got)
			}
		})
	}
}

// An unset variable must resolve to nothing, so the run fails with "no API key
// configured" rather than sending the literal reference to the provider.
func TestAnUnsetEnvReferenceResolvesToNothing(t *testing.T) {
	t.Setenv("ZOT_TEST_UNSET_KEY", "")

	cfg := Config{Backends: map[string]Backend{
		"mine": {Provider: "custom", APIKey: "$ZOT_TEST_UNSET_KEY"},
	}}

	resolveBackends(&cfg)

	if got := BackendCredential(cfg.Backends["mine"]); got != "" {
		t.Errorf("credential = %q, want nothing", got)
	}
}

// A literal key is left exactly as written - a provider key that happens to
// contain a dollar sign is not a reference.
func TestALiteralCredentialIsUntouched(t *testing.T) {
	cfg := Config{Backends: map[string]Backend{
		"mine": {APIKey: "sk-literal-with-$-inside"},
	}}

	resolveBackends(&cfg)

	if got := BackendCredential(cfg.Backends["mine"]); got != "sk-literal-with-$-inside" {
		t.Errorf("credential = %q, want it untouched", got)
	}
}

// Exporting the provider's conventional variable is enough on its own, and must
// not override a key the config states explicitly.
func TestTheConventionalVariableIsOnlyAFallback(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-from-env")

	cfg := Config{Backends: map[string]Backend{"openai": {}}}

	resolveBackends(&cfg)

	if got := BackendCredential(cfg.Backends["openai"]); got != "sk-from-env" {
		t.Errorf("credential = %q, want the conventional variable", got)
	}

	cfg = Config{Backends: map[string]Backend{"openai": {APIKey: "sk-from-config"}}}

	resolveBackends(&cfg)

	if got := BackendCredential(cfg.Backends["openai"]); got != "sk-from-config" {
		t.Errorf("credential = %q, want the config to win", got)
	}
}
