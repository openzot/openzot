// Package runner abstracts a compute integration - the thing that turns a
// resolved environment into a running sandbox.
//
// Cloudflare is the first driver. Vercel and registered computers (bring-your-own
// over SSH) are the same two interfaces: a Runner that creates sandboxes, and a
// Sandbox you can exec in and tear down.
package runner

import (
	"context"
	"io"
)

// Spec is a resolved environment: the base image, env vars, and model config a
// sandbox boots zot with.
type Spec struct {
	Image string
	Env   map[string]string
	Model ModelSpec
}

// ModelSpec is the resolved LLM config injected into the sandbox for zot: the
// provider, the model name, and the credential to reach it.
type ModelSpec struct {
	Provider string
	Model    string
	APIKey   string
	BaseURL  string
}

// Runner provisions ephemeral sandboxes for one provider.
type Runner interface {
	// Type is the provider name (cloudflare, vercel, ssh, ...).
	Type() string

	// Create boots a sandbox from spec and returns a handle to it.
	Create(ctx context.Context, spec Spec) (Sandbox, error)
}

// Sandbox is a running compute instance a job executes in.
type Sandbox interface {
	// Exec runs a command, streaming combined output to out, and returns the
	// command's exit code. env is merged over the sandbox's baseline environment.
	Exec(ctx context.Context, cmd []string, env map[string]string, out io.Writer) (int, error)

	// Destroy tears the sandbox down. Called even when a job fails, so a leaked
	// credential never outlives the run.
	Destroy(ctx context.Context) error
}
