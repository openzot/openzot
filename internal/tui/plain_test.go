package tui

import (
	"context"
	"fmt"
	"github.com/chatbotkit/zot/agent"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestPlainDiffNoANSI(t *testing.T) {
	out := plainDiff("edit", map[string]interface{}{
		"path":      "x.go",
		"oldString": "a := 1\n",
		"newString": "a := 2\n",
	})
	if out == "" {
		t.Fatal("expected plain diff output")
	}
	if strings.Contains(out, "\x1b[") {
		t.Error("plain diff must not contain ANSI escape codes")
	}
	for _, want := range []string{"--- x.go", "- a := 1", "+ a := 2"} {
		if !strings.Contains(out, want) {
			t.Errorf("plain diff missing %q in:\n%s", want, out)
		}
	}
}

func TestIsInteractiveUnderTest(t *testing.T) {
	// `go test` pipes stdout, so the detector should report non-interactive and
	// zot would pick plain mode.
	if isInteractive() {
		t.Skip("stdout is a terminal in this environment")
	}
}

// The plain renderer is the path CI and pipes take, so it is exercised end to
// end against a stub provider rather than a piece at a time - what matters is
// the transcript a human or a log reader ends up with.

// plainServer scripts a provider conversation.
func plainServer(t *testing.T, turns ...[]string) *agent.Client {
	t.Helper()

	turn := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")

		index := turn
		if index >= len(turns) {
			index = len(turns) - 1
		}

		turn++

		for _, frame := range turns[index] {
			fmt.Fprintf(w, "data: %s\n\n", frame)
		}

		fmt.Fprint(w, "data: [DONE]\n\n")
	}))

	t.Cleanup(server.Close)

	client, err := agent.NewClient(agent.ClientOptions{
		Provider: "custom",
		Model:    "test-model",
		APIKey:   "k",
		BaseURL:  server.URL,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	return client
}

func plainToken(text string) string {
	return fmt.Sprintf(`{"choices":[{"delta":{"content":%q}}]}`, text)
}

func plainStop() string {
	return `{"choices":[{"delta":{},"finish_reason":"stop"}]}`
}

func plainCall(id, name, arguments string) string {
	return fmt.Sprintf(
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":%q,"type":"function","function":{"name":%q,"arguments":%q}}]},"finish_reason":"tool_calls"}]}`,
		id, name, arguments,
	)
}

func plainSuccess(summary string) string {
	return plainCall("done", "_success", fmt.Sprintf(`{"summary":%q}`, summary))
}

// capture runs a function with stdout redirected, returning what it printed.
func capture(t *testing.T, fn func() error) (string, error) {
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

func TestRunPlainTranscript(t *testing.T) {
	client := plainServer(t,
		[]string{plainCall("c1", "shell", `{"command":"go test ./..."}`)},
		[]string{plainToken("all green"), plainStop()},
		[]string{plainSuccess("tests pass")},
	)

	meta := Meta{Task: "run the tests", Model: "test-model", Backend: "openai", Workdir: "/tmp/work"}

	options := agent.ExecuteWithToolsOptions{
		Tools: agent.Tools{
			"shell": {
				Description: "run a command",
				Parameters:  agent.FunctionParameters{"type": "object"},
				Handler: func(context.Context, map[string]any) (any, error) {
					return "ok\n", nil
				},
			},
		},
	}

	output, err := capture(t, func() error {
		return runPlain(context.Background(), client, meta, options)
	})
	if err != nil {
		t.Fatalf("runPlain: %v", err)
	}

	for _, want := range []string{
		"zot: run the tests",
		"backend openai",
		"model test-model",
		"iteration 1",
		// the command itself, not a key=value dump
		"go test ./...",
		"all green",
		"done: tests pass",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("transcript is missing %q:\n%s", want, output)
		}
	}

	// escape codes would make a CI log unreadable
	if strings.Contains(output, "\x1b[") {
		t.Errorf("plain output must carry no escape codes:\n%q", output)
	}
}

// A failed run has to return an error, or the shell sees success.
func TestRunPlainReturnsAnErrorOnFailure(t *testing.T) {
	client := plainServer(t, []string{plainToken("I give up."), plainStop()})

	meta := Meta{Task: "impossible", Model: "m", Backend: "b", Workdir: "/w"}

	output, err := capture(t, func() error {
		return runPlain(context.Background(), client, meta,
			agent.ExecuteWithToolsOptions{MaxSettles: 1})
	})

	if err == nil {
		t.Fatal("an unsettled run must return an error")
	}

	if !strings.Contains(err.Error(), "agent exited") {
		t.Errorf("error = %v, want an exit error", err)
	}

	if !strings.Contains(output, "failed") {
		t.Errorf("the transcript must say it failed:\n%s", output)
	}
}

func TestRunPlainReportsToolErrors(t *testing.T) {
	client := plainServer(t,
		[]string{plainCall("c1", "boom", `{}`)},
		[]string{plainSuccess("recovered")},
	)

	options := agent.ExecuteWithToolsOptions{
		Tools: agent.Tools{
			"boom": {
				Description: "fails",
				Parameters:  agent.FunctionParameters{"type": "object"},
				Handler: func(context.Context, map[string]any) (any, error) {
					return nil, fmt.Errorf("disk on fire")
				},
			},
		},
	}

	output, err := capture(t, func() error {
		return runPlain(context.Background(), client,
			Meta{Task: "t", Model: "m", Backend: "b", Workdir: "/w"}, options)
	})
	if err != nil {
		t.Fatalf("runPlain: %v", err)
	}

	if !strings.Contains(output, "disk on fire") {
		t.Errorf("a tool failure must be visible:\n%s", output)
	}
}

// A provider that fails outright is an error, not a silent empty transcript.
func TestRunPlainSurfacesProviderFailures(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)

		fmt.Fprint(w, `{"error":{"message":"invalid api key"}}`)
	}))

	defer server.Close()

	client, err := agent.NewClient(agent.ClientOptions{
		Provider: "custom",
		Model:    "m",
		APIKey:   "k",
		BaseURL:  server.URL,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	_, runErr := capture(t, func() error {
		return runPlain(context.Background(), client,
			Meta{Task: "t", Model: "m", Backend: "b", Workdir: "/w"},
			agent.ExecuteWithToolsOptions{})
	})

	if runErr == nil {
		t.Fatal("a credential failure must reach the caller")
	}
}

func TestRunPlainShowsDiffsWhenAsked(t *testing.T) {
	client := plainServer(t,
		[]string{plainCall("c1", "write", `{"path":"new.txt","content":"hello\nworld"}`)},
		[]string{plainSuccess("written")},
	)

	options := agent.ExecuteWithToolsOptions{
		Tools: agent.Tools{
			"write": {
				Description: "write",
				Parameters:  agent.FunctionParameters{"type": "object"},
				Handler: func(context.Context, map[string]any) (any, error) {
					return "wrote 11 bytes", nil
				},
			},
		},
	}

	withDiff, err := capture(t, func() error {
		return runPlain(context.Background(), client,
			Meta{Task: "t", Model: "m", Backend: "b", Workdir: "/w", ShowDiff: true}, options)
	})
	if err != nil {
		t.Fatalf("runPlain: %v", err)
	}

	if !strings.Contains(withDiff, "hello") {
		t.Errorf("the diff should show the content being written:\n%s", withDiff)
	}
}

// Run dispatches to the plain renderer when there is no usable terminal, or when
// it is asked to - starting an alt-screen program without a TTY would garble the
// output or fail outright.
func TestRunUsesThePlainPathWithoutATerminal(t *testing.T) {
	client := plainServer(t, []string{plainSuccess("finished")})

	meta := Meta{Task: "t", Model: "m", Backend: "b", Workdir: "/w", Plain: true}

	output, err := capture(t, func() error {
		return Run(context.Background(), client, meta, agent.ExecuteWithToolsOptions{})
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !strings.Contains(output, "finished") {
		t.Errorf("Run should have produced a plain transcript:\n%s", output)
	}

	// under `go test` stdout is not a terminal, so even without Plain the
	// dispatch must land on the same path rather than trying an alt screen
	output, err = capture(t, func() error {
		return Run(context.Background(), plainServer(t, []string{plainSuccess("again")}),
			Meta{Task: "t", Model: "m", Backend: "b", Workdir: "/w"},
			agent.ExecuteWithToolsOptions{})
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !strings.Contains(output, "again") {
		t.Errorf("a non-TTY Run must fall back to plain:\n%s", output)
	}
}

func TestRunPropagatesFailures(t *testing.T) {
	client := plainServer(t, []string{plainToken("nope"), plainStop()})

	_, err := capture(t, func() error {
		return Run(context.Background(), client,
			Meta{Task: "t", Model: "m", Backend: "b", Workdir: "/w", Plain: true},
			agent.ExecuteWithToolsOptions{MaxSettles: 1})
	})

	if err == nil {
		t.Error("a failed run must reach the caller through Run")
	}
}
