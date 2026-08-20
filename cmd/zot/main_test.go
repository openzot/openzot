package main

import (
	"fmt"
	"github.com/openzot/openzot"
	"github.com/openzot/openzot/internal/buildinfo"
	"github.com/openzot/openzot/internal/config"
	"github.com/openzot/openzot/internal/session"
	"github.com/openzot/openzot/internal/version"
	"github.com/spf13/pflag"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// Reading a `.env` out of whatever directory zot was pointed at is a developer
// convenience and a released binary's liability: running zot against a
// repository you cloned to review would otherwise be enough to load a stray
// committed `.env` into the process that is about to run shell commands.
//
// So the behaviour is conditional on the build, and this test asserts whichever
// half applies to the binary it is compiled into - which means the release
// behaviour is verified by the ordinary `go test ./...` that CI runs, and the
// developer behaviour by `go test -tags dev ./...`.
func TestDotEnvIsOnlyReadOnADeveloperBuild(t *testing.T) {
	const key = "ZOT_TEST_TARGET_DOTENV"

	original, hadOriginal := os.LookupEnv(key)
	if err := os.Unsetenv(key); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if hadOriginal {
			_ = os.Setenv(key, original)
		} else {
			_ = os.Unsetenv(key)
		}
	})

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(key+"=loaded\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	loadEnv(dir)

	got := os.Getenv(key)

	if buildinfo.Dev {
		if got != "loaded" {
			t.Errorf("%s = %q, want a developer build to read the .env", key, got)
		}

		return
	}

	if got != "" {
		t.Errorf("%s = %q, want a release build to ignore the .env entirely", key, got)
	}
}

// A run must not be able to turn the switch back on. It is a build-time
// constant precisely so nothing at runtime - an env var, a config key, a flag -
// can reach it.
func TestNothingAtRuntimeCanEnableDotEnv(t *testing.T) {
	if buildinfo.Dev {
		t.Skip("this is a developer build")
	}

	const key = "ZOT_TEST_RUNTIME_DOTENV"

	t.Setenv("ZOT_DEV", "1")
	t.Setenv("ZOT_DEV_MODE", "true")
	t.Setenv("NODE_ENV", "development")

	dir := t.TempDir()

	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(key+"=loaded\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	loadEnv(dir)

	if got := os.Getenv(key); got != "" {
		t.Errorf("%s = %q; an environment variable turned .env loading back on", key, got)
	}
}

func TestResolveOrdersLoadsEveryFile(t *testing.T) {
	first := orderFile(t, "build the parser")
	second := orderFile(t, "then the lexer")

	orders, err := resolveOrders([]string{first, second}, false)
	if err != nil {
		t.Fatalf("resolveOrders: %v", err)
	}

	if len(orders) != 2 || orders[0].Objective != "build the parser" || orders[1].Objective != "then the lexer" {
		t.Errorf("orders = %+v", orders)
	}
}

// A bad batch must fail before any run starts: discovering order three is
// broken after orders one and two have spent an hour is the expensive way.
func TestResolveOrdersFailsTheWholeBatchUpFront(t *testing.T) {
	good := orderFile(t, "fine")

	if _, err := resolveOrders([]string{good, filepath.Join(t.TempDir(), "nope.yaml")}, false); err == nil {
		t.Error("a batch with a broken order must not resolve")
	}
}

// Someone typing prose where an order file goes is the retraining moment: the
// error has to teach the new shape, not just report a missing file.
func TestResolveOrdersTeachesProseTypers(t *testing.T) {
	_, err := resolveOrders([]string{"add a health endpoint"}, false)
	if err == nil {
		t.Fatal("prose must not resolve")
	}

	if !strings.Contains(err.Error(), "zot new") {
		t.Errorf("the error should point at `zot new`: %v", err)
	}
}

func TestResolveOrdersRequiresAnOrder(t *testing.T) {
	quietStderr(t)

	if _, err := resolveOrders(nil, false); err == nil {
		t.Error("no order must be an error")
	}
}

// A resume continues the order its session was started with; mixing new orders
// into it would blur which outcome belongs to which order.
func TestResolveOrdersOnAResume(t *testing.T) {
	orders, err := resolveOrders(nil, true)
	if err != nil || orders != nil {
		t.Errorf("a bare resume must resolve to no orders: %v, %v", orders, err)
	}

	if _, err := resolveOrders([]string{orderFile(t, "new work")}, true); err == nil {
		t.Error("orders alongside --resume must be an error")
	}
}

// `zot new` writes an order zot itself will run.
func TestNewOrderScaffoldsARunnableOrder(t *testing.T) {
	t.Chdir(t.TempDir())

	var out strings.Builder

	if err := newOrder([]string{"fix", "the", "typo"}, &out); err != nil {
		t.Fatalf("newOrder: %v", err)
	}

	path := filepath.Join("orders", "fix-the-typo.yaml")

	if !strings.Contains(out.String(), path) {
		t.Errorf("the output should say where the order went and how to run it:\n%s", out.String())
	}

	orders, err := resolveOrders([]string{path}, false)
	if err != nil {
		t.Fatalf("the scaffolded order does not resolve: %v", err)
	}

	if orders[0].Objective != "fix the typo" {
		t.Errorf("objective = %q", orders[0].Objective)
	}
}

// Bare `zot new` scaffolds the blank form: a file to fill in, which refuses to
// run until it is.
func TestNewOrderScaffoldsTheBlankForm(t *testing.T) {
	t.Chdir(t.TempDir())

	var out strings.Builder

	if err := newOrder(nil, &out); err != nil {
		t.Fatalf("newOrder: %v", err)
	}

	path := filepath.Join("orders", "order.yaml")

	if !strings.Contains(out.String(), "edit its objective") {
		t.Errorf("the output should say the objective still needs writing:\n%s", out.String())
	}

	if _, err := resolveOrders([]string{path}, false); err == nil {
		t.Error("the unedited blank form must not run")
	}
}

// --draft without an objective has nothing to draft from.
func TestNewOrderDraftRequiresAnObjective(t *testing.T) {
	t.Chdir(t.TempDir())

	if err := newOrder([]string{"--draft"}, io.Discard); err == nil {
		t.Error("`zot new --draft` with no objective must be an error")
	}
}

// A draft is a small read-only run: the model surveys the tree with the survey
// tools, then delivers the draft as its recorded outcome - and the result
// lands in the scaffold as real, editable YAML.
func TestNewOrderDraftsWithTheConfiguredModel(t *testing.T) {
	t.Chdir(t.TempDir())

	var turn atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")

		// first the survey - a list call - then the draft, delivered the way
		// every run ends: through the success tool
		if turn.Add(1) == 1 {
			fmt.Fprintf(w, "data: %s\n\n",
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"a","type":"function","function":{"name":"list","arguments":"{\"path\":\".\"}"}}]},"finish_reason":"tool_calls"}]}`)
		} else {
			fmt.Fprintf(w, "data: %s\n\n",
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"b","type":"function","function":{"name":"success","arguments":"{\"summary\":\"acceptance:\\n  - the suite passes\\nconstraints:\\n  - no new dependencies\\n\"}"}}]},"finish_reason":"tool_calls"}]}`)
		}

		fmt.Fprint(w, "data: [DONE]\n\n")
	}))

	defer server.Close()

	configPath := filepath.Join(t.TempDir(), "config.yaml")

	if err := os.WriteFile(configPath, []byte(fmt.Sprintf(`
agent:
  model: test-model
default_provider: local
providers:
  local:
    driver: custom
    base_url: %s
    api_key: test-key
`, server.URL)), 0o644); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder

	if err := newOrder([]string{"--draft", "--config", configPath, "add", "rate", "limiting"}, &out); err != nil {
		t.Fatalf("newOrder: %v", err)
	}

	orders, err := resolveOrders([]string{filepath.Join("orders", "add-rate-limiting.yaml")}, false)
	if err != nil {
		t.Fatalf("the drafted order does not resolve: %v", err)
	}

	if len(orders[0].Acceptance) != 1 || orders[0].Acceptance[0] != "the suite passes" {
		t.Errorf("Acceptance = %q, want the drafted criteria in the file", orders[0].Acceptance)
	}

	if !strings.Contains(out.String(), "review its drafted acceptance criteria") {
		t.Errorf("the output should ask for a review of the draft:\n%s", out.String())
	}
}

// A failed draft writes nothing: the operator asked for a drafted order, and
// silently handing back the plain scaffold would hide that they did not get one.
func TestNewOrderDraftFailureWritesNoFile(t *testing.T) {
	t.Chdir(t.TempDir())

	// a non-retriable failure, so the draft dies immediately rather than
	// pacing out the engine's (deliberately short) recovery budget
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "bad key", http.StatusUnauthorized)
	}))

	defer server.Close()

	configPath := filepath.Join(t.TempDir(), "config.yaml")

	if err := os.WriteFile(configPath, []byte(fmt.Sprintf(`
agent:
  model: test-model
default_provider: local
providers:
  local:
    driver: custom
    base_url: %s
    api_key: test-key
`, server.URL)), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := newOrder([]string{"--draft", "--config", configPath, "add", "rate", "limiting"}, io.Discard); err == nil {
		t.Fatal("a failed draft must be an error")
	}

	if _, statErr := os.Stat("orders"); statErr == nil {
		t.Error("a failed draft must not leave a scaffold behind")
	}
}

// A draft surveys the tree; a survey that can edit files or run commands is
// not a survey. This locks the toolbox read-only against anyone extending it.
func TestDraftToolsAreReadOnly(t *testing.T) {
	tools := draftTools()

	for _, name := range []string{"read", "list"} {
		if _, ok := tools[name]; !ok {
			t.Errorf("the draft toolbox is missing %q", name)
		}
	}

	for name := range tools {
		if name == "write" || name == "shell" {
			t.Errorf("the draft toolbox must never carry %q", name)
		}
	}

	if len(tools) != 2 {
		t.Errorf("draft toolbox = %d tools, want exactly read and list", len(tools))
	}
}

// orderFile writes a minimal order and returns its path.
func orderFile(t *testing.T, objective string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "order.yaml")

	if err := os.WriteFile(path, []byte("objective: "+fmt.Sprintf("%q", objective)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	return path
}

// quietStderr silences stderr for a test that deliberately triggers the usage
// block.
func quietStderr(t *testing.T) {
	t.Helper()

	original := os.Stderr

	devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}

	os.Stderr = devNull

	t.Cleanup(func() {
		os.Stderr = original

		devNull.Close()
	})
}

func TestFirstNonEmpty(t *testing.T) {
	tests := []struct {
		values []string
		want   string
	}{
		{[]string{"", "second"}, "second"},
		{[]string{"first", "second"}, "first"},
		// whitespace is not a value - it is how an unset variable usually looks
		{[]string{"  ", "\t", "real"}, "real"},
		{[]string{"", ""}, ""},
		{nil, ""},
	}

	for _, test := range tests {
		if got := firstNonEmpty(test.values...); got != test.want {
			t.Errorf("firstNonEmpty(%q) = %q, want %q", test.values, got, test.want)
		}
	}
}

// The usage text is what a user sees when they get it wrong, so it has to name
// the things they can actually do - and nothing they cannot.
func TestUsageDescribesTheRealCommands(t *testing.T) {
	original := os.Stderr

	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}

	os.Stderr = write

	usage()

	write.Close()

	os.Stderr = original

	var builder strings.Builder

	buffer := make([]byte, 4096)

	for {
		n, err := read.Read(buffer)

		builder.Write(buffer[:n])

		if err != nil {
			break
		}
	}

	text := builder.String()

	for _, want := range []string{"zot [flags] <order.yaml>", "zot new", "zot config", "zot sessions", "--resume"} {
		if !strings.Contains(text, want) {
			t.Errorf("usage does not mention %q:\n%s", want, text)
		}
	}

	// ACP is gone: zot runs unattended and has no protocol server
	if strings.Contains(strings.ToLower(text), "acp") {
		t.Errorf("usage still mentions acp:\n%s", text)
	}
}

// The CLI uses pflag (GNU-style), so a flag may appear AFTER the positional
// order paths: `zot orders/a.yaml --plain` parses --plain as a flag and keeps
// the paths intact. The stdlib flag package stopped at the first non-flag,
// folding --plain into the positionals - this locks the behaviour that
// motivated the switch.
func TestFlagsAfterThePositionalOrdersAreParsed(t *testing.T) {
	set := pflag.NewFlagSet("zot", pflag.ContinueOnError)
	plain := set.Bool("plain", false, "")

	if err := set.Parse([]string{"do", "the", "thing", "--plain"}); err != nil {
		t.Fatalf("parse: %v", err)
	}

	if !*plain {
		t.Error("--plain given after the task must be parsed as a flag, not swallowed into it")
	}

	if got := strings.Join(set.Args(), " "); got != "do the thing" {
		t.Errorf("positional task = %q, want the words before the flag", got)
	}
}

// Command-line values win over the file and the environment - but a boolean that
// was never passed must not overwrite one the config enabled.
func TestApplyOverrides(t *testing.T) {
	base := func() zot.Config {
		cfg := config.Defaults()
		cfg.UI.Diff = true
		cfg.UI.Plain = true

		return cfg
	}

	t.Run("scalars override when set", func(t *testing.T) {
		cfg := base()

		applyOverrides(&cfg, overrides{
			Provider:      "groq",
			Model:         "glm-5.2",
			MaxIterations: 12,
			Color:         "always",
		})

		if cfg.DefaultProvider != "groq" {
			t.Errorf("provider = %q", cfg.DefaultProvider)
		}

		if cfg.Agent.Model != "glm-5.2" {
			t.Errorf("model = %q", cfg.Agent.Model)
		}

		if cfg.Agent.MaxIterations != 12 {
			t.Errorf("max iterations = %d", cfg.Agent.MaxIterations)
		}

		if cfg.UI.Color != "always" {
			t.Errorf("color = %q", cfg.UI.Color)
		}
	})

	t.Run("empty scalars leave the config alone", func(t *testing.T) {
		cfg := base()

		before := cfg.Agent.Model

		applyOverrides(&cfg, overrides{})

		if cfg.Agent.Model != before {
			t.Errorf("model = %q, want %q untouched", cfg.Agent.Model, before)
		}

		if cfg.Agent.MaxIterations <= 0 {
			t.Error("a zero max-iterations must not clear the configured value")
		}
	})

	t.Run("an unpassed boolean does not turn a configured one off", func(t *testing.T) {
		cfg := base()

		applyOverrides(&cfg, overrides{Diff: false, Plain: false})

		if !cfg.UI.Diff {
			t.Error("--diff was never passed; the configured value must stand")
		}

		if !cfg.UI.Plain {
			t.Error("--plain was never passed; the configured value must stand")
		}
	})

	t.Run("a passed boolean does override", func(t *testing.T) {
		cfg := base()

		applyOverrides(&cfg, overrides{
			Diff:   false,
			Plain:  false,
			Passed: map[string]bool{"diff": true, "plain": true},
		})

		if cfg.UI.Diff {
			t.Error("--diff=false was passed and must win")
		}

		if cfg.UI.Plain {
			t.Error("--plain=false was passed and must win")
		}
	})
}

// editConfig is the setup path: it must create the config from the template on
// first run, and say something useful when there is no editor to open it with.
func TestEditConfigSeedsTheTemplate(t *testing.T) {
	dir := t.TempDir()

	t.Setenv("ZOT_CONFIG", filepath.Join(dir, "nested", "config.yaml"))
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "")
	t.Setenv("PATH", dir) // no nano/vi/vim reachable

	err := editConfig()

	// with no editor available it must fail loudly rather than silently doing
	// nothing - but the file it would have opened must exist by then
	if err == nil {
		t.Fatal("expected an error when no editor is available")
	}

	if !strings.Contains(err.Error(), "editor") {
		t.Errorf("the error should mention the missing editor: %v", err)
	}

	if _, statErr := os.Stat(config.DefaultConfigPath()); statErr != nil {
		t.Errorf("the config should have been seeded from the template: %v", statErr)
	}
}

func TestEditConfigOpensTheConfiguredEditor(t *testing.T) {
	dir := t.TempDir()

	path := filepath.Join(dir, "config.yaml")

	t.Setenv("ZOT_CONFIG", path)

	// a no-op "editor" that just succeeds
	t.Setenv("VISUAL", "true")

	if err := editConfig(); err != nil {
		t.Fatalf("editConfig: %v", err)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read seeded config: %v", err)
	}

	if len(content) == 0 {
		t.Error("the seeded config is empty")
	}
}

// withArgs runs a function with a fresh flag set and the given argv, so run()
// can be exercised the way the shell invokes it.
func withArgs(t *testing.T, args ...string) {
	t.Helper()

	originalArgs := os.Args
	originalFlags := pflag.CommandLine

	os.Args = append([]string{"zot"}, args...)
	pflag.CommandLine = pflag.NewFlagSet("zot", pflag.ContinueOnError)
	pflag.CommandLine.SetOutput(io.Discard)

	t.Cleanup(func() {
		os.Args = originalArgs
		pflag.CommandLine = originalFlags
	})
}

func TestRunConfigPath(t *testing.T) {
	t.Setenv("ZOT_CONFIG", "/some/where/config.yaml")

	withArgs(t, "config", "path")

	output, err := captureStdout(t, run)
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if !strings.Contains(output, "/some/where/config.yaml") {
		t.Errorf("output = %q, want the config path", output)
	}
}

func TestRunVersion(t *testing.T) {
	withArgs(t, "--version")

	output, err := captureStdout(t, run)
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if !strings.Contains(output, "zot") {
		t.Errorf("output = %q, want a version line", output)
	}

	// the build kind is on the version line because it changes what the binary
	// reads from disk
	if !strings.Contains(output, buildinfo.Kind) {
		t.Errorf("output = %q, want the build kind %q in it", output, buildinfo.Kind)
	}
}

func TestRunRequiresAnOrder(t *testing.T) {
	quietStderr(t)
	withArgs(t)

	if err := run(); err == nil {
		t.Error("running with no task must be an error")
	}
}

// The whole path: argv in, config resolved, provider called, transcript out.
func TestRunEndToEnd(t *testing.T) {
	// a test must never write into the operator's real session directory
	t.Setenv("ZOT_SESSION_DIR", t.TempDir())

	turn := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")

		frames := [][]string{
			{`{"choices":[{"delta":{"content":"on it"}}]}`,
				`{"choices":[{"delta":{},"finish_reason":"stop"}]}`},
			{`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"d","type":"function","function":{"name":"success","arguments":"{\"summary\":\"complete\"}"}}]},"finish_reason":"tool_calls"}]}`},
		}

		index := turn
		if index >= len(frames) {
			index = len(frames) - 1
		}

		turn++

		for _, frame := range frames[index] {
			fmt.Fprintf(w, "data: %s\n\n", frame)
		}

		fmt.Fprint(w, "data: [DONE]\n\n")
	}))

	defer server.Close()

	workdir := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.yaml")

	configYAML := fmt.Sprintf(`
agent:
  model: test-model
  max_iterations: 5

ui:
  plain: true

default_provider: local

providers:
  local:
    driver: custom
    base_url: %s
    api_key: test-key
`, server.URL)

	if err := os.WriteFile(configPath, []byte(configYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	withArgs(t, "--config", configPath, "--dir", workdir, orderFile(t, "do the thing"))

	output, err := captureStdout(t, run)
	if err != nil {
		t.Fatalf("run: %v\n%s", err, output)
	}

	for _, want := range []string{"do the thing", "on it", "complete"} {
		if !strings.Contains(output, want) {
			t.Errorf("transcript is missing %q:\n%s", want, output)
		}
	}
}

// A batch is N independent runs: each order gets its own session and its own
// recorded outcome, and the batch stops at the first order that does not end in
// success - later orders usually assume the earlier ones landed.
func TestRunABatchOfOrders(t *testing.T) {
	settle := func(name, args string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")

			fmt.Fprintf(w, "data: %s\n\n", fmt.Sprintf(
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"d","type":"function","function":{"name":%q,"arguments":%q}}]},"finish_reason":"tool_calls"}]}`,
				name, args))

			fmt.Fprint(w, "data: [DONE]\n\n")
		}))
	}

	configFor := func(t *testing.T, url string) string {
		t.Helper()

		path := filepath.Join(t.TempDir(), "config.yaml")

		if err := os.WriteFile(path, []byte(fmt.Sprintf(`
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
`, url)), 0o644); err != nil {
			t.Fatal(err)
		}

		return path
	}

	t.Run("every order gets its own run and session", func(t *testing.T) {
		sessions := t.TempDir()

		t.Setenv("ZOT_SESSION_DIR", sessions)

		server := settle("success", `{"summary":"complete"}`)
		defer server.Close()

		withArgs(t, "--config", configFor(t, server.URL), "--dir", t.TempDir(),
			orderFile(t, "the first order"), orderFile(t, "the second order"))

		if _, err := captureStdout(t, run); err != nil {
			t.Fatalf("run: %v", err)
		}

		entries, err := session.List(sessions)
		if err != nil {
			t.Fatalf("List: %v", err)
		}

		if len(entries) != 2 {
			t.Fatalf("got %d sessions, want one per order", len(entries))
		}

		// newest first: each session carries its own order's objective, not a
		// blend of the batch
		if entries[0].Task != "the second order" || entries[1].Task != "the first order" {
			t.Errorf("session tasks = %q, %q", entries[0].Task, entries[1].Task)
		}
	})

	t.Run("the batch stops at the first failed order", func(t *testing.T) {
		sessions := t.TempDir()

		t.Setenv("ZOT_SESSION_DIR", sessions)

		server := settle("failure", `{"reason":"cannot"}`)
		defer server.Close()

		first := orderFile(t, "the doomed order")

		withArgs(t, "--config", configFor(t, server.URL), "--dir", t.TempDir(),
			first, orderFile(t, "the never-run order"))

		var err error

		quietStderr(t)

		if _, err = captureStdout(t, run); err == nil {
			t.Fatal("a failed order must fail the batch")
		}

		if !strings.Contains(err.Error(), first) {
			t.Errorf("the error should name the order that stopped the batch: %v", err)
		}

		entries, listErr := session.List(sessions)
		if listErr != nil {
			t.Fatalf("List: %v", listErr)
		}

		if len(entries) != 1 {
			t.Fatalf("got %d sessions - the second order must never have run", len(entries))
		}
	})
}

// An explicitly passed --max-iterations is the operator's last word. A per-model
// max_iterations is applied when the run resolves - after the command line has
// been layered into the config - so the file used to quietly win: `zot
// --max-iterations 4` against a model capped at 1 stopped after a single
// iteration. Counting provider calls is the only honest way to ask which limit
// the engine enforced.
func TestExplicitMaxIterationsBeatsAPerModelCap(t *testing.T) {
	t.Setenv("ZOT_SESSION_DIR", t.TempDir())

	var requests atomic.Int32

	// never finishes on its own: every turn is a non-terminal tool call, so the
	// run ends only when it runs out of iterations
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")

		step := requests.Add(1)

		fmt.Fprintf(w, "data: %s\n\n", fmt.Sprintf(
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c%d","type":"function","function":{"name":"progress","arguments":"{\"current\":\"step %d\"}"}}]},"finish_reason":"tool_calls"}]}`,
			step, step))

		fmt.Fprint(w, "data: [DONE]\n\n")
	}))

	defer server.Close()

	configPath := filepath.Join(t.TempDir(), "config.yaml")

	if err := os.WriteFile(configPath, []byte(fmt.Sprintf(`
agent:
  model: capped
  max_iterations: 9
ui:
  plain: true
default_provider: local
providers:
  local:
    driver: custom
    base_url: %s
    api_key: test-key
    models:
      capped:
        model: test-model
        max_iterations: 1
`, server.URL)), 0o644); err != nil {
		t.Fatal(err)
	}

	withArgs(t, "--config", configPath, "--dir", t.TempDir(), "--max-iterations", "4", orderFile(t, "a task"))

	// exhausting the iteration budget is how this run ends, so the error is the
	// expected outcome - what matters is the budget it exhausted
	_, err := captureStdout(t, run)
	if err == nil {
		t.Fatal("a run that never records an outcome must end on its iteration cap")
	}

	if !strings.Contains(err.Error(), "stopped after 4 iterations") {
		t.Errorf("run stopped with %v, want the command-line cap of 4 to be the one enforced", err)
	}

	if got := requests.Load(); got != 4 {
		t.Errorf("the run made %d provider calls, want the 4 the command line allowed", got)
	}
}

func TestRunRejectsAnInvalidConfig(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")

	// a provider that names no known driver and has no endpoint
	if err := os.WriteFile(configPath, []byte(`
default_provider: nowhere
providers:
  nowhere: {}
`), 0o644); err != nil {
		t.Fatal(err)
	}

	withArgs(t, "--config", configPath, orderFile(t, "a task"))

	if err := run(); err == nil {
		t.Error("an unreachable provider must fail before any request")
	}
}

func TestRunRejectsAMissingConfigFile(t *testing.T) {
	withArgs(t, "--config", filepath.Join(t.TempDir(), "nope.yaml"), orderFile(t, "a task"))

	if err := run(); err == nil {
		t.Error("an explicit but missing --config must be an error")
	}
}

// withReleaseAPI makes the process look like a released binary of the given
// version and points the update check at a local server, so the wiring can be
// exercised without reaching GitHub.
func withReleaseAPI(t *testing.T, current string, handler http.HandlerFunc) {
	t.Helper()

	server := httptest.NewServer(handler)

	originalVersion, originalAPI := version.Version, version.APIBase

	version.Version, version.APIBase = current, server.URL

	t.Cleanup(func() {
		version.Version, version.APIBase = originalVersion, originalAPI

		server.Close()
	})
}

// A released binary that is behind has to say so, and the notice belongs on
// stderr: stdout is the run's transcript, and a nag folded into it would corrupt
// whatever is reading the output.
func TestAnOutdatedReleaseIsReportedOnStderr(t *testing.T) {
	withReleaseAPI(t, "v1.0.0", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"tag_name":"v2.0.0","html_url":"https://example.com/releases/v2.0.0"}`)
	})

	notice, err := captureStderr(t, func() error {
		report := checkForUpdate()

		report(os.Stderr)

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{"v1.0.0", "v2.0.0", "https://example.com/releases/v2.0.0"} {
		if !strings.Contains(notice, want) {
			t.Errorf("the update notice is missing %q:\n%s", want, notice)
		}
	}
}

// The check is a convenience that must never cost the operator anything: an
// up-to-date build must not nag, a development build must not call GitHub at
// all, and a failed lookup must be swallowed - zot runs unattended, and there is
// nobody there to act on "the update check failed".
func TestTheUpdateCheckStaysSilentWhenItHasNothingToSay(t *testing.T) {
	tests := []struct {
		name    string
		current string
		handler http.HandlerFunc
	}{
		{
			name:    "already on the latest release",
			current: "v2.0.0",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				fmt.Fprint(w, `{"tag_name":"v2.0.0","html_url":"https://example.com/releases/v2.0.0"}`)
			},
		},
		{
			name:    "a rate-limited api",
			current: "v1.0.0",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusForbidden)
			},
		},
		{
			name:    "an unparseable response",
			current: "v1.0.0",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				fmt.Fprint(w, "not json")
			},
		},
		{
			name:    "a development build",
			current: "dev",
			handler: func(_ http.ResponseWriter, _ *http.Request) {
				t.Error("a development build must not call the release API at all")
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			withReleaseAPI(t, test.current, test.handler)

			notice, err := captureStderr(t, func() error {
				report := checkForUpdate()

				report(os.Stderr)

				return nil
			})
			if err != nil {
				t.Fatal(err)
			}

			if notice != "" {
				t.Errorf("wrote %q, want nothing", notice)
			}
		})
	}
}

// The check has to be wired into a real run, on the far side of it: the notice
// only reaches the operator if `zot "do X"` reports it once the viewer has
// released the screen.
func TestARunReportsAnAvailableUpdate(t *testing.T) {
	t.Setenv("ZOT_SESSION_DIR", t.TempDir())

	withReleaseAPI(t, "v1.0.0", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"tag_name":"v9.9.9","html_url":"https://example.com/releases/v9.9.9"}`)
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")

		fmt.Fprintf(w, "data: %s\n\n",
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"d","type":"function","function":{"name":"success","arguments":"{\"summary\":\"complete\"}"}}]},"finish_reason":"tool_calls"}]}`)

		fmt.Fprint(w, "data: [DONE]\n\n")
	}))

	defer server.Close()

	configPath := filepath.Join(t.TempDir(), "config.yaml")

	if err := os.WriteFile(configPath, fmt.Appendf(nil, `
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
`, server.URL), 0o644); err != nil {
		t.Fatal(err)
	}

	withArgs(t, "--config", configPath, "--dir", t.TempDir(), orderFile(t, "a task"))

	var transcript string

	notice, err := captureStderr(t, func() error {
		var runErr error

		transcript, runErr = captureStdout(t, run)

		return runErr
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if !strings.Contains(notice, "v9.9.9") {
		t.Errorf("the run did not report the available update:\n%s", notice)
	}

	// and the transcript is still only the transcript
	if strings.Contains(transcript, "v9.9.9") {
		t.Errorf("the update notice leaked into stdout:\n%s", transcript)
	}
}

// captureStdout collects what a function prints to stdout.
func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()

	return capture(t, &os.Stdout, fn)
}

// captureStderr collects what a function prints to stderr. Stdout and stderr are
// worth telling apart: stdout is the transcript, stderr is where zot talks about
// itself, and something that belongs on one must not leak onto the other.
func captureStderr(t *testing.T, fn func() error) (string, error) {
	t.Helper()

	return capture(t, &os.Stderr, fn)
}

// capture redirects one of the process's standard streams for the duration of a
// call and returns what was written to it.
func capture(t *testing.T, stream **os.File, fn func() error) (string, error) {
	t.Helper()

	original := *stream

	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}

	*stream = write

	done := make(chan string)

	go func() {
		var builder strings.Builder

		buffer := make([]byte, 4096)

		for {
			n, err := read.Read(buffer)

			builder.Write(buffer[:n])

			if err != nil {
				break
			}
		}

		done <- builder.String()
	}()

	runErr := fn()

	write.Close()

	*stream = original

	return <-done, runErr
}

// A run leaves a record, and that record is enough to pick the work up again.
// This is the whole promise of session logs, exercised end to end.
func TestRunRecordsAndResumesASession(t *testing.T) {
	sessions := t.TempDir()

	t.Setenv("ZOT_SESSION_DIR", sessions)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")

		fmt.Fprintf(w, "data: %s\n\n",
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"d","type":"function","function":{"name":"success","arguments":"{\"summary\":\"complete\"}"}}]},"finish_reason":"tool_calls"}]}`)

		fmt.Fprint(w, "data: [DONE]\n\n")
	}))

	defer server.Close()

	workdir := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.yaml")

	configYAML := fmt.Sprintf(`
agent:
  model: test-model
  max_iterations: 5

ui:
  plain: true

default_provider: local

providers:
  local:
    driver: custom
    base_url: %s
    api_key: test-key
`, server.URL)

	if err := os.WriteFile(configPath, []byte(configYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	withArgs(t, "--config", configPath, "--dir", workdir, orderFile(t, "the first task"))

	if _, err := captureStdout(t, run); err != nil {
		t.Fatalf("run: %v", err)
	}

	entries, err := session.List(sessions)
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if len(entries) != 1 {
		t.Fatalf("got %d sessions, want the one the run wrote", len(entries))
	}

	if entries[0].Task != "the first task" || !entries[0].Complete {
		t.Errorf("session entry = %+v", entries[0])
	}

	first, err := session.Load(entries[0].Path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// the task is the durable objective, recorded in the meta (and placed in the
	// instructions), not as the opening user message
	if first.Meta.Task != "the first task" {
		t.Errorf("meta task = %q, want the objective", first.Meta.Task)
	}

	if len(first.Messages) == 0 {
		t.Fatalf("the log must record the opening message: %+v", first.Messages)
	}

	if first.Meta.Model != "test-model" || first.Meta.Workdir == "" {
		t.Errorf("meta = %+v", first.Meta)
	}

	// `zot sessions` has to surface it, because a log nobody can find is a log
	// nobody uses
	withArgs(t, "sessions", "--session-dir", sessions)

	listing, err := captureStdout(t, run)
	if err != nil {
		t.Fatalf("sessions: %v", err)
	}

	if !strings.Contains(listing, entries[0].ID) || !strings.Contains(listing, "the first task") {
		t.Errorf("listing = %q", listing)
	}

	// now resume it: the new run replays the old conversation and continues,
	// rather than starting over
	withArgs(t, "--config", configPath, "--dir", workdir, "--resume", "last")

	if _, err := captureStdout(t, run); err != nil {
		t.Fatalf("resumed run: %v", err)
	}

	entries, err = session.List(sessions)
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if len(entries) != 2 {
		t.Fatalf("a resumed run must write its own log, got %d", len(entries))
	}

	second, err := session.Load(entries[0].Path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if second.Meta.ResumedFrom != first.Meta.ID {
		t.Errorf("ResumedFrom = %q, want %q", second.Meta.ResumedFrom, first.Meta.ID)
	}

	// the objective carries over from the resumed session - a resume continues
	// the order the session was started with, never replaces it
	if second.Meta.Task != "the first task" {
		t.Errorf("resumed task = %q, want the original objective preserved", second.Meta.Task)
	}
}

// Resuming with no new instruction continues the original brief, which is what
// restarting an interrupted overnight run means - so it is a normal invocation,
// not a mistake. `zot --resume last` used to print the whole usage block to
// stderr on its way to working correctly, because the empty-task check ran
// before the session's objective was inherited.
func TestResumeWithoutATaskReusesTheOriginal(t *testing.T) {
	sessions := t.TempDir()

	t.Setenv("ZOT_SESSION_DIR", sessions)

	writer, err := session.Create(sessions, "20260805-090000", session.Meta{Task: "the original brief"})
	if err != nil {
		t.Fatal(err)
	}

	_ = writer.Result(session.Result{Reason: "settled"})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")

		fmt.Fprintf(w, "data: %s\n\n",
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"d","type":"function","function":{"name":"success","arguments":"{\"summary\":\"complete\"}"}}]},"finish_reason":"tool_calls"}]}`)

		fmt.Fprint(w, "data: [DONE]\n\n")
	}))

	defer server.Close()

	configPath := filepath.Join(t.TempDir(), "config.yaml")

	if err := os.WriteFile(configPath, []byte(fmt.Sprintf(`
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
`, server.URL)), 0o644); err != nil {
		t.Fatal(err)
	}

	withArgs(t, "--config", configPath, "--dir", t.TempDir(), "--resume", "last")

	var output string

	diagnostics, err := captureStderr(t, func() error {
		var runErr error

		output, runErr = captureStdout(t, run)

		return runErr
	})
	if err != nil {
		t.Fatalf("run: %v\n%s", err, output)
	}

	if !strings.Contains(output, "the original brief") {
		t.Errorf("the resumed run should carry the original brief:\n%s", output)
	}

	// the resume line is expected on stderr; the help text is not - printing it
	// tells the operator their invocation was wrong when it was exactly right
	if !strings.Contains(diagnostics, "resuming") {
		t.Errorf("stderr should say which session is being resumed:\n%s", diagnostics)
	}

	for _, unwanted := range []string{"Usage:", "Commands:", "Examples:"} {
		if strings.Contains(diagnostics, unwanted) {
			t.Errorf("a resume with no new task printed the usage block (%q):\n%s", unwanted, diagnostics)
		}
	}
}

func TestResumeOfAnUnknownSessionFails(t *testing.T) {
	t.Setenv("ZOT_SESSION_DIR", t.TempDir())

	withArgs(t, "--resume", "nope")

	if err := run(); err == nil {
		t.Error("resuming a session that does not exist must be an error")
	}
}

func TestNoSessionWritesNothing(t *testing.T) {
	sessions := t.TempDir()

	t.Setenv("ZOT_SESSION_DIR", sessions)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")

		fmt.Fprintf(w, "data: %s\n\n",
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"d","type":"function","function":{"name":"success","arguments":"{\"summary\":\"complete\"}"}}]},"finish_reason":"tool_calls"}]}`)

		fmt.Fprint(w, "data: [DONE]\n\n")
	}))

	defer server.Close()

	configPath := filepath.Join(t.TempDir(), "config.yaml")

	if err := os.WriteFile(configPath, []byte(fmt.Sprintf(`
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
`, server.URL)), 0o644); err != nil {
		t.Fatal(err)
	}

	withArgs(t, "--config", configPath, "--dir", t.TempDir(), "--no-session", orderFile(t, "a task"))

	if _, err := captureStdout(t, run); err != nil {
		t.Fatalf("run: %v", err)
	}

	entries, err := session.List(sessions)
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if len(entries) != 0 {
		t.Errorf("--no-session must leave nothing behind, got %+v", entries)
	}
}

func TestSessionsListingWhenThereAreNone(t *testing.T) {
	withArgs(t, "sessions", "--session-dir", filepath.Join(t.TempDir(), "empty"))

	output, err := captureStdout(t, run)
	if err != nil {
		t.Fatalf("sessions: %v", err)
	}

	if !strings.Contains(output, "no sessions") {
		t.Errorf("output = %q", output)
	}
}

// A multi-line brief must not turn the listing into a wall of text - and
// truncating it must not cut a character in half: byte slicing a task written in
// CJK or carrying an emoji left a mangled rune in the `zot sessions` listing.
func TestOneLine(t *testing.T) {
	tests := []struct {
		in    string
		width int
		want  string
	}{
		{in: "short", width: 10, want: "short"},
		{in: "a\nmulti\nline   task", width: 40, want: "a multi line task"},
		{in: strings.Repeat("x", 20), width: 10, want: strings.Repeat("x", 9) + "\u2026"},
		{in: "  padded  ", width: 20, want: "padded"},
		{in: "", width: 10, want: ""},
		// twelve characters but thirty-six bytes: a byte-width cap would both
		// truncate a string that fits and split the character it stopped inside
		{in: "\u65e5\u672c\u8a9e\u306e\u30bf\u30b9\u30af\u8aac\u660e\u6587\u3067\u3059", width: 40, want: "\u65e5\u672c\u8a9e\u306e\u30bf\u30b9\u30af\u8aac\u660e\u6587\u3067\u3059"},
		{in: "\u65e5\u672c\u8a9e\u306e\u30bf\u30b9\u30af\u8aac\u660e\u6587\u3067\u3059", width: 6, want: "\u65e5\u672c\u8a9e\u306e\u30bf\u2026"},
		{in: strings.Repeat("\U0001F680", 5), width: 3, want: strings.Repeat("\U0001F680", 2) + "\u2026"},
	}

	for _, test := range tests {
		if got := oneLine(test.in, test.width); got != test.want {
			t.Errorf("oneLine(%q, %d) = %q, want %q", test.in, test.width, got, test.want)
		}
	}
}
