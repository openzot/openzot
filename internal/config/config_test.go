package config

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/openzot/openzot/agent"
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

// The built-in backends are exactly the providers zot speaks to. Pinning the
// whole set rather than spot-checking it catches an accidental addition and an
// accidental removal with the same assertion - and does not need updating when
// something is dropped, which is what a list of names-that-must-not-appear
// would have required.
func TestBuiltinBackendsAreExactlyTheProviders(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("ZOT_CONFIG", "")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	seeded := make([]string, 0, len(cfg.Backends))

	for name := range cfg.Backends {
		seeded = append(seeded, name)
	}

	sort.Strings(seeded)

	want := []string{
		"anthropic", "cerebras", "deepseek", "groq", "mistral", "moonshot",
		"ollama", "openai", "openrouter", "qwen", "together", "vercel", "xai",
		"zai",
	}

	if !reflect.DeepEqual(seeded, want) {
		t.Errorf("built-in backends:\n got %v\nwant %v", seeded, want)
	}

	// every one of them must be reachable: a name with no endpoint is a backend
	// that fails at the first request rather than at configuration time
	for _, name := range seeded {
		if BackendProvider(name, cfg.Backends[name]) == "" {
			t.Errorf("backend %q names no provider", name)
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
default_backend: openai
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
    api_key: '$MY_PROVIDER_KEY'
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Backends["openai"].APIKey; got != "sk-from-env" {
		t.Errorf("resolved secret = %q, want sk-from-env", got)
	}
}

// A backend key and a per-model key may each be written as a $VAR, and both are
// resolved.
func TestAuthorizationEnvReference(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("GATEWAY_DEFAULT_KEY", "sk-gateway-default")
	t.Setenv("OPENAI_API_KEY", "sk-openai")
	path := writeConfig(t, `
default_backend: mygateway
backends:
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
	if got := cfg.Backends["mygateway"].APIKey; got != "sk-gateway-default" {
		t.Errorf("backend key = %q, want sk-gateway-default", got)
	}
	if got := cfg.Backends["mygateway"].Models["gpt-4"].APIKey; got != "sk-openai" {
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
	if err := validConfig(func(c *Config) { c.DefaultBackend = "nope" }).Validate(); err == nil {
		t.Error("expected an error for an unknown default backend")
	}
}

// Scrubbing removes every resolved credential - Bearer secrets and provider
// keys, backend-level and per-model - from the environment, while
// leaving unrelated variables intact.
func TestScrubBackendSecrets(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("ZAI_API_KEY", "sk-zai")
	t.Setenv("OPENAI_API_KEY", "sk-openai")
	t.Setenv("ZOT_TEST_UNRELATED", "keep-me")
	path := writeConfig(t, `
default_backend: mygateway
backends:
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
// file. It silently did not expand, which sent the literal text "$MY_KEY" to
// the provider and came back as a 401 that reads like a bad key rather than a
// config that never resolved.
func TestAnEnvReferenceIsExpanded(t *testing.T) {
	t.Setenv("ZOT_TEST_PROVIDER_KEY", "sk-resolved")

	tests := []struct {
		name    string
		backend Backend
	}{
		{name: "api_key", backend: Backend{APIKey: "$ZOT_TEST_PROVIDER_KEY"}},
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

// max_time is validated at load, so a typo is caught immediately rather than
// silently ignored into an unbounded run.
func TestMaxTimeIsValidated(t *testing.T) {
	base := func() Config {
		return Config{
			Agent:          Agent{Model: "m", MaxIterations: 10},
			DefaultBackend: "openai",
			Backends:       map[string]Backend{"openai": {APIKey: "k"}},
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
			Agent:          Agent{Model: "m", MaxIterations: 10, LimitCheckpoints: cp},
			DefaultBackend: "openai",
			Backends:       map[string]Backend{"openai": {APIKey: "k"}},
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
		Agent:          Agent{Model: "from-env", MaxIterations: 42},
		DefaultBackend: "openai",
		Backends: map[string]Backend{
			"openai": {APIKey: "sk-from-env"},
		},
	}

	overlay := []byte(`
agent:
  model: baked-model
default_backend: zai
backends:
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
	if cfg.DefaultBackend != "zai" {
		t.Errorf("default_backend = %q, want the baked value to win", cfg.DefaultBackend)
	}
	if got := cfg.Backends["zai"].APIKey; got != "sk-baked" {
		t.Errorf("baked backend key = %q, want sk-baked", got)
	}

	// fields it did NOT set fall through untouched
	if cfg.Agent.MaxIterations != 42 {
		t.Errorf("max_iterations = %d, want the earlier layer (42) to survive an overlay that omits it", cfg.Agent.MaxIterations)
	}
	if got := cfg.Backends["openai"].APIKey; got != "sk-from-env" {
		t.Errorf("a backend the overlay did not mention was lost: openai key = %q", got)
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
