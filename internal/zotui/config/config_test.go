package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/openzot/openzot/configs"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// The file seeded by `zotui config` must use the schema the loader accepts and
// every environment in it must point at configured compute.
func TestEmbeddedExampleLoads(t *testing.T) {
	path := writeConfig(t, string(configs.Zotui))
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load embedded example: %v", err)
	}
	if len(cfg.Repos) == 0 || len(cfg.Compute) == 0 || len(cfg.Providers) == 0 || len(cfg.Environments) == 0 {
		t.Fatalf("example does not describe the repo/compute/environment graph: %+v", cfg)
	}
	for name, env := range cfg.Environments {
		if _, ok := cfg.Compute[env.Compute]; !ok {
			t.Errorf("environment %q references unknown compute %q", name, env.Compute)
		}
		if _, _, ok := cfg.ResolveModel(env.Provider, env.Model); !ok {
			t.Errorf("environment %q references unknown model %q for provider %q", name, env.Model, env.Provider)
		}
	}
}

// The config vocabulary is a connected graph: repos and compute are named at
// the top level, and an environment binds compute to its image, vars and model.
func TestLoadReposComputeAndEnvironment(t *testing.T) {
	t.Setenv("TEST_REPO_KEY", "repo-secret")
	t.Setenv("TEST_COMPUTE_TOKEN", "compute-secret")
	t.Setenv("TEST_VERCEL_TOKEN", "vercel-secret")
	t.Setenv("TEST_VERCEL_TEAM", "team_123")
	t.Setenv("TEST_VERCEL_PROJECT", "prj_123")
	t.Setenv("TEST_MODEL_KEY", "model-secret")

	path := writeConfig(t, `
repos:
  acme:
    type: github
    private_key: $TEST_REPO_KEY
compute:
  cf:
    type: cloudflare
    account_id: account
    api_token: $TEST_COMPUTE_TOKEN
  vercel:
    type: vercel
    token: $TEST_VERCEL_TOKEN
    team_id: $TEST_VERCEL_TEAM
    project_id: $TEST_VERCEL_PROJECT
    timeout: 2h
providers:
  corporate:
    driver: zai
    api_key: $TEST_MODEL_KEY
    base_url: https://models.example.com/v1
    models:
      glm:
        model: glm-5.2
environments:
  go:
    compute: cf
    provider: corporate
    model: glm
    image: toolchain
    env:
      GOFLAGS: -mod=mod
store:
  driver: sqlite
  dsn: ':memory:'
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Repos["acme"].PrivateKey != "repo-secret" {
		t.Error("repo credential was not resolved")
	}
	if cfg.Compute["cf"].APIToken != "compute-secret" {
		t.Error("compute credential was not resolved")
	}
	if got := cfg.Compute["vercel"]; got.Token != "vercel-secret" || got.TeamID != "team_123" || got.ProjectID != "prj_123" || got.Timeout != "2h" {
		t.Errorf("vercel compute config was not resolved: %+v", got)
	}
	if got := cfg.Providers["corporate"]; got.Driver != "zai" || got.APIKey != "model-secret" || got.BaseURL != "https://models.example.com/v1" {
		t.Errorf("model provider was not resolved: %+v", got)
	}
	if got := cfg.Providers["corporate"].Models["glm"]; got.Model != "glm-5.2" {
		t.Errorf("model reference was not loaded: %+v", got)
	}
	provider, model, ok := cfg.ResolveModel("corporate", "glm")
	if !ok || model != "glm-5.2" || provider.APIKey != "model-secret" {
		t.Errorf("custom model did not resolve through its provider: provider=%+v model=%q ok=%v", provider, model, ok)
	}
	if env := cfg.Environments["go"]; env.Compute != "cf" || env.Provider != "corporate" || env.Image != "toolchain" || env.Env["GOFLAGS"] != "-mod=mod" {
		t.Errorf("environment binding was not loaded: %+v", env)
	}
}

func TestLoadExpandsLocalRepoPath(t *testing.T) {
	t.Setenv("TEST_CHECKOUT", filepath.Join(t.TempDir(), "checkout"))
	cfg, err := Load(writeConfig(t, `
repos:
  checkout:
    type: local
    path: $TEST_CHECKOUT
providers:
  zai: {}
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Repos["checkout"].Path; got != os.Getenv("TEST_CHECKOUT") {
		t.Fatalf("expanded path = %q", got)
	}
}

// Removed config names must fail rather than be ignored and leave the command
// center with an empty graph that only breaks when a worker is started.
func TestLoadRejectsRemovedTerminology(t *testing.T) {
	tests := map[string]string{
		"sources": `sources:
  acme:
    type: github
`,
		"runners": `runners:
  cf:
    type: cloudflare
`,
		"environment runner": `environments:
  go:
    runner: cf
`,
		"top-level models": `models:
  glm:
    model: glm-5
`,
		"model credential": `providers:
  zai:
    models:
      glm:
        api_key: secret
`,
	}

	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(writeConfig(t, body)); err == nil {
				t.Fatalf("removed terminology %q was silently accepted", name)
			}
		})
	}
}

func TestLoadRejectsBrokenReferences(t *testing.T) {
	tests := map[string]string{
		"providers": `repos:
  checkout:
    type: local
    path: /workspace
`,
		"provider models": `providers:
  private:
    driver: custom
    base_url: https://models.example.com/v1
`,
		"environment compute": `providers:
  zai: {}
environments:
  go:
    compute: missing
    provider: zai
    model: glm-5
`,
		"environment provider": `compute:
  local:
    type: docker
environments:
  go:
    compute: local
    provider: missing
    model: glm-5
`,
		"environment model": `compute:
  local:
    type: docker
providers:
  zai: {}
environments:
  go:
    compute: local
    provider: zai
    model: missing
`,
	}

	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(writeConfig(t, body)); err == nil {
				t.Fatalf("broken %s reference was accepted", name)
			}
		})
	}
}
