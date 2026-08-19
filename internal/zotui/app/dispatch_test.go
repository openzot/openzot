package app

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openzot/openzot/internal/zotui/compute"
	"github.com/openzot/openzot/internal/zotui/config"
	"github.com/openzot/openzot/internal/zotui/store"
)

// sandboxAPI stands in for the Vercel Sandbox API so a run can be driven end to
// end - create, exec, destroy - and the teardown of each sandbox observed.
type sandboxAPI struct {
	server  *httptest.Server
	started chan struct{}

	mu       sync.Mutex
	creates  int
	stops    int
	exitCode int
	hold     bool // keep the command open until its request is cancelled
}

func newSandboxAPI(t *testing.T) *sandboxAPI {
	t.Helper()
	api := &sandboxAPI{started: make(chan struct{}, 8)}
	api.server = httptest.NewServer(http.HandlerFunc(api.handle))
	t.Cleanup(api.server.Close)
	return api
}

func (a *sandboxAPI) handle(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/v4/sandboxes":
		a.mu.Lock()
		a.creates++
		session := fmt.Sprintf("sess_%d", a.creates)
		a.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"sandbox":{"name":"zotui-test"},"session":{"id":%q,"cwd":"/workspace"}}`, session)
	case strings.HasSuffix(r.URL.Path, "/fs/write"):
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{}`)
	case strings.HasSuffix(r.URL.Path, "/cmd"):
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = io.WriteString(w, "{\"command\":{\"id\":\"cmd_1\",\"exitCode\":null}}\n")
		_, _ = io.WriteString(w, "{\"stream\":\"stdout\",\"data\":\"working\\n\"}\n")
		w.(http.Flusher).Flush()
		select {
		case a.started <- struct{}{}:
		default:
		}
		a.mu.Lock()
		hold, code := a.hold, a.exitCode
		a.mu.Unlock()
		if hold {
			<-r.Context().Done()
			return
		}
		fmt.Fprintf(w, "{\"command\":{\"id\":\"cmd_1\",\"exitCode\":%d}}\n", code)
	case strings.HasSuffix(r.URL.Path, "/stop"):
		a.mu.Lock()
		a.stops++
		a.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{}`)
	default:
		http.Error(w, "unexpected request", http.StatusNotFound)
	}
}

func (a *sandboxAPI) counts() (int, int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.creates, a.stops
}

func (a *sandboxAPI) set(hold bool, exitCode int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.hold, a.exitCode = hold, exitCode
}

func sandboxConfig(baseURL string) *config.Config {
	return &config.Config{
		Repos: map[string]config.Repo{"acme": {Type: "github", Repositories: []string{"acme/api"}}},
		Compute: map[string]config.Compute{"remote": {Type: "vercel", Token: "sandbox-token",
			TeamID: "team_1", ProjectID: "prj_1", Timeout: "1h", BaseURL: baseURL}},
		Providers:    map[string]config.Provider{"zai": {APIKey: "model-secret", Models: map[string]config.Model{"glm": {Model: "glm-5.2"}}}},
		Environments: map[string]config.Environment{"remote": {Compute: "remote", Provider: "zai", Model: "glm", Image: "img"}},
	}
}

// newApp hands sandboxes a stub executable. The real worker is a 23MB binary
// that every sandbox creation re-compresses, which turns these end-to-end tests
// into a gzip benchmark; nothing here depends on what the sandbox is given.
func newApp(cfg *config.Config, st store.Store) *App {
	a := New(cfg, st)
	a.worker = func(platform string) (compute.Worker, error) {
		return compute.Worker{Platform: platform, Data: []byte("zot")}, nil
	}
	return a
}

func newStore(t *testing.T) store.Store {
	t.Helper()
	st, err := store.Open(store.Config{Driver: "sqlite", DSN: filepath.Join(t.TempDir(), "dispatch.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func newWorker(t *testing.T, st store.Store) string {
	t.Helper()
	id, err := st.CreateWorker(context.Background(), store.Worker{Name: "builder", Repo: "acme",
		Repository: "acme/api", Environment: "remote", Provider: "zai", Model: "glm", Mission: "ship it",
		MaxIterations: 5})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func settled(t *testing.T, a *App, runID string) store.Run {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		r, err := a.Run(context.Background(), runID)
		if err != nil {
			t.Fatal(err)
		}
		if r.Status.Terminal() {
			return *r
		}
		if time.Now().After(deadline) {
			t.Fatalf("run did not settle: %+v", r)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func awaitStart(t *testing.T, api *sandboxAPI) {
	t.Helper()
	select {
	case <-api.started:
	case <-time.After(15 * time.Second):
		t.Fatal("the sandbox never started the worker command")
	}
}

// A worker that exits nonzero must keep its exit code. Settling the run twice -
// once carrying the code, then again carrying none - wrote NULL over it, so
// every failed run reported no exit code at all and the failure was unreadable.
func TestFailedRunKeepsItsExitCode(t *testing.T) {
	api := newSandboxAPI(t)
	api.set(false, 2)
	st := newStore(t)
	a := newApp(sandboxConfig(api.server.URL), st)
	runID, err := a.StartRun(context.Background(), newWorker(t, st))
	if err != nil {
		t.Fatal(err)
	}
	r := settled(t, a, runID)
	if r.Status != store.RunFailed {
		t.Fatalf("status = %s, want failed", r.Status)
	}
	if r.ExitCode == nil || *r.ExitCode != 2 {
		t.Fatalf("exit code = %v, want 2", r.ExitCode)
	}
	if !strings.Contains(r.Error, "code 2") {
		t.Fatalf("error = %q, want the exit code explained", r.Error)
	}
	output, err := a.RunOutput(context.Background(), runID, 0)
	if err != nil || string(output.Data) != "working\n" {
		t.Fatalf("run output = %+v, %v", output, err)
	}
	if creates, stops := api.counts(); creates != 1 || stops != 1 {
		t.Fatalf("sandbox lifecycle = %d created, %d destroyed", creates, stops)
	}
}

// A clean run records success and the zero exit code it actually returned.
func TestSucceededRunRecordsItsExitCode(t *testing.T) {
	api := newSandboxAPI(t)
	st := newStore(t)
	a := newApp(sandboxConfig(api.server.URL), st)
	runID, err := a.StartRun(context.Background(), newWorker(t, st))
	if err != nil {
		t.Fatal(err)
	}
	r := settled(t, a, runID)
	if r.Status != store.RunSucceeded || r.ExitCode == nil || *r.ExitCode != 0 || r.Error != "" {
		t.Fatalf("settled run = %+v (exit %v)", r, r.ExitCode)
	}
}

// Ctrl-C must not leave a sandbox running with the run's repository token still
// inside it. Shutdown cancels every in-flight run and waits for the teardown
// instead of letting the process exit out from under the dispatch goroutines.
func TestShutdownDestroysInFlightSandboxes(t *testing.T) {
	api := newSandboxAPI(t)
	api.set(true, 0)
	st := newStore(t)
	a := newApp(sandboxConfig(api.server.URL), st)
	runID, err := a.StartRun(context.Background(), newWorker(t, st))
	if err != nil {
		t.Fatal(err)
	}
	awaitStart(t, api)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := a.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if creates, stops := api.counts(); creates != 1 || stops != 1 {
		t.Fatalf("shutdown orphaned the sandbox: %d created, %d destroyed", creates, stops)
	}
	// A drained App refuses to boot anything else, so a late scheduler tick
	// cannot resurrect a sandbox the shutdown just tore down.
	if _, err := a.StartRun(context.Background(), newWorker(t, st)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if creates, _ := api.counts(); creates != 1 {
		t.Fatalf("a run launched after shutdown: %d sandboxes created", creates)
	}
	if r, _ := a.Run(context.Background(), runID); r.Status == store.RunRunning {
		t.Fatalf("the drained run is still marked running: %+v", r)
	}
}

// A run left "running" by a killed process has no sandbox to return to: nothing
// reaps it, so it shows phantom activity for ever and its worker can never start
// again. A paused run is the operator's decision and must survive the restart.
func TestReconcileFailsRunsLeftInFlightByAnEarlierProcess(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	a := newApp(sandboxConfig("http://127.0.0.1:1"), st)

	orphaned := newWorker(t, st)
	orphanedRun, err := st.CreateRun(ctx, store.Run{WorkerID: orphaned, Status: store.RunRunning, Mission: "m", Model: "glm"})
	if err != nil {
		t.Fatal(err)
	}
	queued, err := st.CreateWorker(ctx, store.Worker{Name: "queued", Repo: "acme", Repository: "acme/api",
		Environment: "remote", Provider: "zai", Model: "glm", Mission: "m", MaxIterations: 5})
	if err != nil {
		t.Fatal(err)
	}
	queuedRun, err := st.CreateRun(ctx, store.Run{WorkerID: queued, Status: store.RunScheduled, Mission: "m", Model: "glm"})
	if err != nil {
		t.Fatal(err)
	}
	held, err := st.CreateWorker(ctx, store.Worker{Name: "held", Repo: "acme", Repository: "acme/api",
		Environment: "remote", Provider: "zai", Model: "glm", Mission: "m", MaxIterations: 5})
	if err != nil {
		t.Fatal(err)
	}
	heldRun, err := st.CreateRun(ctx, store.Run{WorkerID: held, Status: store.RunPaused, Mission: "m", Model: "glm"})
	if err != nil {
		t.Fatal(err)
	}

	reconciled, err := a.Reconcile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if reconciled != 2 {
		t.Fatalf("reconciled %d runs, want the running and scheduled ones", reconciled)
	}
	for _, id := range []string{orphanedRun, queuedRun} {
		r, err := a.Run(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if r.Status != store.RunFailed || !strings.Contains(r.Error, "restart") || r.FinishedAt == nil {
			t.Fatalf("interrupted run = %+v", r)
		}
	}
	if r, _ := a.Run(ctx, heldRun); r.Status != store.RunPaused {
		t.Fatalf("a paused run did not survive the restart: %+v", r)
	}
	// A reconciled worker is startable again.
	if _, err := a.StartRun(ctx, orphaned); err != nil {
		t.Fatalf("worker stayed blocked by its phantom run: %v", err)
	}
}

// Pausing must settle the run's status before cancelling it. Cancelling first let
// the dispatch goroutine record a genuine failure that the pause then overwrote:
// the run read as paused and resumable while still carrying the finish time of
// an error nobody ever saw.
func TestPauseDoesNotMaskARunThatFailed(t *testing.T) {
	ctx := context.Background()
	api := newSandboxAPI(t)
	api.set(true, 0)
	st := &delayedStatus{Store: newStore(t), on: store.RunPaused, delay: 250 * time.Millisecond}
	a := newApp(sandboxConfig(api.server.URL), st)
	runID, err := a.StartRun(ctx, newWorker(t, st))
	if err != nil {
		t.Fatal(err)
	}
	awaitStart(t, api)

	if err := a.PauseRun(ctx, runID); err != nil {
		t.Fatalf("PauseRun: %v", err)
	}
	shutdown, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := a.Shutdown(shutdown); err != nil {
		t.Fatal(err)
	}
	r, err := a.Run(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != store.RunPaused {
		t.Fatalf("status = %s, want paused", r.Status)
	}
	if r.FinishedAt != nil {
		t.Fatalf("the pause hid a recorded completion: finished at %s, error %q", r.FinishedAt, r.Error)
	}
}

// Two resume clicks must not boot two sandboxes for one run. Both calls used to
// pass the paused check and launch, and the second cancel handle replaced the
// first - leaving a container nothing in the process could ever stop.
func TestConcurrentResumeLaunchesOneSandbox(t *testing.T) {
	ctx := context.Background()
	api := newSandboxAPI(t)
	api.set(true, 0)
	st := &delayedStatus{Store: newStore(t), on: store.RunScheduled, delay: 150 * time.Millisecond}
	a := newApp(sandboxConfig(api.server.URL), st)
	workerID := newWorker(t, st)
	runID, err := st.CreateRun(ctx, store.Run{WorkerID: workerID, Status: store.RunPaused, Mission: "ship it",
		Provider: "zai", Model: "glm", MaxIterations: 5})
	if err != nil {
		t.Fatal(err)
	}

	results := make(chan error, 2)
	for range 2 {
		go func() { results <- a.ResumeRun(ctx, runID) }()
	}
	accepted := 0
	for range 2 {
		if err := <-results; err == nil {
			accepted++
		}
	}
	if accepted != 1 {
		t.Fatalf("%d of 2 concurrent resumes were accepted, want 1", accepted)
	}
	awaitStart(t, api)
	shutdown, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := a.Shutdown(shutdown); err != nil {
		t.Fatal(err)
	}
	if creates, stops := api.counts(); creates != 1 || stops != 1 {
		t.Fatalf("resume booted %d sandboxes and destroyed %d, want 1 and 1", creates, stops)
	}
}

// delayedStatus widens the window between reading a run's status and writing the
// next one, which is exactly where a control action and a finishing dispatch used
// to interleave. Only the transition under test is slowed.
type delayedStatus struct {
	store.Store
	on    store.RunStatus
	delay time.Duration
}

func (s *delayedStatus) SetRunStatus(ctx context.Context, id string, status store.RunStatus, code *int, reason string) error {
	if status == s.on {
		time.Sleep(s.delay)
	}
	return s.Store.SetRunStatus(ctx, id, status, code, reason)
}
