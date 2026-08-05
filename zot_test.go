package zot

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadProjectContext(t *testing.T) {
	configDir := t.TempDir()
	workDir := t.TempDir()

	// A global AGENT.md in the config dir and a project one in the work dir.
	mustWrite(t, filepath.Join(configDir, "AGENT.md"), "GLOBAL CONVENTIONS")
	mustWrite(t, filepath.Join(workDir, "AGENT.md"), "PROJECT CONVENTIONS")

	// A skill in each location: plain "skills/" in the config dir and hidden
	// ".skills/" in the project dir - both layouts must be picked up.
	mustWrite(t, filepath.Join(configDir, "skills", "greet", "SKILL.md"),
		"---\nname: greet\ndescription: say hello\n---\nbody")
	mustWrite(t, filepath.Join(workDir, ".skills", "deploy", "SKILL.md"),
		"---\nname: deploy\ndescription: ship it\n---\nbody")

	cfg := Config{}
	if err := LoadProjectContext(&cfg, configDir, workDir); err != nil {
		t.Fatalf("LoadProjectContext: %v", err)
	}

	// Backstory keeps the default and appends both AGENT.md files in order.
	for _, want := range []string{DefaultBackstory[:20], "GLOBAL CONVENTIONS", "PROJECT CONVENTIONS"} {
		if !strings.Contains(cfg.Agent.Backstory, want) {
			t.Errorf("backstory missing %q", want)
		}
	}
	if i, j := strings.Index(cfg.Agent.Backstory, "GLOBAL"), strings.Index(cfg.Agent.Backstory, "PROJECT"); i > j {
		t.Error("expected config-dir AGENT.md to appear before work-dir AGENT.md")
	}

	// A single skills feature carrying both skills.
	var skills *Feature
	for i := range cfg.Features {
		if cfg.Features[i].Name == "skills" {
			skills = &cfg.Features[i]
		}
	}
	if skills == nil {
		t.Fatal("expected a skills feature to be added")
	}
	list, _ := skills.Options["skills"].([]map[string]string)
	if len(list) != 2 {
		t.Fatalf("expected 2 skills, got %d (%v)", len(list), skills.Options["skills"])
	}
}

func TestLoadProjectContextNoFiles(t *testing.T) {
	cfg := Config{}
	if err := LoadProjectContext(&cfg, t.TempDir()); err != nil {
		t.Fatalf("LoadProjectContext: %v", err)
	}
	if cfg.Agent.Backstory != "" {
		t.Error("expected backstory untouched when no AGENT.md is present")
	}
	if len(cfg.Features) != 0 {
		t.Error("expected no features when no skills are present")
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeCfg writes a config file and returns its path.
func writeCfg(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

// The default run targets the CBK relay, which authenticates the provider per
// model inside the model string, so the backend-level RELAY_API_KEY is composed
// onto the default model.
func TestResolveRelayComposesModel(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("ZOT_CONFIG", "")
	t.Setenv("RELAY_API_KEY", "relay-key")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DefaultBackend != "relay" {
		t.Fatalf("default backend = %q, want relay", cfg.DefaultBackend)
	}
	client, opts, err := resolve(cfg, DefaultBackstory)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if client == nil {
		t.Fatal("expected a client")
	}
	want := cfg.Agent.Model + "/authorization=relay-key"
	if opts.Model != want {
		t.Errorf("model = %q, want %q", opts.Model, want)
	}
}

// Per-model authorization: each relay model carries its own provider key,
// composed into the model string.
func TestResolveRelayPerModelAuthorization(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("ZOT_CONFIG", "")
	t.Setenv("OPENAI_API_KEY", "sk-openai")
	path := writeCfg(t, `
agent:
  model: gpt-4
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
	_, opts, err := resolve(cfg, DefaultBackstory)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if opts.Model != "gpt-4/authorization=sk-openai" {
		t.Errorf("model = %q, want gpt-4/authorization=sk-openai", opts.Model)
	}
}

// A key already inlined into the model name is respected, not double-composed.
func TestResolveRelayInlineAuthorizationRespected(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("ZOT_CONFIG", "")
	path := writeCfg(t, `
agent:
  model: 'gpt-4/authorization=sk-inline'
default_backend: relay
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	_, opts, err := resolve(cfg, DefaultBackstory)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if opts.Model != "gpt-4/authorization=sk-inline" {
		t.Errorf("model = %q, want it unchanged", opts.Model)
	}
}

// No provider key anywhere for the relay is a clear, actionable error.
func TestResolveRelayErrorsWithoutAuthorization(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("ZOT_CONFIG", "")
	t.Setenv("RELAY_API_KEY", "")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, _, err := resolve(cfg, DefaultBackstory); err == nil {
		t.Fatal("expected an error when the relay model has no authorization")
	}
}

// The Bearer backends resolve to their endpoints and read their own credential;
// they never touch the model string.
func TestResolveBearerBackends(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("ZOT_CONFIG", "")
	t.Setenv("CBK_API_SECRET", "cbk-key")
	t.Setenv("CHATBOTKIT_API_SECRET", "chatbotkit-key")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, name := range []string{"cbk", "chatbotkit"} {
		cfg.DefaultBackend = name
		client, opts, err := resolve(cfg, DefaultBackstory)
		if err != nil {
			t.Fatalf("resolve(%s): %v", name, err)
		}
		if client == nil {
			t.Fatalf("resolve(%s): expected a client", name)
		}
		if opts.Model != cfg.Agent.Model {
			t.Errorf("%s model = %q, want %q unchanged", name, opts.Model, cfg.Agent.Model)
		}
	}
}

// A missing Bearer credential for a ChatBotKit backend is a clear error.
func TestResolveBearerErrorsWithoutSecret(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("ZOT_CONFIG", "")
	t.Setenv("CBK_API_SECRET", "")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	cfg.DefaultBackend = "cbk"
	if _, _, err := resolve(cfg, DefaultBackstory); err == nil {
		t.Fatal("expected an error when the cbk backend has no secret")
	}
}

// A custom model entry aliases a real id, caps iterations, and carries auth,
// which is composed onto the aliased model on the relay.
func TestResolveCustomModelAlias(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("ZOT_CONFIG", "")
	t.Setenv("OPENAI_API_KEY", "sk-openai")
	path := writeCfg(t, `
agent:
  model: fast
default_backend: relay
backends:
  relay:
    models:
      fast:
        model: gpt-5
        max_iterations: 50
        authorization: $OPENAI_API_KEY
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	_, opts, err := resolve(cfg, DefaultBackstory)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if opts.Model != "gpt-5/authorization=sk-openai" {
		t.Errorf("model = %q, want gpt-5/authorization=sk-openai", opts.Model)
	}
	if opts.MaxIterations != 50 {
		t.Errorf("max iterations = %d, want 50 (from custom model)", opts.MaxIterations)
	}
}
