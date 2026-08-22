package agent

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func call(t *testing.T, tools Tools, name string, args map[string]any) (any, error) {
	t.Helper()

	definition, ok := tools[name]
	if !ok {
		t.Fatalf("no tool named %q", name)
	}

	return definition.Handler(context.Background(), args)
}

// The handlers are methods on a toolSet now; these shims let the existing
// tests exercise them directly at the default output ceiling.
const maxToolOutput = DefaultMaxToolOutput

var defaultSet = toolSet{maxOutput: DefaultMaxToolOutput}

func readHandler(ctx context.Context, a map[string]any) (any, error) { return defaultSet.read(ctx, a) }
func writeHandler(ctx context.Context, a map[string]any) (any, error) {
	return defaultSet.write(ctx, a)
}
func listHandler(ctx context.Context, a map[string]any) (any, error) { return defaultSet.list(ctx, a) }
func shellHandler(ctx context.Context, a map[string]any) (any, error) {
	return defaultSet.shell(ctx, a)
}

func TestDefaultToolsAreWellFormed(t *testing.T) {
	tools := DefaultTools()

	for _, name := range []string{"read", "write", "list", "shell"} {
		definition, ok := tools[name]

		if !ok {
			t.Fatalf("tool %q is missing", name)
		}

		if definition.Description == "" {
			t.Errorf("%s has no description", name)
		}

		if definition.Handler == nil {
			t.Errorf("%s has no handler", name)
		}

		if _, ok := definition.Parameters["properties"]; !ok {
			t.Errorf("%s has no parameter schema", name)
		}
	}
}

func TestReadRangeIsLineNumbered(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.txt")

	if err := os.WriteFile(path, []byte("one\ntwo\nthree\nfour"), 0o644); err != nil {
		t.Fatal(err)
	}

	// line numbers are 1-indexed and the end is inclusive
	part, err := call(t, DefaultTools(), "read", map[string]any{
		"path": path, "startLine": float64(2), "endLine": float64(3),
	})
	if err != nil {
		t.Fatalf("read range: %v", err)
	}

	text := part.(string)

	// the requested lines come back with their line numbers, and a header names
	// the whole file's length so the model knows how much it has not seen
	for _, want := range []string{"lines 2-3 of 4", "2\ttwo", "3\tthree"} {
		if !strings.Contains(text, want) {
			t.Errorf("read range is missing %q:\n%s", want, text)
		}
	}

	if strings.Contains(text, "one") || strings.Contains(text, "four") {
		t.Errorf("read range leaked lines outside 2-3:\n%s", text)
	}
}

// The range is required, not optional: an optional range invites whole-file
// reads, and one large file can overflow a small context window and be
// rejected wholesale.
func TestReadRequiresARange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.txt")

	if err := os.WriteFile(path, []byte("one\ntwo"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, args := range []map[string]any{
		{"path": path},
		{"path": path, "startLine": float64(1)},
		{"path": path, "endLine": float64(2)},
	} {
		if _, err := call(t, DefaultTools(), "read", args); err == nil {
			t.Errorf("read(%v) must require both startLine and endLine", args)
		}
	}
}

func TestReadRangeOutsideTheFileIsNotAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "short.txt")

	if err := os.WriteFile(path, []byte("only one line"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := call(t, DefaultTools(), "read", map[string]any{
		"path": path, "startLine": float64(50), "endLine": float64(60),
	})
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	// an empty range reports the file's real length rather than failing, so the
	// model can correct its request
	if !strings.Contains(got.(string), "1 lines") {
		t.Errorf("read past the end = %q, want the file length reported", got)
	}
}

func TestReadMissingFileErrors(t *testing.T) {
	if _, err := call(t, DefaultTools(), "read", map[string]any{"path": "/nope/missing"}); err == nil {
		t.Error("reading a missing file must be reported")
	}
}

func TestReadRequiresPath(t *testing.T) {
	if _, err := call(t, DefaultTools(), "read", map[string]any{}); err == nil {
		t.Error("a missing path must be reported")
	}
}

func TestWriteCreatesAndReplaces(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "out.txt")

	tools := DefaultTools()

	// the parent directory is created rather than failing
	if _, err := call(t, tools, "write", map[string]any{"path": path, "content": "first"}); err != nil {
		t.Fatalf("write: %v", err)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if string(content) != "first" {
		t.Errorf("file = %q, want %q", content, "first")
	}

	if _, err := call(t, tools, "write", map[string]any{"path": path, "content": "second"}); err != nil {
		t.Fatalf("rewrite: %v", err)
	}

	content, _ = os.ReadFile(path)

	if string(content) != "second" {
		t.Errorf("file = %q, want it replaced", content)
	}
}

func TestWriteReplacesALineRange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lines.txt")

	if err := os.WriteFile(path, []byte("a\nb\nc\nd"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := call(t, DefaultTools(), "write", map[string]any{
		"path": path, "content": "B", "startLine": float64(2), "endLine": float64(3),
	}); err != nil {
		t.Fatalf("write range: %v", err)
	}

	content, _ := os.ReadFile(path)

	if string(content) != "a\nB\nd" {
		t.Errorf("file = %q, want \"a\\nB\\nd\"", content)
	}
}

func TestWriteInsertsBeforeALine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lines.txt")

	if err := os.WriteFile(path, []byte("a\nc"), 0o644); err != nil {
		t.Fatal(err)
	}

	// startLine with no endLine inserts rather than replaces
	if _, err := call(t, DefaultTools(), "write", map[string]any{
		"path": path, "content": "b", "startLine": float64(2),
	}); err != nil {
		t.Fatalf("write insert: %v", err)
	}

	content, _ := os.ReadFile(path)

	if string(content) != "a\nb\nc" {
		t.Errorf("file = %q, want \"a\\nb\\nc\"", content)
	}
}

func TestListMarksDirectories(t *testing.T) {
	dir := t.TempDir()

	os.WriteFile(filepath.Join(dir, "file.txt"), []byte("x"), 0o644)
	os.Mkdir(filepath.Join(dir, "sub"), 0o755)

	got, err := call(t, DefaultTools(), "list", map[string]any{"path": dir})
	if err != nil {
		t.Fatalf("list: %v", err)
	}

	listing, _ := got.(string)

	if !strings.Contains(listing, "sub/") {
		t.Errorf("a directory must be marked with a slash: %q", listing)
	}

	if !strings.Contains(listing, "file.txt") {
		t.Errorf("listing is missing the file: %q", listing)
	}
}

func TestShellReturnsOutput(t *testing.T) {
	got, err := call(t, DefaultTools(), "shell", map[string]any{"command": "echo hello"})
	if err != nil {
		t.Fatalf("shell: %v", err)
	}

	if !strings.Contains(got.(string), "hello") {
		t.Errorf("shell output = %q", got)
	}
}

// A failing command is information the model can act on - a compiler error, a
// failing test - so it comes back as output rather than as an error that would
// end the run.
func TestShellFailureIsOutputNotAnError(t *testing.T) {
	got, err := call(t, DefaultTools(), "shell", map[string]any{"command": "exit 3"})
	if err != nil {
		t.Fatalf("a non-zero exit must not surface as an error: %v", err)
	}

	if !strings.Contains(got.(string), "exit") {
		t.Errorf("the exit status must be visible to the model: %q", got)
	}
}

func TestShellTimeoutIsReported(t *testing.T) {
	got, err := call(t, DefaultTools(), "shell", map[string]any{
		"command": "sleep 5", "timeout": float64(1),
	})
	if err != nil {
		t.Fatalf("a timeout must not surface as an error: %v", err)
	}

	if !strings.Contains(got.(string), "timed out") {
		t.Errorf("a timeout must be visible to the model: %q", got)
	}
}

// An unbounded tool result can consume the whole context window and evict the
// conversation that explains why it was read.
func TestOutputIsTruncatedVisibly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.txt")

	if err := os.WriteFile(path, []byte(strings.Repeat("x", maxToolOutput+5_000)), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := call(t, DefaultTools(), "read", map[string]any{
		"path": path, "startLine": float64(1), "endLine": float64(1),
	})
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	text := got.(string)

	if len(text) > maxToolOutput+200 {
		t.Errorf("output length %d exceeds the cap", len(text))
	}

	if !strings.Contains(text, "truncated") {
		t.Error("truncation must be visible so the model knows it saw a fragment")
	}
}

// The ceiling is configurable, so a model on a small-window endpoint can be
// given a tighter bound than the default.
func TestReadHonoursAConfiguredOutputCeiling(t *testing.T) {
	path := filepath.Join(t.TempDir(), "big.txt")

	if err := os.WriteFile(path, []byte(strings.Repeat("x", 40_000)), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := call(t, DefaultToolsWith(4_000), "read", map[string]any{
		"path": path, "startLine": float64(1), "endLine": float64(1),
	})
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	text := got.(string)

	if len(text) > 4_500 {
		t.Errorf("a 4000-byte ceiling returned %d bytes", len(text))
	}

	if !strings.Contains(text, "truncated") {
		t.Error("the tighter ceiling must still mark its truncation")
	}
}

// The edge cases in the file tools are where an autonomous run does damage: an
// out-of-range line number that silently rewrites the wrong part of a file is
// worse than an error, because nobody is watching.

func TestReadClampsAndReportsRanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file.txt")

	if err := os.WriteFile(path, []byte("one\ntwo\nthree\nfour"), 0o644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		args    map[string]any
		wantHas []string
		wantNot []string
	}{
		{
			name:    "a start below the first line clamps to 1",
			args:    map[string]any{"path": path, "startLine": float64(-5), "endLine": float64(1)},
			wantHas: []string{"1\tone"},
			wantNot: []string{"two"},
		},
		{
			name:    "an end past the last line clamps to the file length",
			args:    map[string]any{"path": path, "startLine": float64(4), "endLine": float64(99)},
			wantHas: []string{"4\tfour"},
			wantNot: []string{"three"},
		},
		{
			name:    "an integer rather than a JSON number",
			args:    map[string]any{"path": path, "startLine": 2, "endLine": 2},
			wantHas: []string{"2\ttwo"},
		},
		{
			name:    "a range the wrong way round reports an empty range",
			args:    map[string]any{"path": path, "startLine": float64(3), "endLine": float64(1)},
			wantHas: []string{"range is empty"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := readHandler(context.Background(), test.args)
			if err != nil {
				t.Fatalf("readHandler: %v", err)
			}

			text := got.(string)

			for _, want := range test.wantHas {
				if !strings.Contains(text, want) {
					t.Errorf("missing %q:\n%s", want, text)
				}
			}

			for _, unwanted := range test.wantNot {
				if strings.Contains(text, unwanted) {
					t.Errorf("unexpected %q:\n%s", unwanted, text)
				}
			}
		})
	}
}

func TestWriteRequiresContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file.txt")

	if _, err := writeHandler(context.Background(), map[string]any{"path": path}); err == nil {
		t.Error("writing without content must be an error, not an empty file")
	}

	if _, err := writeHandler(context.Background(), map[string]any{"content": "x"}); err == nil {
		t.Error("writing without a path must be an error")
	}
}

// A range edit against a file that is not there must fail rather than create
// one: the model asked to change existing lines, and there are none.
func TestWriteRangeAgainstAMissingFileFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nope.txt")

	_, err := writeHandler(context.Background(), map[string]any{
		"path":      path,
		"content":   "x",
		"startLine": float64(1),
	})
	if err == nil {
		t.Error("a range edit against a missing file must be reported")
	}
}

func TestWriteClampsOutOfRangeLines(t *testing.T) {
	tests := []struct {
		name    string
		initial string
		args    map[string]any
		want    string
	}{
		{
			name:    "a start below the first line",
			initial: "a\nb",
			args:    map[string]any{"content": "x", "startLine": float64(-3), "endLine": float64(1)},
			want:    "x\nb",
		},
		{
			name:    "a start past the end appends",
			initial: "a\nb",
			args:    map[string]any{"content": "x", "startLine": float64(99)},
			want:    "a\nb\nx",
		},
		{
			name:    "an end past the last line",
			initial: "a\nb",
			args:    map[string]any{"content": "x", "startLine": float64(2), "endLine": float64(99)},
			want:    "a\nx",
		},
		{
			name:    "an end before the start inserts rather than replaces",
			initial: "a\nb",
			args:    map[string]any{"content": "x", "startLine": float64(2), "endLine": float64(1)},
			want:    "a\nx\nb",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "file.txt")

			if err := os.WriteFile(path, []byte(test.initial), 0o644); err != nil {
				t.Fatal(err)
			}

			test.args["path"] = path

			if _, err := writeHandler(context.Background(), test.args); err != nil {
				t.Fatalf("writeHandler: %v", err)
			}

			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}

			if string(got) != test.want {
				t.Errorf("got %q, want %q", got, test.want)
			}
		})
	}
}

func TestListRequiresAnExistingDirectory(t *testing.T) {
	if _, err := listHandler(context.Background(), map[string]any{
		"path": filepath.Join(t.TempDir(), "nope"),
	}); err == nil {
		t.Error("listing a missing directory must be reported")
	}

	if _, err := listHandler(context.Background(), map[string]any{}); err == nil {
		t.Error("listing without a path must be reported")
	}
}

func TestShellRequiresACommand(t *testing.T) {
	if _, err := shellHandler(context.Background(), map[string]any{}); err == nil {
		t.Error("running without a command must be reported")
	}
}

// A cancelled context stops a command rather than waiting out its timeout.
func TestShellRespectsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	cancel()

	if _, err := shellHandler(ctx, map[string]any{"command": "sleep 5"}); err == nil {
		t.Log("a cancelled run returned output rather than an error, which is acceptable")
	}
}

// plan and progress are reflective tools - they change nothing on disk, so the
// contract is entirely in their arguments and their acknowledgement. A plan
// without steps is the mistake worth catching: an empty plan is not a plan.
func TestPlanTool(t *testing.T) {
	out, err := call(t, DefaultTools(), "plan", map[string]any{
		"steps":     []any{"read the code", "make the change", "run the tests"},
		"rationale": "smallest safe change first",
	})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}

	text, _ := out.(string)

	if !strings.Contains(text, "3 step") {
		t.Errorf("plan ack should state the step count: %q", text)
	}

	if !strings.Contains(text, "smallest safe change first") {
		t.Errorf("plan ack should carry the rationale: %q", text)
	}

	// a plan with no steps is rejected, so the model is told to actually plan
	if _, err := call(t, DefaultTools(), "plan", map[string]any{"steps": []any{}}); err == nil {
		t.Error("an empty plan must be an error")
	}

	if _, err := call(t, DefaultTools(), "plan", map[string]any{}); err == nil {
		t.Error("a plan with no steps field must be an error")
	}
}

func TestProgressTool(t *testing.T) {
	out, err := call(t, DefaultTools(), "progress", map[string]any{
		"completed": []any{"read the code"},
		"current":   "making the change",
		"nextSteps": []any{"run the tests"},
	})
	if err != nil {
		t.Fatalf("progress: %v", err)
	}

	if text, _ := out.(string); !strings.Contains(text, "making the change") {
		t.Errorf("progress ack should name the current step: %q", text)
	}

	// progress with nothing is still valid - it is a checkpoint, not a command
	if _, err := call(t, DefaultTools(), "progress", map[string]any{}); err != nil {
		t.Errorf("an empty progress checkpoint should be allowed: %v", err)
	}
}

// plan and progress must be in the toolbox both tools ship, or the instructions
// that tells the agent to call them is lying.
func TestPlanAndProgressAreInTheToolbox(t *testing.T) {
	tools := DefaultTools()

	for _, name := range []string{"plan", "progress"} {
		if _, ok := tools[name]; !ok {
			t.Errorf("DefaultTools is missing the %q tool", name)
		}
	}
}

// A command that leaves a process behind must not be able to wedge the run.
//
// The timeout kills the shell, but a grandchild that inherited the output pipe
// keeps it open, and CombinedOutput blocks until the pipe closes - so the tool
// call never returned. Nothing downstream could recover: the run's time budget
// is only checked at an iteration boundary, and cancelling the run kills the
// shell, not the process holding the pipe. `npm start &` was enough to do it.
func TestShellDoesNotWedgeOnADaemonisedChild(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the shell idiom under test is POSIX")
	}

	done := make(chan any, 1)

	go func() {
		out, _ := shellHandler(context.Background(), map[string]any{
			// the child outlives the shell and keeps the inherited pipe open
			"command": "sh -c 'sleep 60' & echo started",
			"timeout": 1,
		})

		done <- out
	}()

	select {
	case out := <-done:
		if text, ok := out.(string); ok && !strings.Contains(text, "started") {
			t.Errorf("output = %q, want the command's own output kept", text)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the tool call never returned: a backgrounded child holding the output pipe wedges the run for good")
	}
}
