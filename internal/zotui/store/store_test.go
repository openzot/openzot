package store_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/openzot/openzot/internal/zotui/store"
	_ "modernc.org/sqlite"
)

func TestWorkersRunsAndOutputRoundTrip(t *testing.T) {
	st, err := store.Open(store.Config{Driver: "sqlite", DSN: filepath.Join(t.TempDir(), "state.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	want := store.Worker{Name: "builder", Repo: "gh", Repository: "acme/api", Environment: "go", Model: "glm",
		Mission: "ship improvements", MaxIterations: 12, Schedule: store.Schedule{Cron: "0 * * * *", Timezone: "UTC", RuntimeMinutes: 30}}
	id, err := st.CreateWorker(ctx, want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := st.GetWorker(ctx, id)
	if err != nil || got.Name != want.Name || got.Schedule != want.Schedule || got.CreatedAt.IsZero() {
		t.Fatalf("worker round trip = %+v, %v", got, err)
	}
	got.Name = "renamed"
	if err := st.UpdateWorker(ctx, *got); err != nil {
		t.Fatal(err)
	}
	workers, _ := st.ListWorkers(ctx)
	if len(workers) != 1 || workers[0].Name != "renamed" {
		t.Fatalf("workers = %+v", workers)
	}
	runID, err := st.CreateRun(ctx, store.Run{WorkerID: id, Mission: want.Mission, Model: want.Model, MaxIterations: 12})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetRunStatus(ctx, runID, store.RunRunning, nil, ""); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateRunProgress(ctx, runID, 3, "test", "go test ./..."); err != nil {
		t.Fatal(err)
	}
	if err := st.AppendRunOutput(ctx, runID, []byte("first\n")); err != nil {
		t.Fatal(err)
	}
	if err := st.AppendRunOutput(ctx, runID, []byte("second\n")); err != nil {
		t.Fatal(err)
	}
	code := 0
	if err := st.SetRunStatus(ctx, runID, store.RunSucceeded, &code, ""); err != nil {
		t.Fatal(err)
	}
	r, err := st.GetRun(ctx, runID)
	if err != nil || r.Status != store.RunSucceeded || r.Iteration != 3 || r.Tool != "test" || r.StartedAt == nil || r.FinishedAt == nil {
		t.Fatalf("run round trip = %+v, %v", r, err)
	}
	output, _ := st.RunOutput(ctx, runID)
	if output != "first\nsecond\n" {
		t.Fatalf("output = %q", output)
	}
	if err := st.DeleteWorker(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetRun(ctx, runID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("run survived worker delete: %v", err)
	}
}

func TestMigrationReplacesTheExperimentalJobsSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE schema_migrations (version BIGINT PRIMARY KEY, name TEXT, applied_at BIGINT);
INSERT INTO schema_migrations VALUES (1, 'initial schema', 1);
CREATE TABLE jobs (id TEXT PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	st, err := store.Open(store.Config{Driver: "sqlite", DSN: path})
	if err != nil {
		t.Fatalf("migrate old database: %v", err)
	}
	defer st.Close()
	id, err := st.CreateWorker(context.Background(), store.Worker{Name: "new model", Repo: "r",
		Repository: "o/r", Environment: "e", Model: "m", Mission: "work"})
	if err != nil || id == "" {
		t.Fatalf("new worker schema unavailable after migration: %q, %v", id, err)
	}
}

func TestOnlyOneActiveRunPerWorker(t *testing.T) {
	st, err := store.Open(store.Config{Driver: "sqlite", DSN: filepath.Join(t.TempDir(), "state.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	wid, _ := st.CreateWorker(ctx, store.Worker{Name: "w", Repo: "r", Repository: "o/r", Environment: "e", Model: "m", Mission: "x"})
	if _, err := st.CreateRun(ctx, store.Run{WorkerID: wid, Mission: "x", Model: "m"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateRun(ctx, store.Run{WorkerID: wid, Mission: "x", Model: "m"}); err == nil {
		t.Fatal("expected active-run uniqueness violation")
	}
}
