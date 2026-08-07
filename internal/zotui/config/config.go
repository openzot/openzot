// Package config defines the zotui configuration: the repository provider (git,
// which mints per-job credentials and offers the repositories to choose from), the
// compute runners, the models zot reasons with, the environments that bind a
// runner to a base image and env vars, and the store that tracks scheduled jobs.
//
// Both the TUI and the future web interface load this same file - there is one
// config and one loader, so the two faces can never drift. Jobs are NOT in the
// config: they are scheduled from the tool at runtime and tracked in the store.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config is the whole zotui configuration.
type Config struct {
	// Sources are the repository providers a job's code can come from, keyed by
	// name. Several connect at once - multiple GitHub orgs, GitHub and GitLab
	// together, self-hosted instances. A job names which source it targets.
	Sources map[string]Source `yaml:"sources"`

	// Runners are the compute providers, keyed by a name an environment references.
	Runners map[string]Runner `yaml:"runners"`

	// Models are the LLM configs zot reasons with, keyed by a name an environment
	// or a job references. Each carries the provider credential.
	Models map[string]Model `yaml:"models"`

	// Environments bind a runner to a base image and env vars - the blueprint a
	// job's sandbox is created from.
	Environments map[string]Environment `yaml:"environments"`

	// Store selects where scheduled jobs and their progress are persisted.
	Store StoreConfig `yaml:"store"`
}

// Source is one repository provider: its type, the credentials to reach it, and an
// optional per-source lockdown. Type selects the implementation; the fields it
// reads depend on the type (github uses the App fields, gitlab the base_url/token).
//
// Repositories is an OPTIONAL lockdown for THIS source: empty discovers every
// repository the source exposes; listing narrows to exactly those (owner/name
// within the source). It can only restrict the source's reach, never widen it.
type Source struct {
	Type string `yaml:"type"` // github, gitlab

	// github: a GitHub App installation
	AppID          int64  `yaml:"app_id"`
	InstallationID int64  `yaml:"installation_id"`
	PrivateKey     string `yaml:"private_key"` // PEM, inline or $VAR

	// gitlab: an instance URL and an access token
	BaseURL string `yaml:"base_url"`
	Token   string `yaml:"token"` // inline or $VAR

	Repositories []string `yaml:"repositories"` // optional per-source lockdown; empty = discover
}

// Runner is a compute provider: its type and the credentials to reach it.
type Runner struct {
	Type      string `yaml:"type"`       // cloudflare, vercel, ssh, ...
	AccountID string `yaml:"account_id"` // provider-specific
	APIToken  string `yaml:"api_token"`
	BaseURL   string `yaml:"base_url"` // optional endpoint override
}

// Model is an LLM configuration zot reasons with: the provider, the model name,
// and the credential to reach it. The key is held on the host and injected into a
// job's sandbox at dispatch - never baked into the image.
type Model struct {
	Provider string `yaml:"provider"` // zai, openai, anthropic, ...
	Model    string `yaml:"model"`    // the model name the provider knows
	APIKey   string `yaml:"api_key"`  // inline or $VAR
	BaseURL  string `yaml:"base_url"` // optional gateway / custom endpoint
}

// Environment binds a runner to a base image and a set of environment variables -
// the reusable blueprint a job's sandbox is created from: define once, spawn many
// ephemeral sandboxes. Model is the default model for jobs on this environment; a
// job can override it at dispatch.
//
// Repositories is an OPTIONAL per-environment lockdown: when set, only these
// repositories may run on this environment, additional to any per-source lockdown.
// Entries are source-qualified as "source/owner/repo", since an environment can
// span sources.
type Environment struct {
	Runner       string            `yaml:"runner"`       // references a key in Runners
	Model        string            `yaml:"model"`        // default model (references Models)
	Image        string            `yaml:"image"`        // base image: toolchain + zot
	Env          map[string]string `yaml:"env"`          // environment variables
	Repositories []string          `yaml:"repositories"` // optional per-env lockdown
}

// StoreConfig selects and locates the job store.
type StoreConfig struct {
	Driver string `yaml:"driver"` // sqlite (default), postgres, ...
	DSN    string `yaml:"dsn"`    // path or connection string
}

// Load reads and expands a zotui config file. $VAR references in credential and
// path fields are expanded from the environment, so secrets stay out of the file.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	var c Config
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	c.expand()
	return &c, nil
}

// expand resolves $VAR references in the credential-bearing and path fields, and
// a leading ~ in the store path.
func (c *Config) expand() {
	c.Store.DSN = expandHome(os.ExpandEnv(c.Store.DSN))
	for name, s := range c.Sources {
		s.PrivateKey = os.ExpandEnv(s.PrivateKey)
		s.Token = os.ExpandEnv(s.Token)
		c.Sources[name] = s
	}
	for name, r := range c.Runners {
		r.AccountID = os.ExpandEnv(r.AccountID)
		r.APIToken = os.ExpandEnv(r.APIToken)
		c.Runners[name] = r
	}
	for name, m := range c.Models {
		m.APIKey = os.ExpandEnv(m.APIKey)
		c.Models[name] = m
	}
}

// expandHome resolves a leading ~/ to the user's home directory.
func expandHome(path string) string {
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}
