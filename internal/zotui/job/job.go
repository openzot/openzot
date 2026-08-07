// Package job dispatches a coding job onto a sandbox.
//
// The flow is the heart of zotui: resolve the job's environment, mint a repo-scoped
// GitHub token, boot a sandbox from that environment, run zot against the repository
// inside it, then tear the sandbox down. The sandbox never holds a long-lived
// credential - only the minted, expiring token for this one job.
package job

import (
	"context"
	"fmt"
	"io"

	"github.com/openzot/openzot/internal/zotui/runner"
	"github.com/openzot/openzot/internal/zotui/source"
)

// Job is a unit of work: a task for zot to perform on a repository (from a named
// source), run on a named environment with a chosen model.
type Job struct {
	Source      string // which repository provider the repo lives in
	Repository  string // owner/name within the source
	Task        string
	Environment string // which environment to dispatch onto
	Model       string // which model zot reasons with (a configured model name)
}

// Resolver turns a job into the pieces a dispatch needs. It is implemented by the
// caller (which holds the loaded config), so this package stays free of
// config-shape details.
type Resolver interface {
	// Resolve returns the runner and the spec (image, env vars, model config) the
	// job's sandbox should boot with.
	Resolve(j Job) (runner.Runner, runner.Spec, error)
}

// Dispatcher runs jobs.
type Dispatcher struct {
	Source   source.Source // mints per-job repo credentials
	Resolver Resolver      // resolves a job to its runner + spec
	Output   io.Writer     // where the run's output is streamed
}

// Result is how a job settled.
type Result struct {
	ExitCode int
	Reason   string
}

// Dispatch runs a job end to end.
func (d *Dispatcher) Dispatch(ctx context.Context, j Job) (*Result, error) {
	// 1. Resolve the job -> runner + spec (base image, env vars, model config).
	rn, spec, err := d.Resolver.Resolve(j)
	if err != nil {
		return nil, fmt.Errorf("resolve job: %w", err)
	}

	// 2. Mint a short-lived, repo-scoped GitHub token for this job.
	token, err := d.Source.MintToken(ctx, []string{j.Repository})
	if err != nil {
		return nil, fmt.Errorf("mint credentials: %w", err)
	}

	// 3. Boot the sandbox from the environment's spec.
	sb, err := rn.Create(ctx, spec)
	if err != nil {
		return nil, fmt.Errorf("create sandbox: %w", err)
	}
	defer func() { _ = sb.Destroy(ctx) }()

	// 4. Run zot in the sandbox: it clones the repo with the minted token, then
	//    works the task. The token lives only in this job's environment and
	//    expires within the hour.
	env := map[string]string{
		"GH_TOKEN": token.Value,
		"ZOT_REPO": j.Repository,
	}
	code, err := sb.Exec(ctx, zotCommand(j), env, d.Output)
	if err != nil {
		return nil, fmt.Errorf("run job: %w", err)
	}

	return &Result{ExitCode: code}, nil
}

// zotCommand is the command run inside the sandbox.
func zotCommand(j Job) []string {
	// TODO: assemble the real bootstrap - clone the repo over GH_TOKEN, cd into
	// it, then `zot "<task>"`. For now this names the shape.
	return []string{"zot", j.Task}
}
