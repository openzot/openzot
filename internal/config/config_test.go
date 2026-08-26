package config

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/openzot/openzot/agent"
	"github.com/openzot/openzot/internal/catalogue"
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
		Agent:           Agent{Model: "m", MaxIterations: 1},
		DefaultProvider: "openai",
		Providers:       map[string]ProviderConfig{"openai": {APIKey: "x"}},
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
	if c.DefaultProvider != "zai" {
		t.Errorf("default provider = %q, want zai", c.DefaultProvider)
	}
}

// The built-in providers are seeded for every provider zot knows, each reading
// its provider's conventional credential variable.
func TestLoadSeedsProviders(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("ZOT_CONFIG", "")
	t.Setenv("OPENAI_API_KEY", "sk-openai")
	t.Setenv("ANTHROPIC_API_KEY", "sk-anthropic")
	t.Setenv("GROQ_API_KEY", "sk-groq")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.DefaultProvider != "zai" {
		t.Errorf("default provider = %q, want zai", cfg.DefaultProvider)
	}

	for name, want := range map[string]string{
		"openai":    "sk-openai",
		"anthropic": "sk-anthropic",
		"groq":      "sk-groq",
	} {
		provider, ok := cfg.Providers[name]

		if !ok {
			t.Fatalf("provider %q was not seeded", name)
		}

		if got := ProviderCredential(provider); got != want {
			t.Errorf("%s credential = %q, want %q", name, got, want)
		}

		if got := ProviderDriver(name, provider); got != name {
			t.Errorf("%s driver = %q, want %q", name, got, name)
		}
	}

	// a local provider needs no credential
	if got := ProviderCredential(cfg.Providers["ollama"]); got != "" {
		t.Errorf("ollama credential = %q, want none", got)
	}
}

// The shipped connections are pinned as a complete set. This catches an
// accidental addition and an accidental removal with the same assertion.
func TestBuiltinProvidersMatchTheShippedConnections(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("ZOT_CONFIG", "")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	seeded := make([]string, 0, len(cfg.Providers))

	for name := range cfg.Providers {
		seeded = append(seeded, name)
	}

	sort.Strings(seeded)

	want := []string{
		"anthropic", "cerebras", "deepseek", "groq", "mistral", "moonshot",
		"ollama", "openai", "openrouter", "qwen", "together", "vercel", "xai",
		"zai",
	}

	if !reflect.DeepEqual(seeded, want) {
		t.Errorf("built-in providers:\n got %v\nwant %v", seeded, want)
	}

	// Every one of them must select a driver: an unresolved connection would fail
	// at the first request rather than at configuration time.
	for _, name := range seeded {
		if ProviderDriver(name, cfg.Providers[name]) == "" {
			t.Errorf("provider %q names no driver", name)
		}
	}
}

// Removed configuration vocabulary must fail loudly rather than being ignored
// and silently sending a run to the default provider.
func TestLoadRejectsRemovedConfigKeys(t *testing.T) {
	tests := map[string]string{
		"provider collection": `
default_backend: openai
backends:
  openai:
    api_key: sk-test
	`,
		"driver selector": `
default_provider: corporate
providers:
  corporate:
    provider: openai
	`,
	}

	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(writeConfig(t, body)); err == nil {
				t.Error("removed provider configuration keys must be rejected")
			}
		})
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
default_provider: openai
`)
	t.Setenv("ZOT_AGENT_MODEL", "gpt-4o")
	t.Setenv("ZOT_AGENT_MAX_ITERATIONS", "12")
	t.Setenv("ZOT_DEFAULT_PROVIDER", "groq")

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
	if cfg.DefaultProvider != "groq" {
		t.Errorf("default provider = %q, want groq (env overrides file)", cfg.DefaultProvider)
	}
	if got := ProviderCredential(cfg.Providers["groq"]); got != "sk-test" {
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
default_provider: openai
providers:
  openai:
    api_key: '$MY_PROVIDER_KEY'
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Providers["openai"].APIKey; got != "sk-from-env" {
		t.Errorf("resolved secret = %q, want sk-from-env", got)
	}
}

// A provider key and a per-model key may each be written as a $VAR, and both are
// resolved.
func TestAuthorizationEnvReference(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("GATEWAY_DEFAULT_KEY", "sk-gateway-default")
	t.Setenv("OPENAI_API_KEY", "sk-openai")
	path := writeConfig(t, `
default_provider: mygateway
providers:
  mygateway:
    api_key: '$GATEWAY_DEFAULT_KEY'
    models:
      gpt-4:
        api_key: $OPENAI_API_KEY
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Providers["mygateway"].APIKey; got != "sk-gateway-default" {
		t.Errorf("provider key = %q, want sk-gateway-default", got)
	}
	if got := cfg.Providers["mygateway"].Models["gpt-4"].APIKey; got != "sk-openai" {
		t.Errorf("model key = %q, want sk-openai", got)
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
	if err := validConfig(func(c *Config) { c.DefaultProvider = "nope" }).Validate(); err == nil {
		t.Error("expected an error for an unknown default provider")
	}
	if err := validConfig(func(c *Config) {
		c.Providers["openai"] = ProviderConfig{Models: map[string]ModelConfig{"allowed": {Model: "gpt-5.4"}}}
	}).Validate(); err == nil {
		t.Error("expected a custom provider model list to reject an unlisted model")
	}
	if err := validConfig(func(c *Config) {
		c.Agent.Model = "allowed"
		c.Providers["openai"] = ProviderConfig{Models: map[string]ModelConfig{"allowed": {Model: "gpt-5.4"}}}
	}).Validate(); err != nil {
		t.Errorf("custom provider model was rejected: %v", err)
	}
}

func TestProviderModelsUsesCustomListOrCatalogue(t *testing.T) {
	custom := ProviderConfig{Driver: "openai", Models: map[string]ModelConfig{
		"small": {Model: "gpt-5.4-mini"},
		"large": {Model: "gpt-5.4"},
	}}
	if got, want := ProviderModels("corporate", custom), []string{"large", "small"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("custom models = %v, want %v", got, want)
	}
	builtin := ProviderModels("corporate", ProviderConfig{Driver: "zai"})
	if len(builtin) == 0 || !sort.StringsAreSorted(builtin) {
		t.Fatalf("built-in ZAI models = %v", builtin)
	}
	if index := sort.SearchStrings(builtin, "glm-5.3"); index == len(builtin) || builtin[index] != "glm-5.3" {
		t.Fatalf("built-in ZAI models do not include glm-5.3: %v", builtin)
	}
}

// Scrubbing removes every resolved credential - Bearer secrets and provider
// keys, provider-level and per-model - from the environment, while
// leaving unrelated variables intact.
func TestScrubProviderSecrets(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("ZAI_API_KEY", "sk-zai")
	t.Setenv("OPENAI_API_KEY", "sk-openai")
	t.Setenv("ZOT_TEST_UNRELATED", "keep-me")
	path := writeConfig(t, `
default_provider: mygateway
providers:
  mygateway:
    api_key: $ZAI_API_KEY
    models:
      gpt-4:
        api_key: $OPENAI_API_KEY
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	ScrubProviderSecrets(cfg)

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

// ConfigDir is where a global AGENTS.md and skills live, so it has to track
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

// A provider either selects a driver zot can reach or brings its own endpoint.
// Neither means there is nowhere to send the request, and that is worth catching
// at load rather than mid-run.
func TestValidateRejectsAnUnreachableProvider(t *testing.T) {
	cfg := Defaults()
	cfg.DefaultProvider = "mygateway"

	// a name nobody knows, and no endpoint
	cfg.Providers = map[string]ProviderConfig{"mygateway": {}}

	if err := cfg.Validate(); err == nil {
		t.Error("a provider with no known driver and no base_url must be rejected")
	}

	// an endpoint of its own is enough
	cfg.Providers["mygateway"] = ProviderConfig{BaseURL: "https://gw.example.com/v1"}

	if err := cfg.Validate(); err != nil {
		t.Errorf("a provider with its own endpoint is valid: %v", err)
	}

	// or a driver zot knows
	cfg.Providers["mygateway"] = ProviderConfig{Driver: "openai"}

	if err := cfg.Validate(); err != nil {
		t.Errorf("a provider naming a known driver is valid: %v", err)
	}

	// a provider named after a driver needs nothing at all
	cfg.DefaultProvider = "groq"
	cfg.Providers = map[string]ProviderConfig{"groq": {}}

	if err := cfg.Validate(); err != nil {
		t.Errorf("a provider named after a driver is valid: %v", err)
	}
}

// A `$VAR` reference is the documented way to keep a key out of the config
// file. It silently did not expand, which sent the literal text "$MY_KEY" to
// the provider and came back as a 401 that reads like a bad key rather than a
// config that never resolved.
func TestAnEnvReferenceIsExpanded(t *testing.T) {
	t.Setenv("ZOT_TEST_PROVIDER_KEY", "sk-resolved")

	tests := []struct {
		name     string
		provider ProviderConfig
	}{
		{name: "api_key", provider: ProviderConfig{APIKey: "$ZOT_TEST_PROVIDER_KEY"}},
		{name: "braced", provider: ProviderConfig{APIKey: "${ZOT_TEST_PROVIDER_KEY}"}},
		{name: "padded", provider: ProviderConfig{APIKey: "  $ZOT_TEST_PROVIDER_KEY  "}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := Config{Providers: map[string]ProviderConfig{"mine": test.provider}}

			resolveProviders(&cfg)

			if got := ProviderCredential(cfg.Providers["mine"]); got != "sk-resolved" {
				t.Errorf("credential = %q, want the expanded value", got)
			}
		})
	}
}

// An unset variable must resolve to nothing, so the run fails with "no API key
// configured" rather than sending the literal reference to the provider.
func TestAnUnsetEnvReferenceResolvesToNothing(t *testing.T) {
	t.Setenv("ZOT_TEST_UNSET_KEY", "")

	cfg := Config{Providers: map[string]ProviderConfig{
		"mine": {Driver: "custom", APIKey: "$ZOT_TEST_UNSET_KEY"},
	}}

	resolveProviders(&cfg)

	if got := ProviderCredential(cfg.Providers["mine"]); got != "" {
		t.Errorf("credential = %q, want nothing", got)
	}
}

// A literal key is left exactly as written - a provider key that happens to
// contain a dollar sign is not a reference.
func TestALiteralCredentialIsUntouched(t *testing.T) {
	cfg := Config{Providers: map[string]ProviderConfig{
		"mine": {APIKey: "sk-literal-with-$-inside"},
	}}

	resolveProviders(&cfg)

	if got := ProviderCredential(cfg.Providers["mine"]); got != "sk-literal-with-$-inside" {
		t.Errorf("credential = %q, want it untouched", got)
	}
}

// Exporting the provider's conventional variable is enough on its own, and must
// not override a key the config states explicitly.
func TestTheConventionalVariableIsOnlyAFallback(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-from-env")

	cfg := Config{Providers: map[string]ProviderConfig{"openai": {}}}

	resolveProviders(&cfg)

	if got := ProviderCredential(cfg.Providers["openai"]); got != "sk-from-env" {
		t.Errorf("credential = %q, want the conventional variable", got)
	}

	cfg = Config{Providers: map[string]ProviderConfig{"openai": {APIKey: "sk-from-config"}}}

	resolveProviders(&cfg)

	if got := ProviderCredential(cfg.Providers["openai"]); got != "sk-from-config" {
		t.Errorf("credential = %q, want the config to win", got)
	}
}

// max_time is validated at load, so a typo is caught immediately rather than
// silently ignored into an unbounded run.
func TestMaxTimeIsValidated(t *testing.T) {
	base := func() Config {
		return Config{
			Agent:           Agent{Model: "m", MaxIterations: 10},
			DefaultProvider: "openai",
			Providers:       map[string]ProviderConfig{"openai": {APIKey: "k"}},
		}
	}

	good := base()
	good.Agent.MaxTime = "45m"
	if err := good.Validate(); err != nil {
		t.Errorf("a valid duration must pass: %v", err)
	}

	empty := base() // unbounded is valid
	if err := empty.Validate(); err != nil {
		t.Errorf("an empty max_time must pass (unbounded): %v", err)
	}

	bad := base()
	bad.Agent.MaxTime = "half an hour"
	if err := bad.Validate(); err == nil {
		t.Error("a malformed max_time must fail validation")
	}

	negative := base()
	negative.Agent.MaxTime = "-5m"
	if err := negative.Validate(); err == nil {
		t.Error("a negative max_time must fail validation")
	}
}

// Every run budget is settable from the environment, not just max_iterations -
// the config comment promises a ZOT_AGENT_MAX_* for each, so this pins that the
// reflection-based override actually reaches all of them (including the two-word
// max_continuations, whose env name is the easy one to get wrong).
func TestEveryBudgetHasAnEnvOverride(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("ZOT_CONFIG", "")

	t.Setenv("ZOT_AGENT_MAX_SETTLES", "3")
	t.Setenv("ZOT_AGENT_MAX_CALLS", "4")
	t.Setenv("ZOT_AGENT_MAX_TIME", "45m")
	t.Setenv("ZOT_AGENT_MAX_TOKENS", "5")
	t.Setenv("ZOT_AGENT_MAX_CONTINUATIONS", "6")
	t.Setenv("ZOT_AGENT_MAX_RECOVERIES", "9")
	t.Setenv("ZOT_AGENT_MAX_CYCLES", "7")
	t.Setenv("ZOT_AGENT_MAX_EMPTIES", "8")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	a := cfg.Agent

	for name, got := range map[string]int{
		"max_settles":       a.MaxSettles,
		"max_calls":         a.MaxCalls,
		"max_tokens":        a.MaxTokens,
		"max_continuations": a.MaxContinuations,
		"max_recoveries":    a.MaxRecoveries,
		"max_cycles":        a.MaxCycles,
		"max_empties":       a.MaxEmpties,
	} {
		if got == 0 {
			t.Errorf("%s was not set from its ZOT_AGENT_* variable", name)
		}
	}

	if a.MaxTime != "45m" {
		t.Errorf("max_time = %q, want 45m from the environment", a.MaxTime)
	}

	// and the string parses through to a real duration
	if d, err := a.MaxDuration(); err != nil || d.String() != "45m0s" {
		t.Errorf("MaxDuration = %v (err %v), want 45m", d, err)
	}
}

// limit_checkpoints is validated so a typo (a percentage over 99, or a negative)
// is caught at load rather than silently dropped into a run with no notices.
func TestLimitCheckpointsAreValidated(t *testing.T) {
	base := func(cp []int) Config {
		return Config{
			Agent:           Agent{Model: "m", MaxIterations: 10, LimitCheckpoints: cp},
			DefaultProvider: "openai",
			Providers:       map[string]ProviderConfig{"openai": {APIKey: "k"}},
		}
	}

	if err := base([]int{50, 80, 90}).Validate(); err != nil {
		t.Errorf("a valid checkpoint list must pass: %v", err)
	}

	if err := base(nil).Validate(); err != nil {
		t.Errorf("unset checkpoints must pass (uses the default): %v", err)
	}

	if err := base([]int{}).Validate(); err != nil {
		t.Errorf("an empty list must pass (disables notices): %v", err)
	}

	for _, bad := range [][]int{{0}, {100}, {150}, {-5}, {50, 200}} {
		if err := base(bad).Validate(); err == nil {
			t.Errorf("checkpoints %v must fail validation", bad)
		}
	}
}

// A standard build carries no compiled-in config, so nothing overrides the
// runtime file and Portable reads false. The portable build path is exercised
// by building with -tags portable, which this suite does not; applyPortableOverlay
// below covers the layering that path relies on.
func TestPortableIsOffInAStandardBuild(t *testing.T) {
	if Portable() {
		t.Error("a build without -tags portable must not report a compiled-in config")
	}
	if data := portableConfig(); data != nil {
		t.Errorf("portableConfig must be empty in a standard build, got %q", data)
	}
}

// The overlay is what makes a portable build authoritative: it is applied after
// the file and env, so a field it sets wins. A field it omits must be left
// exactly as the earlier layers resolved it - that is what lets an operator bake
// the model while still taking the key from the environment.
func TestPortableOverlayOverridesOnlyWhatItSets(t *testing.T) {
	// stand in for "the file and env already resolved this"
	cfg := Config{
		Agent:           Agent{Model: "from-env", MaxIterations: 42},
		DefaultProvider: "openai",
		Providers: map[string]ProviderConfig{
			"openai": {APIKey: "sk-from-env"},
		},
	}

	overlay := []byte(`
agent:
  model: baked-model
default_provider: zai
providers:
  zai:
    api_key: sk-baked
`)

	if err := applyPortableOverlay(&cfg, overlay); err != nil {
		t.Fatalf("applyPortableOverlay: %v", err)
	}

	// fields the overlay set are authoritative
	if cfg.Agent.Model != "baked-model" {
		t.Errorf("model = %q, want the baked value to win", cfg.Agent.Model)
	}
	if cfg.DefaultProvider != "zai" {
		t.Errorf("default_provider = %q, want the baked value to win", cfg.DefaultProvider)
	}
	if got := cfg.Providers["zai"].APIKey; got != "sk-baked" {
		t.Errorf("baked provider key = %q, want sk-baked", got)
	}

	// fields it did NOT set fall through untouched
	if cfg.Agent.MaxIterations != 42 {
		t.Errorf("max_iterations = %d, want the earlier layer (42) to survive an overlay that omits it", cfg.Agent.MaxIterations)
	}
	if got := cfg.Providers["openai"].APIKey; got != "sk-from-env" {
		t.Errorf("a provider the overlay did not mention was lost: openai key = %q", got)
	}
}

// An empty overlay - the standard build - must be a no-op, not a wipe.
func TestPortableOverlayEmptyIsANoOp(t *testing.T) {
	cfg := Config{Agent: Agent{Model: "keep", MaxIterations: 7}}

	if err := applyPortableOverlay(&cfg, nil); err != nil {
		t.Fatalf("applyPortableOverlay(nil): %v", err)
	}
	if cfg.Agent.Model != "keep" || cfg.Agent.MaxIterations != 7 {
		t.Errorf("an empty overlay changed the config: %+v", cfg.Agent)
	}
}

// Malformed baked YAML must fail loudly at load, not silently ship a binary that
// ignores its own compiled-in config.
func TestPortableOverlayRejectsBadYAML(t *testing.T) {
	cfg := Config{}
	if err := applyPortableOverlay(&cfg, []byte("agent: [not-a-mapping")); err == nil {
		t.Error("malformed compiled-in config must be an error")
	}
}

// The default strategy is compact, so a long autonomous run summarises rather
// than silently dropping its early context.
func TestDefaultContextStrategyIsCompact(t *testing.T) {
	if got := Defaults().Agent.ContextStrategy; got != agent.StrategyCompact {
		t.Errorf("default context_strategy = %q, want %q", got, agent.StrategyCompact)
	}
}

func TestContextStrategyIsValidated(t *testing.T) {
	base := func(strategy string) Config {
		return validConfig(func(c *Config) { c.Agent.ContextStrategy = strategy })
	}

	// the two real strategies and "unset" all pass
	for _, ok := range []string{"", agent.StrategyCompact, agent.StrategyTruncate} {
		if err := base(ok).Validate(); err != nil {
			t.Errorf("context_strategy %q must validate: %v", ok, err)
		}
	}

	if err := base("summarise").Validate(); err == nil {
		t.Error("an unknown context_strategy must fail validation")
	}
}

func TestCompactTuningIsValidated(t *testing.T) {
	// a ratio outside (0, 1] is a mistake worth catching at load
	for _, bad := range []float64{-0.1, 1.5} {
		cfg := validConfig(func(c *Config) { c.Agent.CompactTriggerRatio = bad })
		if err := cfg.Validate(); err == nil {
			t.Errorf("compact_trigger_ratio %g must fail validation", bad)
		}
	}

	// zero means "use the default" and must pass
	if err := validConfig(func(c *Config) { c.Agent.CompactTriggerRatio = 0 }).Validate(); err != nil {
		t.Errorf("an unset ratio must validate: %v", err)
	}

	// a valid ratio passes
	if err := validConfig(func(c *Config) { c.Agent.CompactTriggerRatio = 0.8 }).Validate(); err != nil {
		t.Errorf("a valid ratio must validate: %v", err)
	}

	if err := validConfig(func(c *Config) { c.Agent.CompactMinTokens = -1 }).Validate(); err == nil {
		t.Error("a negative compact_min_tokens must fail validation")
	}
}

// The strategy is a scalar field, so it picks up the ZOT_AGENT_* env override
// like every other knob.
func TestContextStrategyEnvOverride(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("ZOT_CONFIG", "")
	t.Setenv("ZAI_API_KEY", "sk-zai")
	t.Setenv("ZOT_AGENT_CONTEXT_STRATEGY", "truncate")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Agent.ContextStrategy != "truncate" {
		t.Errorf("ZOT_AGENT_CONTEXT_STRATEGY not applied: %q", cfg.Agent.ContextStrategy)
	}
}

// The viewer scrollback is a scalar UI field, so it takes the ZOT_UI_* env
// override like the others, and a negative value is rejected at load.
func TestScrollbackEnvOverrideAndValidation(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("ZOT_CONFIG", "")
	t.Setenv("ZAI_API_KEY", "sk-zai")
	t.Setenv("ZOT_UI_SCROLLBACK", "20000")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.UI.Scrollback != 20000 {
		t.Errorf("ZOT_UI_SCROLLBACK not applied: %d", cfg.UI.Scrollback)
	}

	if err := validConfig(func(c *Config) { c.UI.Scrollback = -1 }).Validate(); err == nil {
		t.Error("a negative ui.scrollback must fail validation")
	}
}

// Stream color is a capability declaration, so containers must be able to set
// it through the same scalar environment layer as the rest of ui.*.
func TestUIColorEnvOverrideAndValidation(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("ZOT_CONFIG", "")
	t.Setenv("ZAI_API_KEY", "sk-zai")
	t.Setenv("ZOT_UI_COLOR", "always")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.UI.Color != "always" {
		t.Errorf("ZOT_UI_COLOR not applied: %q", cfg.UI.Color)
	}
	if err := validConfig(func(c *Config) { c.UI.Color = "sometimes" }).Validate(); err == nil {
		t.Error("an unknown ui.color mode must fail validation")
	}
}

// ui.stats selects header fields; an unknown field name is a typo worth catching
// at load rather than silently dropping the field.
func TestUIStatsAreValidated(t *testing.T) {
	if err := validConfig(func(c *Config) { c.UI.Stats = []string{"model", "iter"} }).Validate(); err != nil {
		t.Errorf("a valid stat list must pass: %v", err)
	}

	if err := validConfig(func(c *Config) { c.UI.Stats = []string{"model", "bogus"} }).Validate(); err == nil {
		t.Error("an unknown stat field must fail validation")
	}

	if err := validConfig(func(c *Config) { c.UI.Stats = nil }).Validate(); err != nil {
		t.Errorf("an unset stat list must pass (defaults): %v", err)
	}
}

// A base_url override points a connection at a host the provider's own
// credential was never issued for. Falling back to OPENAI_API_KEY there forwards
// a real OpenAI key to whatever URL was typed into the file - which is how a
// provider credential ends up in someone else's logs. Overriding the endpoint
// has to cost the ambient fallback.
func TestOverriddenBaseURLDoesNotInheritTheEnvironmentKey(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("ZOT_CONFIG", "")
	t.Setenv("OPENAI_API_KEY", "sk-openai")
	t.Setenv("GROQ_API_KEY", "sk-groq")
	path := writeConfig(t, `
providers:
  openai:
    base_url: https://proxy.example.com/v1
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Providers["openai"].APIKey; got != "" {
		t.Errorf("openai key = %q, want it withheld from an endpoint it was not issued for", got)
	}
	// a provider left on its built-in endpoint still gets the convenience
	if got := cfg.Providers["groq"].APIKey; got != "sk-groq" {
		t.Errorf("groq key = %q, want the environment fallback", got)
	}
}

// The rule is about inheritance, not about custom endpoints: a key written for
// the overridden endpoint is exactly what it should use.
func TestOverriddenBaseURLKeepsAnExplicitKey(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("ZOT_CONFIG", "")
	t.Setenv("OPENAI_API_KEY", "sk-openai")
	t.Setenv("PROXY_KEY", "sk-proxy")
	path := writeConfig(t, `
providers:
  openai:
    base_url: https://proxy.example.com/v1
    api_key: $PROXY_KEY
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Providers["openai"].APIKey; got != "sk-proxy" {
		t.Errorf("openai key = %q, want the explicitly configured one", got)
	}
}

// providers.<name>.responses is the operator's word on which API a connection
// speaks. All three states must survive the file: unset (the automatic rule),
// true (force Responses), and false (force chat-completions) are different
// answers, so a nil has to stay a nil rather than collapsing into a default.
func TestProviderResponsesParsesAsATriState(t *testing.T) {
	tests := map[string]struct {
		yaml   string
		stated bool
		value  bool
	}{
		"unset": {`
providers:
  openai:
    api_key: sk-test
`, false, false},
		"true": {`
providers:
  openai:
    api_key: sk-test
    responses: true
`, true, true},
		"false": {`
providers:
  openai:
    api_key: sk-test
    responses: false
`, true, false},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			cfg, err := Load(writeConfig(t, test.yaml))
			if err != nil {
				t.Fatalf("Load: %v", err)
			}

			got := cfg.Providers["openai"].Responses

			if test.stated != (got != nil) {
				t.Fatalf("responses stated = %v, want %v", got != nil, test.stated)
			}

			if got != nil && *got != test.value {
				t.Errorf("responses = %v, want %v", *got, test.value)
			}
		})
	}
}

func TestModelCapabilitiesApplyTheContextOverrideToo(t *testing.T) {

	base := catalogue.Model{ContextWindow: 1_000_000}

	if got := (ModelConfig{Context: 32_000}).Capabilities(base); got.ContextWindow != 32_000 {
		t.Errorf("context window = %d, want the override", got.ContextWindow)
	}

	if got := (ModelConfig{}).Capabilities(base); got.ContextWindow != 1_000_000 {
		t.Errorf("context window = %d, want the catalogue's when unset", got.ContextWindow)
	}
}

func TestModelCapabilitiesParseFromYAML(t *testing.T) {
	var parsed struct {
		Models map[string]ModelConfig `yaml:"models"`
	}

	source := "models:\n  stealth/ox-alpha:\n    vision: true\n  blinkered:\n    vision: false\n  quiet: {}\n"

	if err := yaml.Unmarshal([]byte(source), &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if parsed.Models["stealth/ox-alpha"].Vision == nil || !*parsed.Models["stealth/ox-alpha"].Vision {
		t.Error("vision: true must parse as an explicit yes")
	}

	if parsed.Models["blinkered"].Vision == nil || *parsed.Models["blinkered"].Vision {
		t.Error("vision: false must parse as an explicit no, not as absent")
	}

	if parsed.Models["quiet"].Vision != nil {
		t.Error("an unstated capability must stay unstated, so the catalogue decides")
	}
}

// Attribution is what puts zot on OpenRouter's and Vercel's app rankings, so it
// has to be reachable from a config file and from the environment - the latter
// because the containers and CI jobs that do most of zot's calling never write
// a config file. The bool matters as much as the strings: opting out is the
// half someone will actually need.
func TestAttributionIsConfigurableFromFileAndEnv(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("ZOT_CONFIG", "")

	path := writeConfig(t, `
attribution:
  name: acme-bot
  url: https://acme.example
  disabled: true
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Attribution.Name != "acme-bot" {
		t.Errorf("name = %q, want acme-bot from the file", cfg.Attribution.Name)
	}

	if cfg.Attribution.URL != "https://acme.example" {
		t.Errorf("url = %q, want the file's value", cfg.Attribution.URL)
	}

	if !cfg.Attribution.Disabled {
		t.Error("disabled was not read from the file")
	}

	// and the same three from the environment, with no file at all
	t.Setenv("ZOT_ATTRIBUTION_NAME", "env-bot")
	t.Setenv("ZOT_ATTRIBUTION_URL", "https://env.example")
	t.Setenv("ZOT_ATTRIBUTION_DISABLED", "true")

	fromEnv, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if fromEnv.Attribution.Name != "env-bot" || fromEnv.Attribution.URL != "https://env.example" {
		t.Errorf("attribution = %+v, want the ZOT_ATTRIBUTION_* values", fromEnv.Attribution)
	}

	if !fromEnv.Attribution.Disabled {
		t.Error("ZOT_ATTRIBUTION_DISABLED did not reach the config")
	}

	// the default is on, and named: an empty config attributes zot itself
	def, err := Load("")
	if err == nil && def.Attribution.Name != "" && !def.Attribution.Disabled {
		t.Error("a bare config must leave attribution unset so the provider defaults apply")
	}
}

// The release check is the one request a run makes that is not to the
// configured provider, so turning it off has to work from the file and from
// the environment - the air-gapped hosts and locked-down CI jobs that need the
// opt-out are exactly the ones that may never write a config file. And the
// default must be on: an opt-out that defaults to off is not an opt-out.
func TestTheUpdateCheckCanBeDisabledFromFileAndEnv(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("ZOT_CONFIG", "")

	def, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if def.UpdateCheck.Disabled {
		t.Error("a bare config must leave the update check enabled")
	}

	path := writeConfig(t, `
update_check:
  disabled: true
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if !cfg.UpdateCheck.Disabled {
		t.Error("update_check.disabled was not read from the file")
	}

	t.Setenv("ZOT_UPDATE_CHECK_DISABLED", "true")

	fromEnv, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if !fromEnv.UpdateCheck.Disabled {
		t.Error("ZOT_UPDATE_CHECK_DISABLED did not reach the config")
	}
}
