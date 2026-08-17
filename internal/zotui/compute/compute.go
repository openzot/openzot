// Package compute abstracts the providers that turn a resolved environment into
// a running remote computer.
//
// Docker and remote providers satisfy the same two interfaces: a Provider that
// creates sandboxes, and a Sandbox that executes commands and can be torn down.
package compute

import (
	"context"
	"encoding/json"
	"io"
)

// Spec is a resolved environment plus the Zot executable the provider installs.
type Spec struct {
	Image         string
	Platform      string
	Env           map[string]string
	Mounts        []Mount
	Source        Source
	Worker        Worker
	Model         ModelSpec
	MaxIterations int
}

// Worker is the Zot executable the command center deploys into a sandbox.
// Environment images provide tools and dependencies; they do not need to carry
// a particular Zot release themselves.
type Worker struct {
	Platform string
	Data     []byte
}

// Mount exposes a host path inside a sandbox.
type Mount struct {
	Source   string
	Target   string
	ReadOnly bool
}

// Source is a remote Git checkout a compute provider can place in the sandbox.
// Password is populated only after the repo provider mints a per-run token.
type Source struct {
	URL       string
	Username  string
	Password  string
	Directory string
}

// ModelSpec is the resolved LLM config injected into the sandbox for zot: the
// provider, the model name, and the credential to reach it.
type ModelSpec struct {
	Provider string
	Model    string
	APIKey   string
	BaseURL  string
}

// EncodeZotConfig builds the private runtime configuration installed in every
// sandbox. Compute drivers write it with owner-only permissions.
func EncodeZotConfig(spec Spec) ([]byte, error) {
	return json.Marshal(map[string]any{
		"default_backend": "worker",
		"agent":           map[string]any{"model": spec.Model.Model, "max_iterations": spec.MaxIterations},
		"backends": map[string]any{"worker": map[string]string{
			"provider": spec.Model.Provider, "base_url": spec.Model.BaseURL, "api_key": spec.Model.APIKey,
		}},
	})
}

// Provider creates ephemeral computers for one configured compute service.
type Provider interface {
	// Type is the provider name (docker, cloudflare, vercel, ssh, ...).
	Type() string
	// Platform is the operating system and architecture workers run on.
	Platform() string

	// Create boots a sandbox from spec and returns a handle to it.
	Create(ctx context.Context, spec Spec) (Sandbox, error)
}

// Sandbox is a running compute instance a worker run executes in.
type Sandbox interface {
	// WorkerPath is the absolute path where Create installed the worker binary.
	WorkerPath() string

	// Exec runs a command, streaming combined output to out, and returns the
	// command's exit code. env is merged over the sandbox's baseline environment.
	Exec(ctx context.Context, cmd []string, env map[string]string, out io.Writer) (int, error)

	// Destroy tears the sandbox down. Called even when a run fails, so a leaked
	// credential never outlives the run.
	Destroy(ctx context.Context) error
}
