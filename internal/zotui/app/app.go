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

	"github.com/openzot/openzot/internal/zotui/compute"
	"github.com/openzot/openzot/internal/zotui/compute/cloudflare"
	"github.com/openzot/openzot/internal/zotui/config"
	"github.com/openzot/openzot/internal/zotui/ghapp"
	"github.com/openzot/openzot/internal/zotui/job"
	"github.com/openzot/openzot/internal/zotui/repo"
	"github.com/openzot/openzot/internal/zotui/store"
)

// App wires the config and the store into the operations the command center runs.
type App struct {
	cfg   *config.Config
	store store.Store

	mu        sync.Mutex
	repoCache map[string]repo.Provider
}

// New returns an App over a loaded config and an open store.
func New(cfg *config.Config, st store.Store) *App {
	return &App{cfg: cfg, store: st, repoCache: map[string]repo.Provider{}}
}

// --- choices the create form offers -----------------------------------------

func (a *App) Repos() []string        { return sortedKeys(a.cfg.Repos) }
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
	// TODO: signal compute to tear the sandbox down. For now, record the intent;
	// a running dispatch checks this status and stops updating.
	return a.store.SetStatus(ctx, id, store.StatusCancelled, nil)
}

// --- scheduling --------------------------------------------------------------

// ScheduleParams is a new-job request from the command center.
type ScheduleParams struct {
	Repo        string
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
		Repo:        j.Repo,
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

	rp, err := a.repoFor(j.Repo)
	if err != nil {
		a.settle(ctx, id, store.StatusFailed, nil)
		return
	}

	d := &job.Dispatcher{Repo: rp, Resolver: a, Output: io.Discard}
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
	if _, ok := a.cfg.Repos[p.Repo]; !ok {
		return job.Job{}, fmt.Errorf("unknown repo %q", p.Repo)
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
	return job.Job{Repo: p.Repo, Repository: p.Repository, Task: p.Task, Environment: p.Environment, Model: model}, nil
}

// authorize applies the environment and per-repo lockdowns, then discovery.
func (a *App) authorize(ctx context.Context, j job.Job) error {
	qualified := j.Repo + "/" + j.Repository

	if envLock := a.cfg.Environments[j.Environment].Repositories; len(envLock) > 0 {
		if !slices.Contains(envLock, qualified) {
			return fmt.Errorf("repository %q is not allowed on environment %q", qualified, j.Environment)
		}
	}
	if repoLock := a.cfg.Repos[j.Repo].Repositories; len(repoLock) > 0 {
		if !slices.Contains(repoLock, j.Repository) {
			return fmt.Errorf("repository %q is not in repo %q's lockdown", j.Repository, j.Repo)
		}
		return nil
	}

	rp, err := a.repoFor(j.Repo)
	if err != nil {
		return err
	}
	repos, err := rp.ListRepositories(ctx)
	if err != nil {
		return fmt.Errorf("discover repositories: %w", err)
	}
	if !slices.Contains(repos, j.Repository) {
		return fmt.Errorf("repository %q is not available from repo %q", j.Repository, j.Repo)
	}
	return nil
}

// Resolve implements job.Resolver: it turns a job into compute and a spec.
func (a *App) Resolve(j job.Job) (compute.Provider, compute.Spec, error) {
	env, ok := a.cfg.Environments[j.Environment]
	if !ok {
		return nil, compute.Spec{}, fmt.Errorf("unknown environment %q", j.Environment)
	}
	provider, ok := a.cfg.Compute[env.Compute]
	if !ok {
		return nil, compute.Spec{}, fmt.Errorf("environment %q references unknown compute %q", j.Environment, env.Compute)
	}
	m, ok := a.cfg.Models[j.Model]
	if !ok {
		return nil, compute.Spec{}, fmt.Errorf("unknown model %q", j.Model)
	}

	var drv compute.Provider
	switch provider.Type {
	case "cloudflare":
		drv = cloudflare.New(provider.AccountID, provider.APIToken)
	default:
		return nil, compute.Spec{}, fmt.Errorf("compute type %q not supported yet", provider.Type)
	}

	spec := compute.Spec{
		Image: env.Image,
		Env:   env.Env,
		Model: compute.ModelSpec{Provider: m.Provider, Model: m.Model, APIKey: m.APIKey, BaseURL: m.BaseURL},
	}
	return drv, spec, nil
}

// repoFor builds (and caches) the provider for a configured repo name.
func (a *App) repoFor(name string) (repo.Provider, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if rp, ok := a.repoCache[name]; ok {
		return rp, nil
	}
	rc, ok := a.cfg.Repos[name]
	if !ok {
		return nil, fmt.Errorf("unknown repo %q", name)
	}
	rp, err := newRepo(rc)
	if err != nil {
		return nil, err
	}
	a.repoCache[name] = rp
	return rp, nil
}

// newRepo builds one repository provider from its config.
func newRepo(r config.Repo) (repo.Provider, error) {
	switch r.Type {
	case "", "github":
		return ghapp.New(r.AppID, r.InstallationID, r.PrivateKey)
	case "gitlab":
		return nil, fmt.Errorf("gitlab repo not implemented yet")
	default:
		return nil, fmt.Errorf("unsupported repo type %q", r.Type)
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
