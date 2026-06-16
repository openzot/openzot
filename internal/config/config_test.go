package config

import (
	"path/filepath"
	"testing"
)

func TestDefaults(t *testing.T) {
	c := Defaults()
	if c.Agent.Model == "" {
		t.Error("expected a default model")
	}
	if c.Agent.MaxIterations <= 0 {
		t.Error("expected a positive default max_iterations")
	}
}

func TestLoadEnvOverrides(t *testing.T) {
	// Point the default config path at an empty dir so no real file is read.
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
	if cfg.ChatBotKit.APISecret != "sk-test" {
		t.Errorf("api_secret = %q, want it taken from CHATBOTKIT_API_SECRET", cfg.ChatBotKit.APISecret)
	}
}

func TestLoadExplicitMissingIsError(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "nope.yaml")); err == nil {
		t.Error("expected an error for a missing explicit --config file")
	}
}

func TestValidate(t *testing.T) {
	if err := (Config{Agent: Agent{Model: "m", MaxIterations: 1}}).Validate(); err != nil {
		t.Errorf("unexpected error for a valid config: %v", err)
	}
	if err := (Config{Agent: Agent{MaxIterations: 1}}).Validate(); err == nil {
		t.Error("expected an error for an empty model")
	}
	if err := (Config{Agent: Agent{Model: "m"}}).Validate(); err == nil {
		t.Error("expected an error for non-positive max_iterations")
	}
}
