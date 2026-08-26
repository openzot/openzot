package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/openzot/openzot"
	"github.com/openzot/openzot/internal/config"
	"github.com/openzot/openzot/internal/order"
	"github.com/openzot/openzot/internal/session"
	"github.com/openzot/openzot/internal/watch"
	"github.com/openzot/openzot/tui"
)

// fakeEngine is what a watch dispatches through in these tests: it records the
// runs it was asked for instead of reaching a provider, and answers with
// scripted errors so failure paths can be driven deterministically.
type fakeEngine struct {
	mu    sync.Mutex
	calls []struct {
		task    string
		options zot.RunOptions
	}

	errs []error // one per call, the last repeated once exhausted
}

func (f *fakeEngine) run(_ context.Context, _ zot.Config, task string, options zot.RunOptions) error {
	f.mu.Lock()

	f.calls = append(f.calls, struct {
		task    string
		options zot.RunOptions
	}{task, options})

	i := len(f.calls) - 1

	f.mu.Unlock()

	if i < len(f.errs) {
		return f.errs[i]
	}

	if len(f.errs) == 0 {
		return nil
	}

	return f.errs[len(f.errs)-1]
}

func (f *fakeEngine) tasks() []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	var out []string

	for _, c := range f.calls {
		out = append(out, c.task)
	}

	return out
}

// settleServer answers every model turn by recording a successful outcome - the
// local stand-in for a provider, used where the real engine has to run.
func settleServer() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")

		fmt.Fprintf(w, "data: %s\n\n",
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"d","type":"function","function":{"name":"success","arguments":"{\"summary\":\"done\"}"}}]},"finish_reason":"tool_calls"}]}`)

		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
}

// The wiring is the contract: `zot --watch <target>` must reach the watch loop
// with the target resolved against the invoking directory - like an order path,
// before any chdir into --dir - and with a dispatcher ready to run orders.
func TestWatchWiring(t *testing.T) {
	t.Setenv("ZOT_SESSION_DIR", t.TempDir())

	t.Chdir(t.TempDir())

	workdir := t.TempDir()

	configPath := filepath.Join(t.TempDir(), "config.yaml")

	cfgYAML := `
agent:
  model: test-model
ui:
  plain: true
default_provider: local
providers:
  local:
    driver: custom
    base_url: http://127.0.0.1:1
    api_key: test-key
`
	if err := os.WriteFile(configPath, []byte(cfgYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	var gotTarget string

	var gotDispatcher watch.Dispatcher

	release := make(chan struct{})

	original := startWatch

	startWatch = func(ctx context.Context, target string, d watch.Dispatcher) error {
		gotTarget = target
		gotDispatcher = d

		<-release // hold the watch open, the way the real loop blocks

		return nil
	}

	t.Cleanup(func() { startWatch = original })

	withArgs(t, "--config", configPath, "--dir", workdir, "--watch", "orders")

	// captured before run() chdirs into --dir: the target means the invoking
	// directory's orders/, not one inside --dir
	invocation, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan error)

	go func() { done <- run() }()

	time.Sleep(50 * time.Millisecond)

	close(release)

	if err := <-done; err != nil {
		t.Fatalf("run: %v", err)
	}

	if want := filepath.Join(invocation, "orders"); gotTarget != want {
		t.Errorf("watch target = %q, want %q", gotTarget, want)
	}

	if gotDispatcher == nil {
		t.Error("run() must hand the watch loop a dispatcher")
	}
}

// Bare `zot --watch` watches this project's own orders - the drop box you
// almost always mean - while a named target still watches that instead.
func TestBareWatchWatchesTheProjectsOrders(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want func(invocation, project string) string
	}{
		{
			name: "bare --watch watches the project's book",
			args: []string{"--watch"},
			want: func(_, project string) string { return order.OrdersDir(project) },
		},
		{
			name: "a named target wins, resolved where it was typed",
			args: []string{"--watch", "inbox"},
			want: func(invocation, _ string) string { return filepath.Join(invocation, "inbox") },
		},
		{
			name: "--orders-dir moves what bare --watch watches",
			args: []string{"--watch", "--orders-dir", "briefs"},
			want: func(invocation, _ string) string { return filepath.Join(invocation, "briefs") },
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("ZOT_SESSION_DIR", t.TempDir())

			invocation := t.TempDir()

			t.Chdir(invocation)

			project := t.TempDir()

			configPath := filepath.Join(t.TempDir(), "config.yaml")

			if err := os.WriteFile(configPath, []byte(`
agent:
  model: test-model
ui:
  plain: true
default_provider: local
providers:
  local:
    driver: custom
    base_url: http://127.0.0.1:1
    api_key: test-key
`), 0o644); err != nil {
				t.Fatal(err)
			}

			var gotTarget string

			release := make(chan struct{})

			original := startWatch

			startWatch = func(_ context.Context, target string, _ watch.Dispatcher) error {
				gotTarget = target

				<-release

				return nil
			}

			t.Cleanup(func() { startWatch = original })

			withArgs(t, append([]string{"--config", configPath, "--dir", project}, test.args...)...)

			done := make(chan error)

			go func() { done <- run() }()

			time.Sleep(50 * time.Millisecond)

			close(release)

			if err := <-done; err != nil {
				t.Fatalf("run: %v", err)
			}

			if want := test.want(invocation, project); gotTarget != want {
				t.Errorf("watch target = %q, want %q", gotTarget, want)
			}
		})
	}
}

// A watch has one target: it is a place work arrives at, and a resume continues
// one specific session rather than an open-ended stream of new ones.
func TestWatchRejectsSeveralTargetsAndResume(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "several targets",
			args: []string{"--watch", "orders", "extra.yaml"},
			want: "--watch takes one folder or glob",
		},
		{
			name: "--resume alongside --watch",
			args: []string{"--watch", "orders", "--resume", "last"},
			want: "use one, not both",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			quietStderr(t)

			t.Setenv("ZOT_SESSION_DIR", t.TempDir())
			t.Chdir(t.TempDir())

			withArgs(t, test.args...)

			err := run()
			if err == nil {
				t.Fatal("this invocation must be refused")
			}

			if !strings.Contains(err.Error(), test.want) {
				t.Errorf("error = %v, want it to say %q", err, test.want)
			}
		})
	}
}

// An order picked up by the watcher goes through the same gate a batch position
// does: one already satisfied by the ledger is skipped without touching the
// engine, however many times the sweep sees it.
func TestAWatchedOrderAlreadySatisfiedIsSkipped(t *testing.T) {
	book := t.TempDir()

	orderPath := filepath.Join(order.OrdersDir(book), "the-work.yaml")

	if err := os.MkdirAll(filepath.Dir(orderPath), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(orderPath, []byte("objective: finished work\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	loaded, err := order.Load(orderPath)
	if err != nil {
		t.Fatal(err)
	}

	ledger := order.Ledger{Root: order.RecordsDir(book)}

	if err := ledger.Record(loaded, "20260822-010101", "settled", time.Now(), order.Evidence{}); err != nil {
		t.Fatal(err)
	}

	engine := &fakeEngine{}

	runner := watchRunner{runs: oneRun{
		ctx:      context.Background(),
		sessions: t.TempDir(),
		ledger:   ledger,
		run:      engine.run,
	}}

	runner.Dispatch(loaded)

	// the sweep will hand the same file over again; it stays skipped
	runner.Dispatch(loaded)

	if got := engine.tasks(); len(got) != 0 {
		t.Errorf("a satisfied order reached the engine %d times (%v), want never", len(got), got)
	}
}

// One order failing must not end the watch: the first dispatch is scripted to
// die mid-run, the next to succeed, and both must reach the engine - a factory
// pointed at a folder cannot let 3am's failed order end 4am's good one.
func TestAFailedOrderDoesNotKillTheWatch(t *testing.T) {
	engine := &fakeEngine{errs: []error{errors.New("the provider fell over")}}

	first := order.Order{Objective: "doomed", Path: "/book/orders/doomed.yaml"}
	second := order.Order{Objective: "survivor", Path: "/book/orders/survivor.yaml"}

	sessions := t.TempDir()

	runner := watchRunner{runs: oneRun{
		ctx:      context.Background(),
		cfg:      config.Defaults(),
		sessions: sessions,
		run:      engine.run,
	}}

	stderr, err := captureStderr(t, func() error {
		runner.Dispatch(first)
		runner.Dispatch(second)

		return nil
	})
	if err != nil {
		t.Fatalf("captureStderr: %v", err)
	}

	got := engine.tasks()

	if len(got) != 2 || got[0] != first.Objective || got[1] != second.Objective {
		t.Fatalf("engine ran %v, want both orders in arrival order", got)
	}

	// the failure names the order and says the watch went on
	if !strings.Contains(stderr, first.Path) || !strings.Contains(stderr, "staying on watch") {
		t.Errorf("a failed order should be reported as survived, not fatal:\n%s", stderr)
	}

	// each order ran as its own independent run: its own session log directory,
	// and no held final screen to stall the orders behind it
	for _, call := range engine.calls {
		if call.options.SessionDir != sessions {
			t.Errorf("SessionDir = %q, want %q for task %q",
				call.options.SessionDir, sessions, call.task)
		}

		if !call.options.QuitOnDone {
			t.Errorf("a watched order must not hold its final screen (%q)", call.task)
		}
	}
}

// A deliberate stop mid-run (q in the viewer) ends that run, not the watch:
// the message reads as what happened rather than as an order failing.
func TestAnOperatorStopMidRunDoesNotEndTheWatch(t *testing.T) {
	engine := &fakeEngine{errs: []error{fmt.Errorf("wrapped: %w", tui.ErrCancelled)}}

	runner := watchRunner{runs: oneRun{
		ctx:      context.Background(),
		sessions: "",
		run:      engine.run,
	}}

	stderr, err := captureStderr(t, func() error {
		runner.Dispatch(order.Order{Objective: "interrupted", Path: "/b/o/i.yaml"})
		runner.Dispatch(order.Order{Objective: "next", Path: "/b/o/n.yaml"})

		return nil
	})
	if err != nil {
		t.Fatalf("captureStderr: %v", err)
	}

	if !strings.Contains(stderr, "stopped while") {
		t.Errorf("an operator stop should be reported as one:\n%s", stderr)
	}

	if got := engine.tasks(); len(got) != 2 || got[1] != "next" {
		t.Errorf("engine ran %v, want the watch to have carried on to the next order", got)
	}
}

// The whole path, uncut: zot starts watching, an order written into the folder
// afterwards is dispatched without a restart, runs through the real engine
// against a local stub of the provider (never a real one), leaves exactly its
// own session log behind, and a second order still runs after the first. The
// watch is stopped the way an operator stops it - SIGTERM.
func TestWatchRunsOrdersAsTheyArriveEndToEnd(t *testing.T) {
	invocation := t.TempDir()

	t.Chdir(invocation)

	workdir := t.TempDir()

	sessions := filepath.Join(invocation, "sessions")
	watched := filepath.Join(invocation, "dropbox")

	server := settleServer()
	defer server.Close()

	configPath := filepath.Join(invocation, "config.yaml")

	cfgYAML := fmt.Sprintf(`
agent:
  model: test-model
ui:
  plain: true
default_provider: local
providers:
  local:
    driver: custom
    base_url: %s
    api_key: test-key
`, server.URL)

	if err := os.WriteFile(configPath, []byte(cfgYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	// --dir points elsewhere: the watch target must still mean the invoking
	// directory's dropbox/, because that is what was typed
	withArgs(t, "--config", configPath, "--session-dir", sessions,
		"--dir", workdir, "--plain", "--watch", "dropbox")

	done := make(chan error)

	go func() { done <- run() }()

	writeOrderFile(filepath.Join(watched, "first.yaml"), "the watched objective")

	waitForSessions(t, sessions, 1, "the watched objective")

	// the watcher survived its first run and takes the next order too
	writeOrderFile(filepath.Join(watched, "second.yaml"), "the follow-up objective")

	waitForSessions(t, sessions, 2, "the follow-up objective")

	entries, err := session.List(sessions)
	if err != nil {
		t.Fatal(err)
	}

	// newest first, and each order carries only its own brief
	if entries[0].Task != "the follow-up objective" || entries[1].Task != "the watched objective" {
		t.Errorf("session tasks = %q, %q", entries[0].Task, entries[1].Task)
	}

	if err := syscall.Kill(syscall.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("a stopped watch should exit cleanly: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("SIGTERM did not stop the watch")
	}
}

func writeOrderFile(path, objective string) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		panic(err)
	}

	if err := os.WriteFile(path, []byte("objective: "+objective+"\n"), 0o644); err != nil {
		panic(err)
	}
}

// waitForSessions blocks until n session logs exist and the newest names want -
// the observable sign that the watcher dispatched the order and the run wrote
// its own log.
func waitForSessions(t *testing.T, dir string, n int, want string) {
	t.Helper()

	deadline := time.Now().Add(20 * time.Second)

	for time.Now().Before(deadline) {
		entries, err := session.List(dir)
		if err == nil && len(entries) >= n {
			if strings.Contains(entries[0].Task, want) {
				return
			}
		}

		time.Sleep(10 * time.Millisecond)
	}

	entries, _ := session.List(dir)

	t.Fatalf("%d of %d sessions appeared; newest tasks: %+v", len(entries), n, entries)
}

// The help text is how watch mode is discoverable: it names the flag, shows
// both forms of target, and says what stopping looks like.
func TestUsageDocumentsWatchMode(t *testing.T) {
	text, err := captureStderr(t, func() error {
		usage(os.Stderr)

		return nil
	})
	if err != nil {
		t.Fatalf("captureStderr: %v", err)
	}

	for _, want := range []string{"--watch", "orders/*.yaml", "Ctrl-C"} {
		if !strings.Contains(text, want) {
			t.Errorf("usage does not mention %q:\n%s", want, text)
		}
	}
}
