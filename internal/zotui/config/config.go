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
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/openzot/openzot/internal/catalogue"
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

	// Providers are the inference connections Zot can use. Each optionally
	// supplies a custom model list; otherwise its built-in catalogue drives the
	// worker form.
	Providers map[string]Provider `yaml:"providers"`

	// Environments bind compute to runtime settings - the blueprint a run's
	// sandbox is created from. Image is an optional compute-specific override.
	Environments map[string]Environment `yaml:"environments"`

	// Store selects where workers, runs, and output are persisted.
	Store StoreConfig `yaml:"store"`
}

// Repo is one repository connection: its type, the credentials to reach it, and an
// optional per-repo lockdown. Type selects the implementation; the fields it
// reads depend on the type (github optionally uses the App fields, gitlab the
// base_url/token). A github connection without App credentials is public-only
// and must list its repositories explicitly.
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
	Token     string `yaml:"token"`      // vercel access token, inline or $VAR
	TeamID    string `yaml:"team_id"`    // vercel team ID
	ProjectID string `yaml:"project_id"` // vercel project ID
	Timeout   string `yaml:"timeout"`    // vercel sandbox lifetime (default 45m)
	BaseURL   string `yaml:"base_url"`   // optional endpoint override
}

// Provider is one inference connection. Driver selects Zot's transport; when it
// is empty, the provider's map key is also the driver name. Credentials are held
// on the host and injected into a run's sandbox at dispatch.
type Provider struct {
	Driver  string           `yaml:"driver"`   // zai, openai, anthropic, custom, ...
	APIKey  string           `yaml:"api_key"`  // inline or $VAR
	BaseURL string           `yaml:"base_url"` // optional gateway / custom endpoint
	Models  map[string]Model `yaml:"models"`   // optional custom list; empty uses built-ins
}

// Model is one custom visible LLM choice. An empty Model uses the choice's map
// key as the model ID sent to the provider.
type Model struct {
	Model string `yaml:"model"`
}

// Environment binds compute to runtime settings - the reusable blueprint a run's
// sandbox is created from: define once, spawn many ephemeral sandboxes. Model is
// the default model for workers on this environment; a worker can override it.
//
// Repositories is an OPTIONAL per-environment lockdown: when set, only these
// repositories may run on this environment, additional to any per-repo lockdown.
// Entries start with the configured repo name ("repo/owner/name"), since an
// environment can span repo connections.
type Environment struct {
	Compute      string            `yaml:"compute"`      // references a key in Compute
	Provider     string            `yaml:"provider"`     // default inference connection
	Model        string            `yaml:"model"`        // default model for Provider
	Image        string            `yaml:"image"`        // optional compute-specific override; omit for standard runtime
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
	if err := c.validate(); err != nil {
		return nil, fmt.Errorf("validate config: %w", err)
	}
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
		r.Token = os.ExpandEnv(r.Token)
		r.TeamID = os.ExpandEnv(r.TeamID)
		r.ProjectID = os.ExpandEnv(r.ProjectID)
		r.Timeout = os.ExpandEnv(r.Timeout)
		r.BaseURL = os.ExpandEnv(r.BaseURL)
		c.Compute[name] = r
	}
	for name, p := range c.Providers {
		p.APIKey = os.ExpandEnv(p.APIKey)
		p.BaseURL = os.ExpandEnv(p.BaseURL)
		c.Providers[name] = p
	}
}

func (c *Config) validate() error {
	if len(c.Providers) == 0 {
		return fmt.Errorf("at least one model provider is required")
	}
	for name := range c.Providers {
		if len(c.ProviderModels(name)) == 0 {
			return fmt.Errorf("provider %q has no custom models and driver %q has no built-in models", name, c.providerDriver(name))
		}
	}
	for name, environment := range c.Environments {
		if _, ok := c.Compute[environment.Compute]; !ok {
			return fmt.Errorf("environment %q references unknown compute %q", name, environment.Compute)
		}
		if _, ok := c.Providers[environment.Provider]; !ok {
			return fmt.Errorf("environment %q references unknown provider %q", name, environment.Provider)
		}
		if _, _, ok := c.ResolveModel(environment.Provider, environment.Model); !ok {
			return fmt.Errorf("environment %q references unknown model %q for provider %q", name, environment.Model, environment.Provider)
		}
	}
	return nil
}

func (c *Config) providerDriver(name string) string {
	if driver := c.Providers[name].Driver; driver != "" {
		return driver
	}
	return name
}

// ProviderModels returns the sorted model names visible for a provider. A
// custom list replaces the built-in catalogue; an omitted list uses the
// provider's resolved driver.
func (c *Config) ProviderModels(name string) []string {
	provider, ok := c.Providers[name]
	if !ok {
		return nil
	}
	if len(provider.Models) == 0 {
		return catalogue.NamesForProvider(c.providerDriver(name))
	}
	names := make([]string, 0, len(provider.Models))
	for name := range provider.Models {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// ResolveModel resolves one visible model alias to the provider model ID.
func (c *Config) ResolveModel(providerName, modelName string) (Provider, string, bool) {
	provider, ok := c.Providers[providerName]
	if !ok {
		return Provider{}, "", false
	}
	if len(provider.Models) == 0 {
		if !slices.Contains(catalogue.NamesForProvider(c.providerDriver(providerName)), modelName) {
			return Provider{}, "", false
		}
		return provider, modelName, true
	}
	model, ok := provider.Models[modelName]
	if !ok {
		return Provider{}, "", false
	}
	if model.Model == "" {
		model.Model = modelName
	}
	return provider, model.Model, true
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
