package store_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

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
	want := store.Worker{Name: "builder", Repo: "gh", Repository: "acme/api", Environment: "go", Provider: "zai", Model: "glm",
		Mission: "ship improvements", MaxIterations: 12, Schedule: store.Schedule{Cron: "0 * * * *", Timezone: "UTC", RuntimeMinutes: 30}}
	id, err := st.CreateWorker(ctx, want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := st.GetWorker(ctx, id)
	if err != nil || got.Name != want.Name || got.Provider != want.Provider || got.Schedule != want.Schedule || got.CreatedAt.IsZero() {
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
	runID, err := st.CreateRun(ctx, store.Run{WorkerID: id, Mission: want.Mission, Provider: want.Provider, Model: want.Model, MaxIterations: 12})
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
	if err != nil || r.Status != store.RunSucceeded || r.Provider != want.Provider || r.Iteration != 3 || r.Tool != "test" || r.StartedAt == nil || r.FinishedAt == nil {
		t.Fatalf("run round trip = %+v, %v", r, err)
	}
	output, _ := st.RunOutput(ctx, runID, 0)
	if string(output.Data) != "first\nsecond\n" || output.Start != 0 || output.Next != 13 {
		t.Fatalf("output = %+v", output)
	}
	if err := st.DeleteWorker(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetRun(ctx, runID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("run survived worker delete: %v", err)
	}
}

// A reader that already holds the head of a run's output is handed only the
// bytes appended since. Re-shipping the whole accumulated buffer on every 1.5s
// poll is what made a verbose run flood the browser and the single store
// connection.
func TestRunOutputReadsForwardFromAnOffset(t *testing.T) {
	ctx := context.Background()
	st, runID := runFixture(t)
	for _, chunk := range []string{"boot\n", "compiling\n", "done\n"} {
		if err := st.AppendRunOutput(ctx, runID, []byte(chunk)); err != nil {
			t.Fatal(err)
		}
	}
	head, err := st.RunOutput(ctx, runID, 0)
	if err != nil || string(head.Data) != "boot\ncompiling\ndone\n" || head.Start != 0 {
		t.Fatalf("first read = %+v, %v", head, err)
	}
	idle, err := st.RunOutput(ctx, runID, head.Next)
	if err != nil || len(idle.Data) != 0 || idle.Next != head.Next {
		t.Fatalf("poll with nothing new = %+v, %v", idle, err)
	}
	if err := st.AppendRunOutput(ctx, runID, []byte("tests pass\n")); err != nil {
		t.Fatal(err)
	}
	tail, err := st.RunOutput(ctx, runID, head.Next)
	if err != nil || string(tail.Data) != "tests pass\n" || tail.Start != head.Next {
		t.Fatalf("incremental read = %+v, %v", tail, err)
	}
	if tail.Next != head.Next+int64(len("tests pass\n")) {
		t.Fatalf("cursor did not advance by the appended bytes: %+v", tail)
	}
	// An offset inside a chunk still resolves to exactly the bytes after it.
	mid, err := st.RunOutput(ctx, runID, 2)
	if err != nil || string(mid.Data) != "ot\ncompiling\ndone\ntests pass\n" || mid.Start != 2 {
		t.Fatalf("mid-chunk read = %+v, %v", mid, err)
	}
	if _, err := st.RunOutput(ctx, "run_missing", 0); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("output of an unknown run = %v", err)
	}
}

// A run that never stops talking must not grow the database without bound: the
// head is discarded and the reader is told its tail is no longer continuous
// instead of being silently handed a buffer with a hole in it.
func TestRunOutputDiscardsTheHeadPastTheCap(t *testing.T) {
	ctx := context.Background()
	st, runID := runFixture(t)
	chunk := bytes.Repeat([]byte("x"), 1<<20)
	const chunks = 9
	for range chunks {
		if err := st.AppendRunOutput(ctx, runID, chunk); err != nil {
			t.Fatal(err)
		}
	}
	written := int64(chunks) * int64(len(chunk))
	output, err := st.RunOutput(ctx, runID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if output.Next != written {
		t.Fatalf("cursor = %d, want every appended byte accounted for (%d)", output.Next, written)
	}
	if int64(len(output.Data)) >= written {
		t.Fatalf("nothing was discarded: kept %d of %d bytes", len(output.Data), written)
	}
	if output.Start == 0 || output.Start+int64(len(output.Data)) != written {
		t.Fatalf("retained window = [%d,%d) of %d bytes", output.Start, output.Start+int64(len(output.Data)), written)
	}
}

// Appending costs the same whether a run has emitted a kilobyte or megabytes.
// Concatenating into one growing column rewrote every byte already stored, so a
// long run turned quadratic and blocked every other writer on the single
// connection.
func TestRunOutputAppendCostDoesNotGrowWithTheRun(t *testing.T) {
	ctx := context.Background()
	st, runID := runFixture(t)
	chunk := bytes.Repeat([]byte("x"), 8<<10)
	appends := func(n int) time.Duration {
		start := time.Now()
		for range n {
			if err := st.AppendRunOutput(ctx, runID, chunk); err != nil {
				t.Fatal(err)
			}
		}
		return time.Since(start)
	}
	early := appends(100)
	appends(300)
	late := appends(100)
	if late > 4*early {
		t.Fatalf("appending got %.1fx more expensive as output grew (%s then %s)",
			float64(late)/float64(early), early, late)
	}
}

// A process that starts up has to find the runs an earlier one abandoned. Only
// unsettled runs qualify: a finished run is history, and returning it would have
// the new process re-settle work that already completed.
func TestActiveRunsReportsOnlyUnsettledRuns(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(store.Config{Driver: "sqlite", DSN: filepath.Join(t.TempDir(), "active.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	want := map[string]bool{}
	for _, status := range []store.RunStatus{store.RunScheduled, store.RunRunning, store.RunPaused,
		store.RunStopped, store.RunSucceeded, store.RunFailed} {
		workerID, err := st.CreateWorker(ctx, store.Worker{Name: string(status), Repo: "r", Repository: "o/r",
			Environment: "e", Model: "m", Mission: "x"})
		if err != nil {
			t.Fatal(err)
		}
		runID, err := st.CreateRun(ctx, store.Run{WorkerID: workerID, Status: status, Mission: "x", Model: "m"})
		if err != nil {
			t.Fatal(err)
		}
		if !status.Terminal() {
			want[runID] = true
		}
	}
	active, err := st.ActiveRuns(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != len(want) {
		t.Fatalf("active runs = %d, want %d", len(active), len(want))
	}
	for _, r := range active {
		if !want[r.ID] {
			t.Fatalf("settled run %s (%s) reported as active", r.ID, r.Status)
		}
	}
}

// Output for a run that no longer exists is an error, not silent success: the
// writer would otherwise stream a whole run into nothing.
func TestAppendRunOutputRejectsAnUnknownRun(t *testing.T) {
	ctx := context.Background()
	st, runID := runFixture(t)
	if err := st.AppendRunOutput(ctx, "run_missing", []byte("x")); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("append to an unknown run = %v", err)
	}
	if err := st.AppendRunOutput(ctx, runID, nil); err != nil {
		t.Fatalf("empty append = %v", err)
	}
	output, err := st.RunOutput(ctx, runID, 0)
	if err != nil || len(output.Data) != 0 || output.Next != 0 {
		t.Fatalf("output after an empty append = %+v, %v", output, err)
	}
}

func runFixture(t *testing.T) (store.Store, string) {
	t.Helper()
	st, err := store.Open(store.Config{Driver: "sqlite", DSN: filepath.Join(t.TempDir(), "output.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	workerID, err := st.CreateWorker(ctx, store.Worker{Name: "w", Repo: "r", Repository: "o/r",
		Environment: "e", Model: "m", Mission: "x"})
	if err != nil {
		t.Fatal(err)
	}
	runID, err := st.CreateRun(ctx, store.Run{WorkerID: workerID, Mission: "x", Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	return st, runID
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
