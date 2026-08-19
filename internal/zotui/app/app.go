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
	"time"

	"github.com/openzot/openzot/internal/zotui/compute"
	dockercompute "github.com/openzot/openzot/internal/zotui/compute/docker"
	vercelcompute "github.com/openzot/openzot/internal/zotui/compute/vercel"
	"github.com/openzot/openzot/internal/zotui/config"
	"github.com/openzot/openzot/internal/zotui/dispatch"
	"github.com/openzot/openzot/internal/zotui/ghapp"
	"github.com/openzot/openzot/internal/zotui/repo"
	githubrepo "github.com/openzot/openzot/internal/zotui/repo/github"
	localrepo "github.com/openzot/openzot/internal/zotui/repo/local"
	"github.com/openzot/openzot/internal/zotui/store"
	workerbin "github.com/openzot/openzot/internal/zotui/worker"
)

// App wires configuration, durable state, and remote execution together.
type App struct {
	cfg   *config.Config
	store store.Store

	mu              sync.Mutex
	repoCache       map[string]repo.Provider
	repositoryCache map[string]repositoryChoices
	cancels         map[string]*runCancel
	closed          bool

	// runMu serialises every run status transition. Each transition reads the
	// status it is allowed to leave and writes the next one; without the lock a
	// control action and a finishing dispatch decide against a status the other
	// is about to replace, and the loser's write silently wins.
	runMu sync.Mutex

	// worker resolves the zot executable a sandbox is given, keyed by platform.
	worker func(string) (compute.Worker, error)
}

// runCancel stops one dispatch goroutine and reports when it has torn its
// sandbox down; Shutdown waits on done so Ctrl-C does not orphan a container.
type runCancel struct {
	cancel context.CancelFunc
	done   chan struct{}
}

type repositoryChoices struct {
	repositories []string
	expires      time.Time
}

const DefaultMaxIterations = 1_000_000

const repositoryCacheTTL = 5 * time.Minute

func New(cfg *config.Config, st store.Store) *App {
	return &App{cfg: cfg, store: st, repoCache: map[string]repo.Provider{},
		repositoryCache: map[string]repositoryChoices{}, cancels: map[string]*runCancel{}, worker: workerbin.Load}
}

type Choices struct {
	Repos                     []string                       `json:"repos"`
	Repositories              map[string][]string            `json:"repositories"`
	RepositoriesByEnvironment map[string]map[string][]string `json:"repositoriesByEnvironment"`
	Environments              []string                       `json:"environments"`
	Providers                 []string                       `json:"providers"`
	Models                    map[string][]string            `json:"models"`
	DefaultMaxIterations      int                            `json:"defaultMaxIterations"`
}

func (a *App) Choices(ctx context.Context) (Choices, error) {
	repositories := make(map[string][]string, len(a.cfg.Repos))
	for _, name := range sortedKeys(a.cfg.Repos) {
		discovered, err := a.repositoriesFor(ctx, name)
		if err != nil {
			return Choices{}, fmt.Errorf("repositories for %q: %w", name, err)
		}
		repositories[name] = discovered
	}
	models := make(map[string][]string, len(a.cfg.Providers))
	for _, provider := range sortedKeys(a.cfg.Providers) {
		models[provider] = a.cfg.ProviderModels(provider)
	}
	return Choices{Repos: sortedKeys(a.cfg.Repos), Repositories: repositories,
		RepositoriesByEnvironment: a.repositoriesByEnvironment(repositories),
		Environments:              sortedKeys(a.cfg.Environments), Providers: sortedKeys(a.cfg.Providers), Models: models,
		DefaultMaxIterations: DefaultMaxIterations}, nil
}

func (a *App) repositoriesByEnvironment(repositories map[string][]string) map[string]map[string][]string {
	choices := make(map[string]map[string][]string, len(a.cfg.Environments))
	for _, environmentName := range sortedKeys(a.cfg.Environments) {
		environment := a.cfg.Environments[environmentName]
		allowed := make(map[string][]string)
		for _, repoName := range sortedKeys(a.cfg.Repos) {
			repoConfig := a.cfg.Repos[repoName]
			if repoConfig.Type == "local" && a.cfg.Compute[environment.Compute].Type != "docker" {
				continue
			}
			for _, repository := range repositories[repoName] {
				qualified := repoName + "/" + repository
				if len(environment.Repositories) == 0 || slices.Contains(environment.Repositories, qualified) {
					allowed[repoName] = append(allowed[repoName], repository)
				}
			}
		}
		choices[environmentName] = allowed
	}
	return choices
}

type WorkerParams struct {
	Name          string         `json:"name"`
	Repo          string         `json:"repo"`
	Repository    string         `json:"repository"`
	Environment   string         `json:"environment"`
	Provider      string         `json:"provider"`
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
func (a *App) RunOutput(ctx context.Context, id string, offset int64) (store.Output, error) {
	return a.store.RunOutput(ctx, id, offset)
}

// Reconcile fails the runs a previous process left mid-flight. Their sandboxes
// died with that process, so nothing can adopt them; leaving the rows "running"
// would show phantom activity and block the worker's next run for ever. Paused
// runs are deliberately untouched - a pause is meant to survive a restart.
func (a *App) Reconcile(ctx context.Context) (int, error) {
	runs, err := a.store.ActiveRuns(ctx)
	if err != nil {
		return 0, err
	}
	reconciled := 0
	for _, r := range runs {
		if r.Status != store.RunRunning && r.Status != store.RunScheduled {
			continue
		}
		if err := a.store.SetRunStatus(ctx, r.ID, store.RunFailed, nil, "interrupted by a zotui restart"); err != nil {
			return reconciled, err
		}
		reconciled++
	}
	return reconciled, nil
}

// Shutdown cancels every in-flight run and waits for its dispatch goroutine to
// tear the sandbox down, refusing new launches meanwhile. Without it Ctrl-C
// leaves sandboxes running with the run's repository token still inside them.
func (a *App) Shutdown(ctx context.Context) error {
	a.mu.Lock()
	a.closed = true
	handles := make([]*runCancel, 0, len(a.cancels))
	for _, handle := range a.cancels {
		handles = append(handles, handle)
	}
	a.mu.Unlock()
	for _, handle := range handles {
		handle.cancel()
	}
	for _, handle := range handles {
		select {
		case <-handle.done:
		case <-ctx.Done():
			return fmt.Errorf("shutdown: %d run(s) still tearing down: %w", len(handles), ctx.Err())
		}
	}
	return nil
}

func (a *App) StartRun(ctx context.Context, workerID string) (string, error) {
	w, err := a.store.GetWorker(ctx, workerID)
	if err != nil {
		return "", err
	}
	if err := a.authorize(ctx, workerExecution(*w)); err != nil {
		return "", err
	}
	modelProvider := w.Provider
	if modelProvider == "" {
		modelProvider = a.cfg.Environments[w.Environment].Provider
	}
	id, err := a.store.CreateRun(ctx, store.Run{WorkerID: w.ID, Status: store.RunScheduled,
		Mission: w.Mission, Provider: modelProvider, Model: w.Model, MaxIterations: w.MaxIterations})
	if err != nil {
		return "", fmt.Errorf("start worker: %w", err)
	}
	a.launch(id, *w)
	return id, nil
}

// Pause stops the current remote execution but preserves the run. Resume boots a
// fresh sandbox for the same run; checkpoint-aware compute can refine this seam.
//
// The status is written before the cancellation so the dispatch goroutine can
// only ever see an already-paused run: cancelling first let it record a genuine
// failure that the pending pause then overwrote, hiding the error behind a run
// that looked resumable.
func (a *App) PauseRun(ctx context.Context, id string) error {
	a.runMu.Lock()
	r, err := a.store.GetRun(ctx, id)
	if err == nil && r.Status != store.RunRunning && r.Status != store.RunScheduled {
		err = fmt.Errorf("run cannot be paused while %s", r.Status)
	}
	if err == nil {
		err = a.store.SetRunStatus(ctx, id, store.RunPaused, r.ExitCode, "")
	}
	a.runMu.Unlock()
	if err != nil {
		return err
	}
	a.cancel(id)
	return nil
}

func (a *App) ResumeRun(ctx context.Context, id string) error {
	a.runMu.Lock()
	var w *store.Worker
	r, err := a.store.GetRun(ctx, id)
	if err == nil && r.Status != store.RunPaused {
		err = fmt.Errorf("run cannot be resumed while %s", r.Status)
	}
	if err == nil {
		w, err = a.store.GetWorker(ctx, r.WorkerID)
	}
	if err == nil {
		// Claiming the run inside the lock is what makes a duplicate resume fail
		// instead of booting a second sandbox for the same run.
		err = a.store.SetRunStatus(ctx, id, store.RunScheduled, nil, "")
	}
	a.runMu.Unlock()
	if err != nil {
		return err
	}
	a.launch(id, *w)
	return nil
}

func (a *App) StopRun(ctx context.Context, id string) error {
	a.runMu.Lock()
	r, err := a.store.GetRun(ctx, id)
	if err == nil && r.Status.Terminal() {
		err = fmt.Errorf("run is already %s", r.Status)
	}
	if err == nil {
		err = a.store.SetRunStatus(ctx, id, store.RunStopped, r.ExitCode, "stopped by user")
	}
	a.runMu.Unlock()
	if err != nil {
		return err
	}
	a.cancel(id)
	return nil
}

func (a *App) launch(id string, w store.Worker) {
	ctx, cancel := context.WithCancel(context.Background())
	handle := &runCancel{cancel: cancel, done: make(chan struct{})}
	a.mu.Lock()
	// A run already in flight keeps its handle: replacing it would strand the
	// running goroutine's sandbox with nothing left able to cancel it.
	if a.closed || a.cancels[id] != nil {
		a.mu.Unlock()
		cancel()
		close(handle.done)
		return
	}
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
	if a.cancels[id] == handle {
		delete(a.cancels, id)
	}
	a.mu.Unlock()
	close(handle.done)
}

func (a *App) dispatch(ctx context.Context, id string, execution dispatch.Execution, handle *runCancel) {
	defer a.release(id, handle)
	if ctx.Err() != nil {
		return
	}
	if err := a.markRunning(id); err != nil {
		return
	}
	writer := &runWriter{ctx: context.Background(), store: a.store, runID: id}
	// Every path below settles the run exactly once. Settling twice erased the
	// exit code, because the second write carried none and the store stores what
	// it is given.
	status, reason := store.RunSucceeded, ""
	var code *int
	rp, err := a.repoFor(execution.Repo)
	if err == nil {
		d := &dispatch.Dispatcher{Repo: rp, Resolver: a, Worker: a.worker, Output: writer}
		var res *dispatch.Result
		res, err = d.Dispatch(ctx, execution)
		if res != nil {
			code = &res.ExitCode
			if err == nil && res.ExitCode != 0 {
				err = fmt.Errorf("zot exited with code %d", res.ExitCode)
			}
		}
	}
	if err != nil {
		status, reason = store.RunFailed, err.Error()
	}
	_ = a.finishRun(id, status, code, reason)
}

// markRunning claims a scheduled run. A run paused or stopped between launch and
// dispatch stays that way, and the sandbox is never created.
func (a *App) markRunning(id string) error {
	a.runMu.Lock()
	defer a.runMu.Unlock()
	r, err := a.store.GetRun(context.Background(), id)
	if err != nil {
		return err
	}
	if r.Status != store.RunScheduled {
		return fmt.Errorf("run is %s", r.Status)
	}
	return a.store.SetRunStatus(context.Background(), id, store.RunRunning, nil, "")
}

// finishRun records the outcome unless the run has already settled or been
// paused by the operator - the cancellation it is reporting is theirs.
func (a *App) finishRun(id string, status store.RunStatus, code *int, reason string) error {
	a.runMu.Lock()
	defer a.runMu.Unlock()
	r, err := a.store.GetRun(context.Background(), id)
	if err != nil || r.Status.Terminal() || r.Status == store.RunPaused {
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
	computeConfig, ok := a.cfg.Compute[env.Compute]
	if !ok {
		return store.Worker{}, fmt.Errorf("environment %q references unknown compute %q", p.Environment, env.Compute)
	}
	if rc.Type == "local" && computeConfig.Type != "docker" {
		return store.Worker{}, fmt.Errorf("local repo %q requires docker compute; %s compute needs a remote repo connection", p.Repo, computeConfig.Type)
	}
	if p.Provider == "" {
		p.Provider = env.Provider
	}
	if p.Model == "" {
		p.Model = env.Model
	}
	if _, _, ok := a.cfg.ResolveModel(p.Provider, p.Model); !ok {
		return store.Worker{}, fmt.Errorf("unknown model %q for provider %q", p.Model, p.Provider)
	}
	if p.MaxIterations <= 0 {
		p.MaxIterations = DefaultMaxIterations
	}
	if err := validateSchedule(p.Schedule); err != nil {
		return store.Worker{}, err
	}
	return store.Worker{Name: strings.TrimSpace(p.Name), Repo: p.Repo, Repository: p.Repository,
		Environment: p.Environment, Provider: p.Provider, Model: p.Model, Mission: strings.TrimSpace(p.Mission),
		MaxIterations: p.MaxIterations, Schedule: p.Schedule}, nil
}

func workerExecution(w store.Worker) dispatch.Execution {
	return dispatch.Execution{Repo: w.Repo, Repository: w.Repository, Mission: w.Mission, Environment: w.Environment,
		Provider: w.Provider, Model: w.Model, MaxIterations: w.MaxIterations}
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
	}
	repositories, err := a.repositoriesFor(ctx, execution.Repo)
	if err != nil {
		return fmt.Errorf("discover repositories: %w", err)
	}
	if !slices.Contains(repositories, execution.Repository) {
		return fmt.Errorf("repository %q is not available from repo %q", execution.Repository, execution.Repo)
	}
	return nil
}

func (a *App) repositoriesFor(ctx context.Context, name string) ([]string, error) {
	configured, ok := a.cfg.Repos[name]
	if !ok {
		return nil, fmt.Errorf("unknown repo %q", name)
	}
	now := time.Now()
	a.mu.Lock()
	if cached, ok := a.repositoryCache[name]; ok && now.Before(cached.expires) {
		repositories := restrictRepositories(cached.repositories, configured.Repositories)
		a.mu.Unlock()
		return repositories, nil
	}
	a.mu.Unlock()

	provider, err := a.repoFor(name)
	if err != nil {
		return nil, err
	}
	repositories, err := provider.ListRepositories(ctx)
	if err != nil {
		return nil, err
	}
	repositories = slices.Clone(repositories)
	sort.Strings(repositories)
	a.mu.Lock()
	a.repositoryCache[name] = repositoryChoices{repositories: slices.Clone(repositories), expires: now.Add(repositoryCacheTTL)}
	a.mu.Unlock()
	return restrictRepositories(repositories, configured.Repositories), nil
}

func restrictRepositories(discovered, configured []string) []string {
	repositories := slices.Clone(discovered)
	if len(configured) > 0 {
		repositories = slices.DeleteFunc(repositories, func(repository string) bool {
			return !slices.Contains(configured, repository)
		})
	}
	return repositories
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
	if execution.Provider == "" {
		execution.Provider = env.Provider
	}
	modelProvider, modelID, ok := a.cfg.ResolveModel(execution.Provider, execution.Model)
	if !ok {
		return nil, compute.Spec{}, fmt.Errorf("unknown model %q for provider %q", execution.Model, execution.Provider)
	}
	modelDriver := modelProvider.Driver
	if modelDriver == "" {
		modelDriver = execution.Provider
	}
	var driver compute.Provider
	switch provider.Type {
	case "docker":
		driver = dockercompute.New()
	case "vercel":
		driver = vercelcompute.New(provider.Token, provider.TeamID, provider.ProjectID, provider.Timeout, provider.BaseURL)
	default:
		return nil, compute.Spec{}, fmt.Errorf("compute type %q not supported yet", provider.Type)
	}
	var source compute.Source
	if rc := a.cfg.Repos[execution.Repo]; rc.Type == "local" {
		source = compute.Source{LocalPath: rc.Path}
	} else if rc.Type == "" || rc.Type == "github" {
		parts := strings.Split(execution.Repository, "/")
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return nil, compute.Spec{}, fmt.Errorf("GitHub repository %q must be owner/name", execution.Repository)
		}
		source = compute.Source{URL: "https://github.com/" + execution.Repository + ".git",
			Username: "x-access-token", Directory: parts[1]}
	}
	return driver, compute.Spec{Image: env.Image, Platform: driver.Platform(), Env: env.Env, Source: source, MaxIterations: execution.MaxIterations,
		Model: compute.ModelSpec{Provider: modelDriver, Model: modelID, APIKey: modelProvider.APIKey, BaseURL: modelProvider.BaseURL}}, nil
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
		if r.AppID == 0 && r.InstallationID == 0 && strings.TrimSpace(r.PrivateKey) == "" {
			return githubrepo.New(r.Repositories)
		}
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
