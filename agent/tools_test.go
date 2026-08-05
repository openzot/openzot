package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func call(t *testing.T, tools Tools, name string, args map[string]any) (any, error) {
	t.Helper()

	definition, ok := tools[name]
	if !ok {
		t.Fatalf("no tool named %q", name)
	}

	return definition.Handler(context.Background(), args)
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

func TestReadWholeFileAndRange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.txt")

	if err := os.WriteFile(path, []byte("one\ntwo\nthree\nfour"), 0o644); err != nil {
		t.Fatal(err)
	}

	tools := DefaultTools()

	whole, err := call(t, tools, "read", map[string]any{"path": path})
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	if whole != "one\ntwo\nthree\nfour" {
		t.Errorf("read whole = %q", whole)
	}

	// line numbers are 1-indexed and the end is inclusive
	part, err := call(t, tools, "read", map[string]any{
		"path": path, "startLine": float64(2), "endLine": float64(3),
	})
	if err != nil {
		t.Fatalf("read range: %v", err)
	}

	if part != "two\nthree" {
		t.Errorf("read range = %q, want \"two\\nthree\"", part)
	}
}

func TestReadRangeOutsideTheFileIsEmptyNotAnError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "short.txt")

	if err := os.WriteFile(path, []byte("only one line"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := call(t, DefaultTools(), "read", map[string]any{
		"path": path, "startLine": float64(50),
	})
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	if got != "" {
		t.Errorf("read past the end = %q, want empty", got)
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

	got, err := call(t, DefaultTools(), "read", map[string]any{"path": path})
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

// The edge cases in the file tools are where an autonomous run does damage: an
// out-of-range line number that silently rewrites the wrong part of a file is
// worse than an error, because nobody is watching.

func TestReadHandlesPartialRanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file.txt")

	if err := os.WriteFile(path, []byte("one\ntwo\nthree\nfour"), 0o644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		args map[string]any
		want string
	}{
		{
			name: "only a start",
			args: map[string]any{"path": path, "startLine": float64(3)},
			want: "three\nfour",
		},
		{
			name: "only an end",
			args: map[string]any{"path": path, "endLine": float64(2)},
			want: "one\ntwo",
		},
		{
			name: "a start below the first line",
			args: map[string]any{"path": path, "startLine": float64(-5), "endLine": float64(1)},
			want: "one",
		},
		{
			name: "an end past the last line",
			args: map[string]any{"path": path, "startLine": float64(4), "endLine": float64(99)},
			want: "four",
		},
		{
			name: "an integer rather than a JSON number",
			args: map[string]any{"path": path, "startLine": 2, "endLine": 2},
			want: "two",
		},
		{
			name: "a range that is the wrong way round",
			args: map[string]any{"path": path, "startLine": float64(3), "endLine": float64(1)},
			want: "",
		},
		{
			name: "a start past the end of the file",
			args: map[string]any{"path": path, "startLine": float64(99)},
			want: "",
		},
		{
			name: "line numbers that are not numbers at all",
			args: map[string]any{"path": path, "startLine": "2", "endLine": "3"},
			want: "one\ntwo\nthree\nfour",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := readHandler(context.Background(), test.args)
			if err != nil {
				t.Fatalf("readHandler: %v", err)
			}

			if got != test.want {
				t.Errorf("got %q, want %q", got, test.want)
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
