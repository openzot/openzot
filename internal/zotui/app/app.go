// Package app implements the command center's worker and run lifecycle.
package app

import (
	"context"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/openzot/openzot/internal/zotui/compute"
	"github.com/openzot/openzot/internal/zotui/compute/cloudflare"
	dockercompute "github.com/openzot/openzot/internal/zotui/compute/docker"
	"github.com/openzot/openzot/internal/zotui/config"
	"github.com/openzot/openzot/internal/zotui/dispatch"
	"github.com/openzot/openzot/internal/zotui/ghapp"
	"github.com/openzot/openzot/internal/zotui/repo"
	localrepo "github.com/openzot/openzot/internal/zotui/repo/local"
	"github.com/openzot/openzot/internal/zotui/store"
)

// App wires configuration, durable state, and remote execution together.
type App struct {
	cfg   *config.Config
	store store.Store

	mu        sync.Mutex
	repoCache map[string]repo.Provider
	cancels   map[string]*runCancel
}

type runCancel struct{ cancel context.CancelFunc }

const DefaultMaxIterations = 1_000_000

func New(cfg *config.Config, st store.Store) *App {
	return &App{cfg: cfg, store: st, repoCache: map[string]repo.Provider{}, cancels: map[string]*runCancel{}}
}

type Choices struct {
	Repos                []string            `json:"repos"`
	Repositories         map[string][]string `json:"repositories"`
	Environments         []string            `json:"environments"`
	Models               []string            `json:"models"`
	DefaultMaxIterations int                 `json:"defaultMaxIterations"`
}

func (a *App) Choices() Choices {
	repositories := make(map[string][]string, len(a.cfg.Repos))
	for name, configured := range a.cfg.Repos {
		repositories[name] = slices.Clone(configured.Repositories)
		sort.Strings(repositories[name])
	}
	return Choices{Repos: sortedKeys(a.cfg.Repos), Repositories: repositories,
		Environments: sortedKeys(a.cfg.Environments), Models: sortedKeys(a.cfg.Models),
		DefaultMaxIterations: DefaultMaxIterations}
}

type WorkerParams struct {
	Name          string         `json:"name"`
	Repo          string         `json:"repo"`
	Repository    string         `json:"repository"`
	Environment   string         `json:"environment"`
	Model         string         `json:"model"`
	Mission       string         `json:"mission"`
	MaxIterations int            `json:"maxIterations"`
	Schedule      store.Schedule `json:"schedule"`
}

func (a *App) CreateWorker(ctx context.Context, p WorkerParams) (string, error) {
	w, err := a.buildWorker(p)
	if err != nil {
		return "", err
	}
	if err := a.authorize(ctx, workerExecution(w)); err != nil {
		return "", err
	}
	return a.store.CreateWorker(ctx, w)
}

func (a *App) UpdateWorker(ctx context.Context, id string, p WorkerParams) error {
	current, err := a.store.GetWorker(ctx, id)
	if err != nil {
		return err
	}
	w, err := a.buildWorker(p)
	if err != nil {
		return err
	}
	w.ID, w.CreatedAt = id, current.CreatedAt
	if err := a.authorize(ctx, workerExecution(w)); err != nil {
		return err
	}
	return a.store.UpdateWorker(ctx, w)
}

func (a *App) DeleteWorker(ctx context.Context, id string) error {
	runs, err := a.store.ListRuns(ctx, id)
	if err != nil {
		return err
	}
	for _, r := range runs {
		if !r.Status.Terminal() {
			return fmt.Errorf("worker has an active run")
		}
	}
	return a.store.DeleteWorker(ctx, id)
}

func (a *App) Worker(ctx context.Context, id string) (*store.Worker, error) {
	return a.store.GetWorker(ctx, id)
}
func (a *App) Workers(ctx context.Context) ([]store.Worker, error)    { return a.store.ListWorkers(ctx) }
func (a *App) Run(ctx context.Context, id string) (*store.Run, error) { return a.store.GetRun(ctx, id) }
func (a *App) Runs(ctx context.Context, workerID string) ([]store.Run, error) {
	return a.store.ListRuns(ctx, workerID)
}
func (a *App) RunOutput(ctx context.Context, id string) (string, error) {
	return a.store.RunOutput(ctx, id)
}

func (a *App) StartRun(ctx context.Context, workerID string) (string, error) {
	w, err := a.store.GetWorker(ctx, workerID)
	if err != nil {
		return "", err
	}
	if err := a.authorize(ctx, workerExecution(*w)); err != nil {
		return "", err
	}
	id, err := a.store.CreateRun(ctx, store.Run{WorkerID: w.ID, Status: store.RunScheduled,
		Mission: w.Mission, Model: w.Model, MaxIterations: w.MaxIterations})
	if err != nil {
		return "", fmt.Errorf("start worker: %w", err)
	}
	a.launch(id, *w)
	return id, nil
}

// Pause stops the current remote execution but preserves the run. Resume boots a
// fresh sandbox for the same run; checkpoint-aware compute can refine this seam.
func (a *App) PauseRun(ctx context.Context, id string) error {
	r, err := a.store.GetRun(ctx, id)
	if err != nil {
		return err
	}
	if r.Status != store.RunRunning && r.Status != store.RunScheduled {
		return fmt.Errorf("run cannot be paused while %s", r.Status)
	}
	a.cancel(id)
	return a.store.SetRunStatus(ctx, id, store.RunPaused, r.ExitCode, "")
}

func (a *App) ResumeRun(ctx context.Context, id string) error {
	r, err := a.store.GetRun(ctx, id)
	if err != nil {
		return err
	}
	if r.Status != store.RunPaused {
		return fmt.Errorf("run cannot be resumed while %s", r.Status)
	}
	w, err := a.store.GetWorker(ctx, r.WorkerID)
	if err != nil {
		return err
	}
	if err := a.store.SetRunStatus(ctx, id, store.RunScheduled, nil, ""); err != nil {
		return err
	}
	a.launch(id, *w)
	return nil
}

func (a *App) StopRun(ctx context.Context, id string) error {
	r, err := a.store.GetRun(ctx, id)
	if err != nil {
		return err
	}
	if r.Status.Terminal() {
		return fmt.Errorf("run is already %s", r.Status)
	}
	a.cancel(id)
	return a.store.SetRunStatus(ctx, id, store.RunStopped, r.ExitCode, "stopped by user")
}

func (a *App) launch(id string, w store.Worker) {
	ctx, cancel := context.WithCancel(context.Background())
	handle := &runCancel{cancel: cancel}
	a.mu.Lock()
	a.cancels[id] = handle
	a.mu.Unlock()
	go a.dispatch(ctx, id, workerExecution(w), handle)
}

func (a *App) cancel(id string) {
	a.mu.Lock()
	handle := a.cancels[id]
	a.mu.Unlock()
	if handle != nil {
		handle.cancel()
	}
}

func (a *App) release(id string, handle *runCancel) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cancels[id] == handle {
		delete(a.cancels, id)
	}
}

func (a *App) dispatch(ctx context.Context, id string, execution dispatch.Execution, handle *runCancel) {
	defer a.release(id, handle)
	if ctx.Err() != nil {
		return
	}
	if err := a.store.SetRunStatus(context.Background(), id, store.RunRunning, nil, ""); err != nil {
		return
	}
	writer := &runWriter{ctx: context.Background(), store: a.store, runID: id}
	rp, err := a.repoFor(execution.Repo)
	if err == nil {
		d := &dispatch.Dispatcher{Repo: rp, Resolver: a, Output: writer}
		var res *dispatch.Result
		res, err = d.Dispatch(ctx, execution)
		if err == nil && res != nil && res.ExitCode != 0 {
			err = fmt.Errorf("zot exited with code %d", res.ExitCode)
		}
		if res != nil {
			code := res.ExitCode
			if err == nil {
				_ = a.finishRun(id, store.RunSucceeded, &code, "")
			} else {
				_ = a.finishRun(id, store.RunFailed, &code, err.Error())
			}
		}
	}
	if err != nil {
		_ = a.finishRun(id, store.RunFailed, nil, err.Error())
	}
}

func (a *App) finishRun(id string, status store.RunStatus, code *int, reason string) error {
	r, err := a.store.GetRun(context.Background(), id)
	if err != nil || r.Status == store.RunPaused || r.Status == store.RunStopped {
		return err
	}
	return a.store.SetRunStatus(context.Background(), id, status, code, reason)
}

type runWriter struct {
	ctx   context.Context
	store store.Store
	runID string
}

func (w *runWriter) Write(p []byte) (int, error) {
	if err := w.store.AppendRunOutput(w.ctx, w.runID, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (a *App) buildWorker(p WorkerParams) (store.Worker, error) {
	if strings.TrimSpace(p.Name) == "" {
		return store.Worker{}, fmt.Errorf("a worker name is required")
	}
	if strings.TrimSpace(p.Mission) == "" {
		return store.Worker{}, fmt.Errorf("a mission is required")
	}
	rc, ok := a.cfg.Repos[p.Repo]
	if !ok {
		return store.Worker{}, fmt.Errorf("unknown repo %q", p.Repo)
	}
	if rc.Type == "local" && strings.TrimSpace(rc.Path) == "" {
		return store.Worker{}, fmt.Errorf("local repo %q requires a path", p.Repo)
	}
	if p.Repository == "" {
		return store.Worker{}, fmt.Errorf("a repository is required")
	}
	env, ok := a.cfg.Environments[p.Environment]
	if !ok {
		return store.Worker{}, fmt.Errorf("unknown environment %q", p.Environment)
	}
	if p.Model == "" {
		p.Model = env.Model
	}
	if _, ok := a.cfg.Models[p.Model]; !ok {
		return store.Worker{}, fmt.Errorf("unknown model %q", p.Model)
	}
	if p.MaxIterations <= 0 {
		p.MaxIterations = DefaultMaxIterations
	}
	if err := validateSchedule(p.Schedule); err != nil {
		return store.Worker{}, err
	}
	return store.Worker{Name: strings.TrimSpace(p.Name), Repo: p.Repo, Repository: p.Repository,
		Environment: p.Environment, Model: p.Model, Mission: strings.TrimSpace(p.Mission),
		MaxIterations: p.MaxIterations, Schedule: p.Schedule}, nil
}

func workerExecution(w store.Worker) dispatch.Execution {
	return dispatch.Execution{Repo: w.Repo, Repository: w.Repository, Mission: w.Mission, Environment: w.Environment,
		Model: w.Model, MaxIterations: w.MaxIterations}
}

func (a *App) authorize(ctx context.Context, execution dispatch.Execution) error {
	qualified := execution.Repo + "/" + execution.Repository
	if envLock := a.cfg.Environments[execution.Environment].Repositories; len(envLock) > 0 && !slices.Contains(envLock, qualified) {
		return fmt.Errorf("repository %q is not allowed on environment %q", qualified, execution.Environment)
	}
	if repoLock := a.cfg.Repos[execution.Repo].Repositories; len(repoLock) > 0 {
		if !slices.Contains(repoLock, execution.Repository) {
			return fmt.Errorf("repository %q is not in repo %q's lockdown", execution.Repository, execution.Repo)
		}
		return nil
	}
	rp, err := a.repoFor(execution.Repo)
	if err != nil {
		return err
	}
	repositories, err := rp.ListRepositories(ctx)
	if err != nil {
		return fmt.Errorf("discover repositories: %w", err)
	}
	if !slices.Contains(repositories, execution.Repository) {
		return fmt.Errorf("repository %q is not available from repo %q", execution.Repository, execution.Repo)
	}
	return nil
}

func (a *App) Resolve(execution dispatch.Execution) (compute.Provider, compute.Spec, error) {
	env, ok := a.cfg.Environments[execution.Environment]
	if !ok {
		return nil, compute.Spec{}, fmt.Errorf("unknown environment %q", execution.Environment)
	}
	provider, ok := a.cfg.Compute[env.Compute]
	if !ok {
		return nil, compute.Spec{}, fmt.Errorf("environment %q references unknown compute %q", execution.Environment, env.Compute)
	}
	m, ok := a.cfg.Models[execution.Model]
	if !ok {
		return nil, compute.Spec{}, fmt.Errorf("unknown model %q", execution.Model)
	}
	var driver compute.Provider
	switch provider.Type {
	case "cloudflare":
		driver = cloudflare.New(provider.AccountID, provider.APIToken)
	case "docker":
		driver = dockercompute.New()
	default:
		return nil, compute.Spec{}, fmt.Errorf("compute type %q not supported yet", provider.Type)
	}
	var mounts []compute.Mount
	if rc := a.cfg.Repos[execution.Repo]; rc.Type == "local" {
		mounts = []compute.Mount{{Source: rc.Path, Target: "/workspace"}}
	}
	return driver, compute.Spec{Image: env.Image, Env: env.Env, Mounts: mounts, MaxIterations: execution.MaxIterations,
		Model: compute.ModelSpec{Provider: m.Provider, Model: m.Model, APIKey: m.APIKey, BaseURL: m.BaseURL}}, nil
}

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
	if err == nil {
		a.repoCache[name] = rp
	}
	return rp, err
}

func newRepo(r config.Repo) (repo.Provider, error) {
	switch r.Type {
	case "", "github":
		return ghapp.New(r.AppID, r.InstallationID, r.PrivateKey)
	case "gitlab":
		return nil, fmt.Errorf("gitlab repo not implemented yet")
	case "local":
		return localrepo.New(r.Repositories), nil
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

var _ io.Writer = (*runWriter)(nil)
