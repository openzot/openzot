// Package dispatch executes a worker run on a remote sandbox.
package dispatch

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/openzot/openzot/internal/zotui/compute"
	"github.com/openzot/openzot/internal/zotui/repo"
)

// Execution is the resolved worker definition needed by one run.
type Execution struct {
	Repo          string
	Repository    string
	Mission       string
	Environment   string
	Model         string
	MaxIterations int
}

// Resolver turns an execution into a compute provider and sandbox spec.
type Resolver interface {
	Resolve(Execution) (compute.Provider, compute.Spec, error)
}

type Dispatcher struct {
	Repo     repo.Provider
	Resolver Resolver
	Output   io.Writer
}

type Result struct{ ExitCode int }

// Dispatch boots a sandbox, runs zot, and always tears the sandbox down.
func (d *Dispatcher) Dispatch(ctx context.Context, execution Execution) (*Result, error) {
	provider, spec, err := d.Resolver.Resolve(execution)
	if err != nil {
		return nil, fmt.Errorf("resolve run: %w", err)
	}
	token, err := d.Repo.MintToken(ctx, []string{execution.Repository})
	if err != nil {
		return nil, fmt.Errorf("mint credentials: %w", err)
	}
	sandbox, err := provider.Create(ctx, spec)
	if err != nil {
		return nil, fmt.Errorf("create sandbox: %w", err)
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		_ = sandbox.Destroy(cleanupCtx)
	}()
	env := map[string]string{"ZOT_REPO": execution.Repository}
	if token.Value != "" {
		env["GH_TOKEN"] = token.Value
	}
	code, err := sandbox.Exec(ctx, []string{"zot", execution.Mission}, env, d.Output)
	if err != nil {
		return nil, fmt.Errorf("execute run: %w", err)
	}
	return &Result{ExitCode: code}, nil
}
