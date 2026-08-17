// Package config defines the zotui configuration: the repository connections,
// compute providers, models, environments that bind compute to an image and env
// vars, and the store that tracks workers and their runs.
//
// Workers are not in the config: they are created in the web command center and
// tracked in the store.
package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config is the whole zotui configuration.
type Config struct {
	// Repos are the repository connections a worker's code can come from, keyed by
	// name. Several connect at once - multiple GitHub orgs, GitHub and GitLab
	// together, self-hosted instances. A worker names which connection it uses.
	Repos map[string]Repo `yaml:"repos"`

	// Compute holds the providers that create or expose remote computers, keyed by
	// a name an environment references.
	Compute map[string]Compute `yaml:"compute"`

	// Models are the LLM configs zot reasons with, keyed by a name an environment
	// or worker references. Each carries the provider credential.
	Models map[string]Model `yaml:"models"`

	// Environments bind compute to a base image and env vars - the blueprint a
	// run's sandbox is created from.
	Environments map[string]Environment `yaml:"environments"`

	// Store selects where workers, runs, and output are persisted.
	Store StoreConfig `yaml:"store"`
}

// Repo is one repository connection: its type, the credentials to reach it, and an
// optional per-repo lockdown. Type selects the implementation; the fields it
// reads depend on the type (github uses the App fields, gitlab the base_url/token).
//
// Repositories is an OPTIONAL lockdown for THIS repo connection: empty discovers
// every repository it exposes; listing narrows to exactly those (owner/name
// within the connection). It can only restrict reach, never widen it.
type Repo struct {
	Type string `yaml:"type"` // github, gitlab, local
	Path string `yaml:"path"` // local: checkout path mounted into the sandbox

	// github: a GitHub App installation
	AppID          int64  `yaml:"app_id"`
	InstallationID int64  `yaml:"installation_id"`
	PrivateKey     string `yaml:"private_key"` // PEM, inline or $VAR

	// gitlab: an instance URL and an access token
	BaseURL string `yaml:"base_url"`
	Token   string `yaml:"token"` // inline or $VAR

	Repositories []string `yaml:"repositories"` // optional per-repo lockdown; empty = discover
}

// Compute is a provider of remote computers: its type and credentials.
type Compute struct {
	Type      string `yaml:"type"`       // cloudflare, docker, vercel, ssh, ...
	AccountID string `yaml:"account_id"` // provider-specific
	APIToken  string `yaml:"api_token"`
	BaseURL   string `yaml:"base_url"` // optional endpoint override
}

// Model is an LLM configuration zot reasons with: the provider, the model name,
// and the credential to reach it. The key is held on the host and injected into a
// run's sandbox at dispatch - never baked into the image.
type Model struct {
	Provider string `yaml:"provider"` // zai, openai, anthropic, ...
	Model    string `yaml:"model"`    // the model name the provider knows
	APIKey   string `yaml:"api_key"`  // inline or $VAR
	BaseURL  string `yaml:"base_url"` // optional gateway / custom endpoint
}

// Environment binds compute to a base image and a set of environment variables -
// the reusable blueprint a run's sandbox is created from: define once, spawn many
// ephemeral sandboxes. Model is the default model for workers on this environment;
// a worker can override it.
//
// Repositories is an OPTIONAL per-environment lockdown: when set, only these
// repositories may run on this environment, additional to any per-repo lockdown.
// Entries start with the configured repo name ("repo/owner/name"), since an
// environment can span repo connections.
type Environment struct {
	Compute      string            `yaml:"compute"`      // references a key in Compute
	Model        string            `yaml:"model"`        // default model (references Models)
	Image        string            `yaml:"image"`        // base image: toolchain + zot
	Env          map[string]string `yaml:"env"`          // environment variables
	Repositories []string          `yaml:"repositories"` // optional per-env lockdown
}

// StoreConfig selects and locates the command center store.
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
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	c.expand()
	return &c, nil
}

// expand resolves $VAR references in the credential-bearing and path fields, and
// a leading ~ in the store path.
func (c *Config) expand() {
	c.Store.DSN = expandHome(os.ExpandEnv(c.Store.DSN))
	for name, s := range c.Repos {
		s.PrivateKey = os.ExpandEnv(s.PrivateKey)
		s.Token = os.ExpandEnv(s.Token)
		s.Path = expandHome(os.ExpandEnv(s.Path))
		c.Repos[name] = s
	}
	for name, r := range c.Compute {
		r.AccountID = os.ExpandEnv(r.AccountID)
		r.APIToken = os.ExpandEnv(r.APIToken)
		c.Compute[name] = r
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
