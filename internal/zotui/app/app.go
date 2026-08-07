// Package app is the zotui engine facade: it validates and schedules jobs,
// dispatches them in the background, tracks them in the store, and exposes the
// config choices the command center needs. The TUI drives everything through it.
package app

import (
	"context"
	"fmt"
	"io"
	"slices"
	"sort"
	"sync"

	"github.com/openzot/openzot/internal/zotui/config"
	"github.com/openzot/openzot/internal/zotui/ghapp"
	"github.com/openzot/openzot/internal/zotui/job"
	"github.com/openzot/openzot/internal/zotui/runner"
	"github.com/openzot/openzot/internal/zotui/runner/cloudflare"
	"github.com/openzot/openzot/internal/zotui/source"
	"github.com/openzot/openzot/internal/zotui/store"
)

// App wires the config and the store into the operations the command center runs.
type App struct {
	cfg   *config.Config
	store store.Store

	mu       sync.Mutex
	srcCache map[string]source.Source
}

// New returns an App over a loaded config and an open store.
func New(cfg *config.Config, st store.Store) *App {
	return &App{cfg: cfg, store: st, srcCache: map[string]source.Source{}}
}

// --- choices the create form offers -----------------------------------------

func (a *App) Sources() []string      { return sortedKeys(a.cfg.Sources) }
func (a *App) Environments() []string { return sortedKeys(a.cfg.Environments) }
func (a *App) Models() []string       { return sortedKeys(a.cfg.Models) }

// DefaultModel returns the environment's default model (empty if none).
func (a *App) DefaultModel(env string) string { return a.cfg.Environments[env].Model }

// --- reading + controlling jobs ---------------------------------------------

// Jobs returns all jobs, newest first.
func (a *App) Jobs(ctx context.Context) ([]store.Job, error) { return a.store.List(ctx) }

// Job returns one job by id.
func (a *App) Job(ctx context.Context, id string) (*store.Job, error) { return a.store.Get(ctx, id) }

// Cancel marks a job cancelled. A terminal job cannot be cancelled.
func (a *App) Cancel(ctx context.Context, id string) error {
	j, err := a.store.Get(ctx, id)
	if err != nil {
		return err
	}
	switch j.Status {
	case store.StatusSettled, store.StatusFailed, store.StatusCancelled:
		return fmt.Errorf("job is already %s", j.Status)
	}
	// TODO: signal the runner to tear the sandbox down. For now, record the intent;
	// a running dispatch checks this status and stops updating.
	return a.store.SetStatus(ctx, id, store.StatusCancelled, nil)
}

// --- scheduling --------------------------------------------------------------

// ScheduleParams is a new-job request from the command center.
type ScheduleParams struct {
	Source      string
	Repository  string
	Environment string
	Model       string // optional; empty uses the environment default
	Task        string
}

// Schedule validates and records a new job, then dispatches it in the background,
// returning the new job id immediately. The command center sees it appear as
// "scheduled" and watches it progress.
func (a *App) Schedule(ctx context.Context, p ScheduleParams) (string, error) {
	j, err := a.buildJob(p)
	if err != nil {
		return "", err
	}
	if err := a.authorize(ctx, j); err != nil {
		return "", err
	}

	id, err := a.store.Create(ctx, store.Job{
		Source:      j.Source,
		Repository:  j.Repository,
		Task:        j.Task,
		Environment: j.Environment,
		Model:       j.Model,
		Status:      store.StatusScheduled,
	})
	if err != nil {
		return "", err
	}

	go a.dispatch(context.Background(), id, j)
	return id, nil
}

// dispatch runs a scheduled job to completion, updating the store as it goes. It
// leaves a job that was cancelled meanwhile untouched.
func (a *App) dispatch(ctx context.Context, id string, j job.Job) {
	if a.cancelled(ctx, id) {
		return
	}
	_ = a.store.SetStatus(ctx, id, store.StatusRunning, nil)

	src, err := a.sourceFor(j.Source)
	if err != nil {
		a.settle(ctx, id, store.StatusFailed, nil)
		return
	}

	d := &job.Dispatcher{Source: src, Resolver: a, Output: io.Discard}
	res, derr := d.Dispatch(ctx, j)

	status, code := store.StatusSettled, (*int)(nil)
	if res != nil {
		code = &res.ExitCode
		if res.ExitCode != 0 {
			status = store.StatusFailed
		}
	}
	if derr != nil {
		status = store.StatusFailed
	}
	a.settle(ctx, id, status, code)
}

// settle records a terminal status unless the job was cancelled meanwhile.
func (a *App) settle(ctx context.Context, id string, status store.Status, code *int) {
	if a.cancelled(ctx, id) {
		return
	}
	_ = a.store.SetStatus(ctx, id, status, code)
}

func (a *App) cancelled(ctx context.Context, id string) bool {
	j, err := a.store.Get(ctx, id)
	return err == nil && j.Status == store.StatusCancelled
}

// --- validation, authorization, resolution (shared with a scripted path) -----

func (a *App) buildJob(p ScheduleParams) (job.Job, error) {
	if p.Task == "" {
		return job.Job{}, fmt.Errorf("a task is required")
	}
	if _, ok := a.cfg.Sources[p.Source]; !ok {
		return job.Job{}, fmt.Errorf("unknown source %q", p.Source)
	}
	if p.Repository == "" {
		return job.Job{}, fmt.Errorf("a repository is required")
	}
	env, ok := a.cfg.Environments[p.Environment]
	if !ok {
		return job.Job{}, fmt.Errorf("unknown environment %q", p.Environment)
	}
	model := p.Model
	if model == "" {
		model = env.Model
	}
	if model == "" {
		return job.Job{}, fmt.Errorf("no model for environment %q (choose one)", p.Environment)
	}
	if _, ok := a.cfg.Models[model]; !ok {
		return job.Job{}, fmt.Errorf("unknown model %q", model)
	}
	return job.Job{Source: p.Source, Repository: p.Repository, Task: p.Task, Environment: p.Environment, Model: model}, nil
}

// authorize applies the environment and per-source lockdowns, then discovery.
func (a *App) authorize(ctx context.Context, j job.Job) error {
	qualified := j.Source + "/" + j.Repository

	if envLock := a.cfg.Environments[j.Environment].Repositories; len(envLock) > 0 {
		if !slices.Contains(envLock, qualified) {
			return fmt.Errorf("repository %q is not allowed on environment %q", qualified, j.Environment)
		}
	}
	if srcLock := a.cfg.Sources[j.Source].Repositories; len(srcLock) > 0 {
		if !slices.Contains(srcLock, j.Repository) {
			return fmt.Errorf("repository %q is not in source %q's lockdown", j.Repository, j.Source)
		}
		return nil
	}

	src, err := a.sourceFor(j.Source)
	if err != nil {
		return err
	}
	repos, err := src.ListRepositories(ctx)
	if err != nil {
		return fmt.Errorf("discover repositories: %w", err)
	}
	if !slices.Contains(repos, j.Repository) {
		return fmt.Errorf("repository %q is not available from source %q", j.Repository, j.Source)
	}
	return nil
}

// Resolve implements job.Resolver: it turns a job into its runner and spec.
func (a *App) Resolve(j job.Job) (runner.Runner, runner.Spec, error) {
	env, ok := a.cfg.Environments[j.Environment]
	if !ok {
		return nil, runner.Spec{}, fmt.Errorf("unknown environment %q", j.Environment)
	}
	rn, ok := a.cfg.Runners[env.Runner]
	if !ok {
		return nil, runner.Spec{}, fmt.Errorf("environment %q references unknown runner %q", j.Environment, env.Runner)
	}
	m, ok := a.cfg.Models[j.Model]
	if !ok {
		return nil, runner.Spec{}, fmt.Errorf("unknown model %q", j.Model)
	}

	var drv runner.Runner
	switch rn.Type {
	case "cloudflare":
		drv = cloudflare.New(rn.AccountID, rn.APIToken)
	default:
		return nil, runner.Spec{}, fmt.Errorf("runner type %q not supported yet", rn.Type)
	}

	spec := runner.Spec{
		Image: env.Image,
		Env:   env.Env,
		Model: runner.ModelSpec{Provider: m.Provider, Model: m.Model, APIKey: m.APIKey, BaseURL: m.BaseURL},
	}
	return drv, spec, nil
}

// sourceFor builds (and caches) the repository provider for a source name.
func (a *App) sourceFor(name string) (source.Source, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if s, ok := a.srcCache[name]; ok {
		return s, nil
	}
	sc, ok := a.cfg.Sources[name]
	if !ok {
		return nil, fmt.Errorf("unknown source %q", name)
	}
	s, err := newSource(sc)
	if err != nil {
		return nil, err
	}
	a.srcCache[name] = s
	return s, nil
}

// newSource builds one repository provider from its config.
func newSource(s config.Source) (source.Source, error) {
	switch s.Type {
	case "", "github":
		return ghapp.New(s.AppID, s.InstallationID, s.PrivateKey)
	case "gitlab":
		return nil, fmt.Errorf("gitlab source not implemented yet")
	default:
		return nil, fmt.Errorf("unsupported source type %q", s.Type)
	}
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
