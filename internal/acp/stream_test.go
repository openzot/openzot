package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"unicode/utf8"

	acpsdk "github.com/coder/acp-go-sdk"

	"github.com/chatbotkit/go-sdk/agent"
)

// sessionUpdate is the shape of a session/update notification, decoded loosely
// so the tests can assert on the wire format rather than the SDK's types.
type sessionUpdate struct {
	SessionID string         `json:"sessionId"`
	Update    map[string]any `json:"update"`
}

// recorder wires a stream to a connection whose notifications are captured.
type recorder struct {
	stream  *stream
	updates func() []sessionUpdate
}

func newRecorder(t *testing.T, maxIterations int) *recorder {
	t.Helper()

	captured, agentOut := io.Pipe()
	// Nothing is ever sent to the agent side, so a bare server is enough to
	// satisfy the connection's handler.
	conn := acpsdk.NewAgentSideConnection(&server{}, agentOut, strings.NewReader(""))

	lines := make(chan sessionUpdate, 64)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(captured)
		scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
		for scanner.Scan() {
			var msg struct {
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
			}
			if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
				continue
			}
			if msg.Method != "session/update" {
				continue
			}
			var params sessionUpdate
			if err := json.Unmarshal(msg.Params, &params); err != nil {
				continue
			}
			lines <- params
		}
	}()

	t.Cleanup(func() { _ = agentOut.Close() })

	return &recorder{
		stream: newStream(conn, "zot_test", maxIterations),
		updates: func() []sessionUpdate {
			// Closing the writer ends the scanner, so everything already
			// written is drained before the channel closes.
			_ = agentOut.Close()
			var out []sessionUpdate
			for u := range lines {
				out = append(out, u)
			}
			return out
		},
	}
}

func (r *recorder) handle(t *testing.T, events ...agent.AgentEvent) {
	t.Helper()
	for _, ev := range events {
		if err := r.stream.handle(context.Background(), ev); err != nil {
			t.Fatalf("handle(%T): %v", ev, err)
		}
	}
}

func TestStreamTokensBecomeMessageChunks(t *testing.T) {
	r := newRecorder(t, 10)

	r.handle(t,
		agent.TokenAgentEvent{Token: "Look"},
		agent.TokenAgentEvent{Token: ""}, // empty tokens are not worth a notification
		agent.TokenAgentEvent{Token: "ing"},
	)

	updates := r.updates()
	if len(updates) != 2 {
		t.Fatalf("got %d updates, want 2: %+v", len(updates), updates)
	}
	for i, want := range []string{"Look", "ing"} {
		u := updates[i].Update
		if u["sessionUpdate"] != "agent_message_chunk" {
			t.Errorf("update %d kind = %v, want agent_message_chunk", i, u["sessionUpdate"])
		}
		content, _ := u["content"].(map[string]any)
		if content["text"] != want {
			t.Errorf("update %d text = %v, want %q", i, content["text"], want)
		}
	}
}

func TestStreamToolCallLifecycle(t *testing.T) {
	r := newRecorder(t, 10)

	r.handle(t,
		agent.ToolCallStartEvent{Name: "read", Args: map[string]any{"path": "/repo/main.go"}},
		agent.ToolCallEndEvent{Name: "read", Result: map[string]any{
			"success": true, "content": "package main", "totalLines": 1,
		}},
	)

	updates := r.updates()
	if len(updates) != 2 {
		t.Fatalf("got %d updates, want 2: %+v", len(updates), updates)
	}

	start := updates[0].Update
	if start["sessionUpdate"] != "tool_call" {
		t.Errorf("start kind = %v, want tool_call", start["sessionUpdate"])
	}
	if start["kind"] != "read" {
		t.Errorf("start tool kind = %v, want read", start["kind"])
	}
	if start["status"] != "in_progress" {
		t.Errorf("start status = %v, want in_progress", start["status"])
	}
	if start["title"] != "read /repo/main.go" {
		t.Errorf("start title = %v, want %q", start["title"], "read /repo/main.go")
	}
	id, _ := start["toolCallId"].(string)
	if id == "" {
		t.Fatal("start carried no tool call id")
	}

	end := updates[1].Update
	if end["sessionUpdate"] != "tool_call_update" {
		t.Errorf("end kind = %v, want tool_call_update", end["sessionUpdate"])
	}
	if end["toolCallId"] != id {
		t.Errorf("end id = %v, want %q", end["toolCallId"], id)
	}
	if end["status"] != "completed" {
		t.Errorf("end status = %v, want completed", end["status"])
	}
}

func TestStreamBoundsLargeToolDataOnTheWire(t *testing.T) {
	r := newRecorder(t, 10)
	large := strings.Repeat("sensitive-output-", 1000)

	r.handle(t,
		agent.ToolCallStartEvent{Name: "write", Args: map[string]any{
			"path":    "/repo/generated.txt",
			"content": large,
		}},
		agent.ToolCallEndEvent{Name: "write", Result: map[string]any{
			"success": true,
			"message": large,
		}},
	)

	updates := r.updates()
	if len(updates) != 2 {
		t.Fatalf("got %d updates, want 2: %+v", len(updates), updates)
	}

	for i, update := range updates {
		wire := marshal(t, update)
		if strings.Contains(wire, large) {
			t.Errorf("update %d contains the complete large payload", i)
		}
		if len(wire) > maxToolContent*2 {
			t.Errorf("update %d is %d bytes, want a bounded protocol message", i, len(wire))
		}
	}

	raw, _ := updates[1].Update["rawOutput"].(map[string]any)
	if raw["truncated"] != true {
		t.Errorf("large raw output = %v, want a truncated summary", raw)
	}
}

func TestStreamToolFailureIsReported(t *testing.T) {
	tests := []struct {
		name  string
		end   agent.AgentEvent
		wants string
	}{
		{
			name:  "in-band failure",
			end:   agent.ToolCallEndEvent{Name: "read", Result: map[string]any{"success": false, "error": "no such file"}},
			wants: "no such file",
		},
		{
			name:  "tool error",
			end:   agent.ToolCallErrorEvent{Name: "read", Error: "handler exploded"},
			wants: "handler exploded",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRecorder(t, 10)
			r.handle(t, agent.ToolCallStartEvent{Name: "read", Args: map[string]any{"path": "/repo/gone.go"}}, tt.end)

			updates := r.updates()
			if len(updates) != 2 {
				t.Fatalf("got %d updates, want 2: %+v", len(updates), updates)
			}
			end := updates[1].Update
			if end["status"] != "failed" {
				t.Errorf("status = %v, want failed", end["status"])
			}
			if !strings.Contains(marshal(t, end), tt.wants) {
				t.Errorf("update does not mention %q: %s", tt.wants, marshal(t, end))
			}
		})
	}
}

func TestStreamPairsConcurrentCallsInOrder(t *testing.T) {
	r := newRecorder(t, 10)

	// The SDK reports a call's end by tool name only, so two overlapping execs
	// have to be paired first-started-first-ended.
	r.handle(t,
		agent.ToolCallStartEvent{Name: "exec", Args: map[string]any{"command": "go build ./..."}},
		agent.ToolCallStartEvent{Name: "exec", Args: map[string]any{"command": "go test ./..."}},
		agent.ToolCallEndEvent{Name: "exec", Result: map[string]any{"success": true, "stdout": "build ok"}},
		agent.ToolCallEndEvent{Name: "exec", Result: map[string]any{"success": true, "stdout": "test ok"}},
	)

	updates := r.updates()
	if len(updates) != 4 {
		t.Fatalf("got %d updates, want 4: %+v", len(updates), updates)
	}

	first, _ := updates[0].Update["toolCallId"].(string)
	second, _ := updates[1].Update["toolCallId"].(string)
	if first == second {
		t.Fatal("overlapping calls reused a tool call id")
	}
	if got := updates[2].Update["toolCallId"]; got != first {
		t.Errorf("first end paired with %v, want %q", got, first)
	}
	if got := updates[3].Update["toolCallId"]; got != second {
		t.Errorf("second end paired with %v, want %q", got, second)
	}
}

func TestStreamOrphanEndIsIgnored(t *testing.T) {
	r := newRecorder(t, 10)
	r.handle(t, agent.ToolCallEndEvent{Name: "read", Result: map[string]any{"success": true}})

	if updates := r.updates(); len(updates) != 0 {
		t.Errorf("got %d updates for an unpaired end, want 0: %+v", len(updates), updates)
	}
}

func TestStreamPlanAndProgress(t *testing.T) {
	r := newRecorder(t, 10)

	r.handle(t,
		agent.ToolCallStartEvent{Name: "plan", Args: map[string]any{
			"steps": []any{"read the parser", "fix the bug", "add a test"},
		}},
		agent.ToolCallEndEvent{Name: "plan", Result: map[string]any{"success": true}},
		agent.ToolCallStartEvent{Name: "progress", Args: map[string]any{
			"completed": []any{"read the parser"},
			"current":   "fix the bug",
		}},
		agent.ToolCallEndEvent{Name: "progress", Result: map[string]any{"success": true}},
	)

	updates := r.updates()
	if len(updates) != 2 {
		t.Fatalf("got %d updates, want 2 plan updates: %+v", len(updates), updates)
	}

	for i, u := range updates {
		if u.Update["sessionUpdate"] != "plan" {
			t.Errorf("update %d kind = %v, want plan", i, u.Update["sessionUpdate"])
		}
	}

	entries, _ := updates[1].Update["entries"].([]any)
	if len(entries) != 3 {
		t.Fatalf("got %d plan entries, want 3", len(entries))
	}
	wantStatus := []string{"completed", "in_progress", "pending"}
	for i, want := range wantStatus {
		entry, _ := entries[i].(map[string]any)
		if entry["status"] != want {
			t.Errorf("entry %d status = %v, want %s", i, entry["status"], want)
		}
	}
}

func TestStreamProgressWithoutPlanIsQuiet(t *testing.T) {
	r := newRecorder(t, 10)
	r.handle(t, agent.ToolCallStartEvent{Name: "progress", Args: map[string]any{"current": "thinking"}})

	if updates := r.updates(); len(updates) != 0 {
		t.Errorf("got %d updates, want 0: %+v", len(updates), updates)
	}
}

func TestStreamExitIsNotAToolCall(t *testing.T) {
	r := newRecorder(t, 10)

	r.handle(t,
		agent.ToolCallStartEvent{Name: "exit", Args: map[string]any{"code": float64(0), "message": "all done"}},
		agent.ToolCallEndEvent{Name: "exit", Result: map[string]any{"success": true}},
		agent.AgentExitEvent{Code: 0, Message: "all done"},
	)

	updates := r.updates()
	if len(updates) != 1 {
		t.Fatalf("got %d updates, want 1: %+v", len(updates), updates)
	}
	if updates[0].Update["sessionUpdate"] != "agent_message_chunk" {
		t.Errorf("kind = %v, want agent_message_chunk", updates[0].Update["sessionUpdate"])
	}
}

func TestStreamRecordsHistory(t *testing.T) {
	r := newRecorder(t, 10)

	r.handle(t,
		agent.MessageAgentEvent{Type: "assistant", Text: "on it"},
		agent.TokenAgentEvent{Token: "on it"},
	)
	r.updates()

	if got := r.stream.messages; len(got) != 1 || got[0].Text != "on it" || got[0].Type != "assistant" {
		t.Errorf("history = %+v, want one assistant message", got)
	}
}

func TestStreamStopReason(t *testing.T) {
	tests := []struct {
		name          string
		maxIterations int
		iterations    int
		exit          agent.AgentExitEvent
		want          acpsdk.StopReason
	}{
		{
			name:          "clean exit ends the turn",
			maxIterations: 10,
			iterations:    3,
			exit:          agent.AgentExitEvent{Code: 0, Message: "done"},
			want:          acpsdk.StopReasonEndTurn,
		},
		{
			name:          "a failed exit still ends the turn",
			maxIterations: 10,
			iterations:    3,
			exit:          agent.AgentExitEvent{Code: 1, Message: "could not build"},
			want:          acpsdk.StopReasonEndTurn,
		},
		{
			name:          "running out of iterations is reported as such",
			maxIterations: 3,
			iterations:    3,
			exit:          agent.AgentExitEvent{Code: 1, Message: "Task did not complete within 3 iterations"},
			want:          acpsdk.StopReasonMaxTurnRequests,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRecorder(t, tt.maxIterations)
			r.handle(t, agent.IterationEvent{Iteration: tt.iterations}, tt.exit)
			r.updates()

			if got := r.stream.stopReason(); got != tt.want {
				t.Errorf("stopReason() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestToolContentRendersEdits(t *testing.T) {
	content := toolContent("edit",
		map[string]any{"path": "main.go", "oldString": "a := 1", "newString": "a := 2"},
		map[string]any{"success": true},
	)
	if len(content) != 1 || content[0].Diff == nil {
		t.Fatalf("edit content = %+v, want a diff", content)
	}
	diff := content[0].Diff
	if diff.Path != "main.go" || diff.NewText != "a := 2" || diff.OldText == nil || *diff.OldText != "a := 1" {
		t.Errorf("diff = %+v, want main.go a := 1 -> a := 2", diff)
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("short", 10); got != "short" {
		t.Errorf("truncate() = %q, want it unchanged", got)
	}

	got := truncate(strings.Repeat("x", 20), 10)
	if !strings.HasPrefix(got, strings.Repeat("x", 10)) || !strings.Contains(got, "truncated") {
		t.Errorf("truncate() = %q, want a truncated marker", got)
	}

	// Cutting mid-rune would reach the client as a replacement character.
	if got := truncate(strings.Repeat("é", 10), 5); !utf8.ValidString(got) {
		t.Errorf("truncate() = %q, want valid UTF-8", got)
	}
}

func marshal(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}
