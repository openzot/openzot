package app_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/openzot/openzot/internal/zotui/app"
	"github.com/openzot/openzot/internal/zotui/config"
	"github.com/openzot/openzot/internal/zotui/dispatch"
	"github.com/openzot/openzot/internal/zotui/store"
)

func testConfig() *config.Config {
	return &config.Config{
		Repos:        map[string]config.Repo{"acme": {Type: "github", Repositories: []string{"acme/api"}}},
		Compute:      map[string]config.Compute{"cf": {Type: "cloudflare"}},
		Models:       map[string]config.Model{"glm": {Provider: "zai", Model: "glm-5.2"}},
		Environments: map[string]config.Environment{"go": {Compute: "cf", Model: "glm", Image: "img"}},
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
	if w.Model != "glm" || w.MaxIterations != 20 || w.Schedule.Cron != "0 */4 * * *" {
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
		Repos:        map[string]config.Repo{"checkout": {Type: "local", Path: "/workspaces/openzot", Repositories: []string{"openzot/openzot"}}},
		Compute:      map[string]config.Compute{"development": {Type: "docker"}},
		Models:       map[string]config.Model{"local": {Provider: "zai", Model: "glm", APIKey: "secret"}},
		Environments: map[string]config.Environment{"dev": {Compute: "development", Model: "local", Image: "openzot/zot:dev"}},
	}
	a := app.New(cfg, openStore(t))
	provider, spec, err := a.Resolve(dispatch.Execution{Repo: "checkout", Repository: "openzot/openzot", Environment: "dev", Model: "local", MaxIterations: 27})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if provider.Type() != "docker" || spec.Image != "openzot/zot:dev" || spec.MaxIterations != 27 {
		t.Fatalf("resolved compute = %s, %+v", provider.Type(), spec)
	}
	if len(spec.Mounts) != 1 || spec.Mounts[0].Source != "/workspaces/openzot" || spec.Mounts[0].Target != "/workspace" {
		t.Fatalf("local checkout mount = %+v", spec.Mounts)
	}
	if _, err := a.CreateWorker(context.Background(), app.WorkerParams{Name: "tester", Repo: "checkout", Repository: "openzot/openzot", Environment: "dev", Mission: "run tests"}); err != nil {
		t.Fatalf("local worker: %v", err)
	}

	cfg.Repos["checkout"] = config.Repo{Type: "local", Repositories: []string{"openzot/openzot"}}
	if _, err := a.CreateWorker(context.Background(), app.WorkerParams{Name: "tester", Repo: "checkout", Repository: "openzot/openzot", Environment: "dev", Mission: "run tests"}); err == nil {
		t.Fatal("local worker accepted a missing checkout path")
	}
}
