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
	if len(cfg.Repos) == 0 || len(cfg.Compute) == 0 || len(cfg.Environments) == 0 {
		t.Fatalf("example does not describe the repo/compute/environment graph: %+v", cfg)
	}
	for name, env := range cfg.Environments {
		if _, ok := cfg.Compute[env.Compute]; !ok {
			t.Errorf("environment %q references unknown compute %q", name, env.Compute)
		}
	}
}

// The config vocabulary is a connected graph: repos and compute are named at
// the top level, and an environment binds compute to its image, vars and model.
func TestLoadReposComputeAndEnvironment(t *testing.T) {
	t.Setenv("TEST_REPO_KEY", "repo-secret")
	t.Setenv("TEST_COMPUTE_TOKEN", "compute-secret")
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
models:
  glm:
    provider: zai
    model: glm-5.2
    api_key: $TEST_MODEL_KEY
environments:
  go:
    compute: cf
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
	if cfg.Models["glm"].APIKey != "model-secret" {
		t.Error("model credential was not resolved")
	}
	if env := cfg.Environments["go"]; env.Compute != "cf" || env.Image != "toolchain" || env.Env["GOFLAGS"] != "-mod=mod" {
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
	}

	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(writeConfig(t, body)); err == nil {
				t.Fatalf("removed terminology %q was silently accepted", name)
			}
		})
	}
}
