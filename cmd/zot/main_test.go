package main

import (
	"flag"
	"fmt"
	"github.com/chatbotkit/zot"
	"github.com/chatbotkit/zot/internal/build"
	"github.com/chatbotkit/zot/internal/config"
	"github.com/chatbotkit/zot/internal/session"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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

	if build.Dev {
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
	if build.Dev {
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

func TestResolveTaskFromArguments(t *testing.T) {
	task, err := resolveTask("", []string{"add", "a", "health", "endpoint"})
	if err != nil {
		t.Fatalf("resolveTask: %v", err)
	}

	if task != "add a health endpoint" {
		t.Errorf("task = %q", task)
	}
}

func TestResolveTaskFromFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "task.md")

	if err := os.WriteFile(path, []byte("\n  refactor the parser  \n"), 0o644); err != nil {
		t.Fatal(err)
	}

	task, err := resolveTask(path, nil)
	if err != nil {
		t.Fatalf("resolveTask: %v", err)
	}

	if task != "refactor the parser" {
		t.Errorf("task = %q, want it trimmed", task)
	}
}

// A task file takes precedence, so a long brief does not have to survive shell
// quoting.
func TestResolveTaskFilePrecedence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "task.md")

	if err := os.WriteFile(path, []byte("from the file"), 0o644); err != nil {
		t.Fatal(err)
	}

	task, err := resolveTask(path, []string{"from", "the", "args"})
	if err != nil {
		t.Fatalf("resolveTask: %v", err)
	}

	if task != "from the file" {
		t.Errorf("task = %q, want the file to win", task)
	}
}

func TestResolveTaskErrors(t *testing.T) {
	empty := filepath.Join(t.TempDir(), "empty.md")

	if err := os.WriteFile(empty, []byte("   \n\t"), 0o644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name     string
		taskFile string
		args     []string
	}{
		// there is no interactive prompt to fall back on: zot is a viewer, so a
		// missing task has to be an error rather than a wait
		{"no task at all", "", nil},
		{"whitespace-only arguments", "", []string{"  ", "\t"}},
		{"a missing task file", filepath.Join(t.TempDir(), "nope.md"), nil},
		{"an empty task file", empty, nil},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := resolveTask(test.taskFile, test.args); err == nil {
				t.Error("expected an error")
			}
		})
	}
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

	for _, want := range []string{"zot [flags]", "zot config", "zot sessions", "--resume"} {
		if !strings.Contains(text, want) {
			t.Errorf("usage does not mention %q:\n%s", want, text)
		}
	}

	// ACP is gone: zot runs unattended and has no protocol server
	if strings.Contains(strings.ToLower(text), "acp") {
		t.Errorf("usage still mentions acp:\n%s", text)
	}
}

func TestStringSliceFlag(t *testing.T) {
	var slice stringSlice

	if err := slice.Set(" first "); err != nil {
		t.Fatalf("Set: %v", err)
	}

	if err := slice.Set("second"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	if len(slice) != 2 || slice[0] != "first" || slice[1] != "second" {
		t.Errorf("slice = %v, want trimmed and accumulated", slice)
	}

	if got := slice.String(); got != "first,second" {
		t.Errorf("String = %q", got)
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
			Backend:       "groq",
			Model:         "glm-5.2",
			MaxIterations: 12,
		})

		if cfg.DefaultBackend != "groq" {
			t.Errorf("backend = %q", cfg.DefaultBackend)
		}

		if cfg.Agent.Model != "glm-5.2" {
			t.Errorf("model = %q", cfg.Agent.Model)
		}

		if cfg.Agent.MaxIterations != 12 {
			t.Errorf("max iterations = %d", cfg.Agent.MaxIterations)
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
	originalFlags := flag.CommandLine

	os.Args = append([]string{"zot"}, args...)
	flag.CommandLine = flag.NewFlagSet("zot", flag.ContinueOnError)
	flag.CommandLine.SetOutput(io.Discard)

	t.Cleanup(func() {
		os.Args = originalArgs
		flag.CommandLine = originalFlags
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
	if !strings.Contains(output, build.Kind) {
		t.Errorf("output = %q, want the build kind %q in it", output, build.Kind)
	}
}

func TestRunRequiresATask(t *testing.T) {
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
			{`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"d","type":"function","function":{"name":"_success","arguments":"{\"summary\":\"complete\"}"}}]},"finish_reason":"tool_calls"}]}`},
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

default_backend: local

backends:
  local:
    provider: custom
    base_url: %s
    api_key: test-key
`, server.URL)

	if err := os.WriteFile(configPath, []byte(configYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	withArgs(t, "--config", configPath, "--dir", workdir, "do", "the", "thing")

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

func TestRunRejectsAnInvalidConfig(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")

	// a backend that names no known provider and has no endpoint
	if err := os.WriteFile(configPath, []byte(`
default_backend: nowhere
backends:
  nowhere: {}
`), 0o644); err != nil {
		t.Fatal(err)
	}

	withArgs(t, "--config", configPath, "a task")

	if err := run(); err == nil {
		t.Error("an unreachable backend must fail before any request")
	}
}

func TestRunRejectsAMissingConfigFile(t *testing.T) {
	withArgs(t, "--config", filepath.Join(t.TempDir(), "nope.yaml"), "a task")

	if err := run(); err == nil {
		t.Error("an explicit but missing --config must be an error")
	}
}

// captureStdout collects what a function prints.
func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()

	original := os.Stdout

	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}

	os.Stdout = write

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

	os.Stdout = original

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
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"d","type":"function","function":{"name":"_success","arguments":"{\"summary\":\"complete\"}"}}]},"finish_reason":"tool_calls"}]}`)

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

default_backend: local

backends:
  local:
    provider: custom
    base_url: %s
    api_key: test-key
`, server.URL)

	if err := os.WriteFile(configPath, []byte(configYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	withArgs(t, "--config", configPath, "--dir", workdir, "the first task")

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

	if len(first.Messages) == 0 || first.Messages[0].Text != "the first task" {
		t.Fatalf("the log must open with the brief: %+v", first.Messages)
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

	// now resume it: the new run carries the old conversation plus the new
	// instruction, so the agent continues rather than starting over
	withArgs(t, "--config", configPath, "--dir", workdir, "--resume", "last", "the follow-up")

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

	var texts []string

	for _, message := range second.Messages {
		texts = append(texts, message.Text)
	}

	joined := strings.Join(texts, "\n")

	if !strings.Contains(joined, "the first task") || !strings.Contains(joined, "the follow-up") {
		t.Errorf("a resumed log must hold both the old conversation and the new instruction:\n%s", joined)
	}
}

// Resuming with no new instruction continues the original brief, which is what
// restarting an interrupted overnight run means.
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
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"d","type":"function","function":{"name":"_success","arguments":"{\"summary\":\"complete\"}"}}]},"finish_reason":"tool_calls"}]}`)

		fmt.Fprint(w, "data: [DONE]\n\n")
	}))

	defer server.Close()

	configPath := filepath.Join(t.TempDir(), "config.yaml")

	if err := os.WriteFile(configPath, []byte(fmt.Sprintf(`
agent:
  model: test-model
ui:
  plain: true
default_backend: local
backends:
  local:
    provider: custom
    base_url: %s
    api_key: test-key
`, server.URL)), 0o644); err != nil {
		t.Fatal(err)
	}

	withArgs(t, "--config", configPath, "--dir", t.TempDir(), "--resume", "last")

	output, err := captureStdout(t, run)
	if err != nil {
		t.Fatalf("run: %v\n%s", err, output)
	}

	if !strings.Contains(output, "the original brief") {
		t.Errorf("the resumed run should carry the original brief:\n%s", output)
	}
}

func TestResumeOfAnUnknownSessionFails(t *testing.T) {
	t.Setenv("ZOT_SESSION_DIR", t.TempDir())

	withArgs(t, "--resume", "nope", "a task")

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
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"d","type":"function","function":{"name":"_success","arguments":"{\"summary\":\"complete\"}"}}]},"finish_reason":"tool_calls"}]}`)

		fmt.Fprint(w, "data: [DONE]\n\n")
	}))

	defer server.Close()

	configPath := filepath.Join(t.TempDir(), "config.yaml")

	if err := os.WriteFile(configPath, []byte(fmt.Sprintf(`
agent:
  model: test-model
ui:
  plain: true
default_backend: local
backends:
  local:
    provider: custom
    base_url: %s
    api_key: test-key
`, server.URL)), 0o644); err != nil {
		t.Fatal(err)
	}

	withArgs(t, "--config", configPath, "--dir", t.TempDir(), "--no-session", "a task")

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

// A multi-line brief must not turn the listing into a wall of text.
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
	}

	for _, test := range tests {
		if got := oneLine(test.in, test.width); got != test.want {
			t.Errorf("oneLine(%q, %d) = %q, want %q", test.in, test.width, got, test.want)
		}
	}
}
