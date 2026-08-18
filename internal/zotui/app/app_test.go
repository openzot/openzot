package app_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openzot/openzot/internal/zotui/app"
	"github.com/openzot/openzot/internal/zotui/config"
	"github.com/openzot/openzot/internal/zotui/dispatch"
	"github.com/openzot/openzot/internal/zotui/store"
)

func testConfig() *config.Config {
	return &config.Config{
		Repos:   map[string]config.Repo{"acme": {Type: "github", Repositories: []string{"acme/api"}}},
		Compute: map[string]config.Compute{"cf": {Type: "cloudflare"}},
		Providers: map[string]config.Provider{"zai": {Models: map[string]config.Model{
			"glm": {Model: "glm-5.2"},
		}}},
		Environments: map[string]config.Environment{"go": {Compute: "cf", Provider: "zai", Model: "glm", Image: "img"}},
	}
}

func openStore(t *testing.T) store.Store {
	t.Helper()
	st, err := store.Open(store.Config{Driver: "sqlite", DSN: filepath.Join(t.TempDir(), "z.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// Repository choices follow both their connection and environment, and model
// choices follow their provider, so the worker form cannot present invalid combinations.
func TestChoicesGroupRepositoriesAndModelsByProvider(t *testing.T) {
	cfg := &config.Config{
		Repos: map[string]config.Repo{
			"second": {Repositories: []string{"zeta/api", "alpha/web"}},
			"first":  {Type: "local", Repositories: []string{"local/repo"}},
		},
		Compute: map[string]config.Compute{"remote": {Type: "vercel"}},
		Environments: map[string]config.Environment{
			"production": {Compute: "remote", Repositories: []string{"second/alpha/web", "first/local/repo"}},
		},
		Providers: map[string]config.Provider{
			"zai": {Models: map[string]config.Model{
				"glm-5": {Model: "glm-5"},
				"glm-4": {Model: "glm-4"},
			}},
			"anthropic": {Models: map[string]config.Model{
				"sonnet": {Model: "claude-sonnet"},
			}},
		},
	}
	choices, err := app.New(cfg, nil).Choices(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(choices.Repos) != 2 || choices.Repos[0] != "first" || choices.Repos[1] != "second" {
		t.Fatalf("repo connections = %v", choices.Repos)
	}
	if got := choices.Repositories["second"]; len(got) != 2 || got[0] != "alpha/web" || got[1] != "zeta/api" {
		t.Fatalf("second repositories = %v", got)
	}
	if got := choices.Repositories["first"]; len(got) != 1 || got[0] != "local/repo" {
		t.Fatalf("first repositories = %v", got)
	}
	if got := choices.RepositoriesByEnvironment["production"]; len(got) != 1 || len(got["second"]) != 1 || got["second"][0] != "alpha/web" {
		t.Fatalf("environment repository choices = %v", got)
	}
	if len(choices.Providers) != 2 || choices.Providers[0] != "anthropic" || choices.Providers[1] != "zai" {
		t.Fatalf("providers = %v", choices.Providers)
	}
	if got := choices.Models["zai"]; len(got) != 2 || got[0] != "glm-4" || got[1] != "glm-5" {
		t.Fatalf("zai models = %v", got)
	}
	if got := choices.Models["anthropic"]; len(got) != 1 || got[0] != "sonnet" {
		t.Fatalf("anthropic models = %v", got)
	}

	choices.Repositories["second"][0] = "changed/outside"
	choices, err = app.New(cfg, nil).Choices(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := choices.Repositories["second"][0]; got != "alpha/web" {
		t.Fatalf("choices exposed configuration state: %q", got)
	}
}

func TestWorkerLifecycle(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	a := app.New(testConfig(), st)
	id, err := a.CreateWorker(ctx, app.WorkerParams{Name: "API maintainer", Repo: "acme",
		Repository: "acme/api", Environment: "go", Mission: "keep the API healthy",
		Schedule: store.Schedule{Cron: "0 */4 * * *", Timezone: "UTC", RuntimeMinutes: 60}})
	if err != nil {
		t.Fatalf("create worker: %v", err)
	}
	w, err := a.Worker(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if w.Provider != "zai" || w.Model != "glm" || w.MaxIterations != 1_000_000 || w.Schedule.Cron != "0 */4 * * *" {
		t.Fatalf("defaults or schedule lost: %+v", w)
	}
	err = a.UpdateWorker(ctx, id, app.WorkerParams{Name: "API owner", Repo: "acme",
		Repository: "acme/api", Environment: "go", Model: "glm", Mission: "own the API", MaxIterations: 40})
	if err != nil {
		t.Fatalf("update worker: %v", err)
	}
	w, _ = a.Worker(ctx, id)
	if w.Name != "API owner" || w.Mission != "own the API" || w.MaxIterations != 40 {
		t.Fatalf("worker was not updated: %+v", w)
	}
	runID, err := a.StartRun(ctx, id)
	if err != nil {
		t.Fatalf("start run: %v", err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		r, err := a.Run(ctx, runID)
		if err != nil {
			t.Fatal(err)
		}
		if r.Status.Terminal() {
			if r.Status != store.RunFailed || r.Error == "" {
				t.Fatalf("stub dispatch should record its failure: %+v", r)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("run did not settle: %+v", r)
		}
		time.Sleep(10 * time.Millisecond)
	}
	runs, err := a.Runs(ctx, id)
	if err != nil || len(runs) != 1 || runs[0].ID != runID {
		t.Fatalf("run history = %+v, %v", runs, err)
	}
	if err := a.DeleteWorker(ctx, id); err != nil {
		t.Fatalf("delete settled worker: %v", err)
	}
}

func TestWorkerValidationAndControls(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	a := app.New(testConfig(), st)
	invalid := app.WorkerParams{Name: "x", Repo: "acme", Repository: "acme/other", Environment: "go", Mission: "x"}
	if _, err := a.CreateWorker(ctx, invalid); err == nil {
		t.Fatal("expected repository lockdown rejection")
	}
	invalid.Repository = "acme/api"
	invalid.Schedule.Cron = "0 * * * *"
	if _, err := a.CreateWorker(ctx, invalid); err == nil {
		t.Fatal("expected schedule timezone rejection")
	}
	workerID, err := st.CreateWorker(ctx, store.Worker{Name: "w", Repo: "acme", Repository: "acme/api",
		Environment: "go", Model: "glm", Mission: "m", MaxIterations: 2})
	if err != nil {
		t.Fatal(err)
	}
	runID, err := st.CreateRun(ctx, store.Run{WorkerID: workerID, Status: store.RunScheduled, Mission: "m", Model: "glm", MaxIterations: 2})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.PauseRun(ctx, runID); err != nil {
		t.Fatal(err)
	}
	if r, _ := a.Run(ctx, runID); r.Status != store.RunPaused {
		t.Fatalf("pause status = %s", r.Status)
	}
	if err := a.StopRun(ctx, runID); err != nil {
		t.Fatal(err)
	}
	if err := a.DeleteWorker(ctx, workerID); err != nil {
		t.Fatalf("delete stopped worker: %v", err)
	}
}

func TestLocalRepoResolvesDockerCompute(t *testing.T) {
	cfg := &config.Config{
		Repos:   map[string]config.Repo{"checkout": {Type: "local", Path: "/workspaces/openzot", Repositories: []string{"openzot/openzot"}}},
		Compute: map[string]config.Compute{"development": {Type: "docker"}},
		Providers: map[string]config.Provider{"zai": {APIKey: "secret", Models: map[string]config.Model{
			"local": {Model: "glm"},
		}}},
		Environments: map[string]config.Environment{"dev": {Compute: "development", Provider: "zai", Model: "local", Image: "golang:1.26.5-bookworm"}},
	}
	a := app.New(cfg, openStore(t))
	provider, spec, err := a.Resolve(dispatch.Execution{Repo: "checkout", Repository: "openzot/openzot", Environment: "dev", Model: "local", MaxIterations: 27})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if provider.Type() != "docker" || spec.Image != "golang:1.26.5-bookworm" || spec.MaxIterations != 27 {
		t.Fatalf("resolved compute = %s, %+v", provider.Type(), spec)
	}
	if len(spec.Mounts) != 1 || spec.Mounts[0].Source != "/workspaces/openzot" || spec.Mounts[0].Target != "/workspace" {
		t.Fatalf("local checkout mount = %+v", spec.Mounts)
	}
	if spec.Model.Provider != "zai" || spec.Model.APIKey != "secret" {
		t.Fatalf("provider name was not used as the default driver: %+v", spec.Model)
	}
	if _, _, err := a.Resolve(dispatch.Execution{Environment: "dev", Provider: "missing", Model: "local"}); err == nil || !strings.Contains(err.Error(), `provider "missing"`) {
		t.Fatalf("missing model provider error = %v", err)
	}
	if _, err := a.CreateWorker(context.Background(), app.WorkerParams{Name: "tester", Repo: "checkout", Repository: "openzot/openzot", Environment: "dev", Mission: "run tests"}); err != nil {
		t.Fatalf("local worker: %v", err)
	}

	cfg.Repos["checkout"] = config.Repo{Type: "local", Repositories: []string{"openzot/openzot"}}
	if _, err := a.CreateWorker(context.Background(), app.WorkerParams{Name: "tester", Repo: "checkout", Repository: "openzot/openzot", Environment: "dev", Mission: "run tests"}); err == nil {
		t.Fatal("local worker accepted a missing checkout path")
	}
}

func TestGitHubRepoResolvesVercelComputeAndGitSource(t *testing.T) {
	cfg := &config.Config{
		Repos: map[string]config.Repo{"github": {Type: "github"}},
		Compute: map[string]config.Compute{"remote": {
			Type: "vercel", Token: "sandbox-token", TeamID: "team_123", ProjectID: "prj_123", Timeout: "2h",
		}},
		Providers: map[string]config.Provider{"corporate": {Driver: "zai", APIKey: "model-secret", BaseURL: "https://models.example.com", Models: map[string]config.Model{
			"glm": {Model: "glm-5.2"},
		}}},
		Environments: map[string]config.Environment{"remote": {
			Compute: "remote", Provider: "corporate", Model: "glm", Image: "go-environment:latest",
		}},
	}

	provider, spec, err := app.New(cfg, nil).Resolve(dispatch.Execution{
		Repo: "github", Repository: "openzot/openzot", Environment: "remote", Provider: "corporate", Model: "glm", MaxIterations: 17,
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if provider.Type() != "vercel" || spec.Source.URL != "https://github.com/openzot/openzot.git" || spec.Source.Username != "x-access-token" || spec.Source.Directory != "openzot" {
		t.Fatalf("resolved provider/source = %s, %+v", provider.Type(), spec.Source)
	}
	if spec.Model.Provider != "zai" || spec.Model.Model != "glm-5.2" || spec.Model.APIKey != "model-secret" || spec.Model.BaseURL != "https://models.example.com" {
		t.Fatalf("resolved model provider = %+v", spec.Model)
	}
}

func TestWorkerRejectsLocalRepoOnVercelCompute(t *testing.T) {
	cfg := &config.Config{
		Repos:     map[string]config.Repo{"checkout": {Type: "local", Path: "/workspace", Repositories: []string{"openzot/openzot"}}},
		Compute:   map[string]config.Compute{"remote": {Type: "vercel"}},
		Providers: map[string]config.Provider{"zai": {Models: map[string]config.Model{"glm": {Model: "glm-5.2"}}}},
		Environments: map[string]config.Environment{"remote": {
			Compute: "remote", Provider: "zai", Model: "glm", Image: "go-environment:latest",
		}},
	}
	_, err := app.New(cfg, openStore(t)).CreateWorker(context.Background(), app.WorkerParams{
		Name: "remote", Repo: "checkout", Repository: "openzot/openzot", Environment: "remote", Mission: "test",
	})
	if err == nil || !strings.Contains(err.Error(), "needs a remote repo connection") {
		t.Fatalf("CreateWorker error = %v", err)
	}
}
