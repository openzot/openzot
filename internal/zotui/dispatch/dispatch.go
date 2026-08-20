// Package dispatch executes a worker run on a remote sandbox.
package dispatch

import (
	"context"
	"fmt"
	"io"
	"maps"
	"time"

	"github.com/openzot/openzot/internal/order"
	"github.com/openzot/openzot/internal/zotui/compute"
	"github.com/openzot/openzot/internal/zotui/repo"
)

// Execution is the resolved worker definition needed by one run.
type Execution struct {
	Repo          string
	Repository    string
	Mission       string
	Environment   string
	Provider      string
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
	Worker   func(string) (compute.Worker, error)
	Output   io.Writer
}

type Result struct{ ExitCode int }

// Dispatch boots a sandbox, runs zot, and always tears the sandbox down.
func (d *Dispatcher) Dispatch(ctx context.Context, execution Execution) (*Result, error) {
	provider, spec, err := d.Resolver.Resolve(execution)
	if err != nil {
		return nil, fmt.Errorf("resolve run: %w", err)
	}
	if d.Worker == nil {
		return nil, fmt.Errorf("resolve worker: no worker source configured")
	}
	spec.Worker, err = d.Worker(spec.Platform)
	if err != nil {
		return nil, fmt.Errorf("resolve worker: %w", err)
	}
	token, err := d.Repo.MintToken(ctx, []string{execution.Repository})
	if err != nil {
		return nil, fmt.Errorf("mint credentials: %w", err)
	}
	if token.Value != "" {
		spec.Source.Password = token.Value
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
	// The command center consumes an append-only ANSI stream. Declaring color
	// support is deliberately separate from allocating a TTY: a TTY would make
	// zot start its keyboard-driven alternate-screen viewer.
	env := map[string]string{"ZOT_REPO": execution.Repository, "ZOT_UI_COLOR": "always"}
	if token.Value != "" {
		env["GH_TOKEN"] = token.Value
	}
	workerPath := sandbox.WorkerPath()
	if workerPath == "" {
		return nil, fmt.Errorf("execute run: compute returned no installed worker path")
	}
	// zot takes a work order file, not prose argv. The mission is rendered to
	// order YAML - a mission that already is one passes through intact, so the
	// worker form can carry a full order - and written into the sandbox through
	// the environment, which unlike shell interpolation carries arbitrary text
	// without quoting rules getting a say.
	writeOrder := maps.Clone(env)
	writeOrder["ZOT_DISPATCH_ORDER"] = order.FromText(execution.Mission).Encode()
	code, err := sandbox.Exec(ctx,
		[]string{"/bin/sh", "-c", `printf '%s' "$ZOT_DISPATCH_ORDER" > ` + orderPath}, writeOrder, d.Output)
	if err != nil {
		return nil, fmt.Errorf("write order: %w", err)
	}
	if code != 0 {
		return nil, fmt.Errorf("write order: exit code %d", code)
	}
	code, err = sandbox.Exec(ctx, []string{workerPath, orderPath}, env, d.Output)
	if err != nil {
		return nil, fmt.Errorf("execute run: %w", err)
	}
	return &Result{ExitCode: code}, nil
}

// orderPath is where the run's work order lands inside the sandbox: outside the
// workspace, so the order is not something the agent finds in its own tree.
const orderPath = "/tmp/zot-order.yaml"
