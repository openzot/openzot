package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	acpsdk "github.com/coder/acp-go-sdk"

	"github.com/chatbotkit/go-sdk/agent"
)

// maxToolContent caps how much tool output is echoed to the client. The agent
// still sees the full result; this only bounds what crosses the wire for
// display.
const maxToolContent = 4000

// stream translates one agent run into ACP session updates. The SDK's event
// stream and ACP's session updates line up closely: tokens become agent message
// chunks, tool calls become tool calls, and the plan/progress system tools drive
// ACP's first-class plan.
type stream struct {
	conn *acpsdk.AgentSideConnection
	sid  acpsdk.SessionId

	maxIterations int
	iterations    int

	// open tracks in-flight tool calls by tool name. The SDK reports a call's
	// end by name only, so calls of the same tool are paired in the order they
	// started.
	seq  int
	open map[string][]openCall

	plan     []acpsdk.PlanEntry
	messages []agent.Message
	exit     *agent.AgentExitEvent
}

// openCall is a tool call that has started but not yet reported a result. The
// arguments are kept so the end update can render a diff.
type openCall struct {
	id   acpsdk.ToolCallId
	args map[string]any
}

func newStream(conn *acpsdk.AgentSideConnection, sid acpsdk.SessionId, maxIterations int) *stream {
	return &stream{
		conn:          conn,
		sid:           sid,
		maxIterations: maxIterations,
		open:          map[string][]openCall{},
	}
}

// handle relays one agent event to the client.
func (s *stream) handle(ctx context.Context, ev agent.AgentEvent) error {
	switch e := ev.(type) {
	case agent.IterationEvent:
		s.iterations = e.Iteration
		return nil

	case agent.TokenAgentEvent:
		if e.Token == "" {
			return nil
		}
		return s.update(ctx, acpsdk.UpdateAgentMessageText(e.Token))

	case agent.MessageAgentEvent:
		// Mirror the SDK's own history bookkeeping so the next turn resumes
		// from the same conversation the agent just had.
		s.messages = append(s.messages, agent.Message{Type: e.Type, Text: e.Text, Meta: e.Meta})
		return nil

	case agent.ToolCallStartEvent:
		return s.toolStart(ctx, e)

	case agent.ToolCallEndEvent:
		return s.toolEnd(ctx, e)

	case agent.ToolCallErrorEvent:
		return s.toolError(ctx, e)

	case agent.AgentExitEvent:
		s.exit = &e
		if msg := strings.TrimSpace(e.Message); msg != "" {
			return s.update(ctx, acpsdk.UpdateAgentMessageText(msg))
		}
		return nil
	}

	return nil
}

// stopReason reports how the turn ended, once the event stream is drained.
func (s *stream) stopReason() acpsdk.StopReason {
	// The agent loop stops without an exit call when it runs out of iterations.
	if s.exit != nil && s.exit.Code != 0 && s.maxIterations > 0 && s.iterations >= s.maxIterations {
		return acpsdk.StopReasonMaxTurnRequests
	}
	return acpsdk.StopReasonEndTurn
}

func (s *stream) update(ctx context.Context, u acpsdk.SessionUpdate) error {
	return s.conn.SessionUpdate(ctx, acpsdk.SessionNotification{SessionId: s.sid, Update: u})
}

func (s *stream) toolStart(ctx context.Context, e agent.ToolCallStartEvent) error {
	switch e.Name {
	case "plan":
		s.plan = planEntries(e.Args["steps"])
		if len(s.plan) == 0 {
			return nil
		}
		return s.update(ctx, acpsdk.UpdatePlan(s.plan...))

	case "progress":
		if !s.applyProgress(e.Args) {
			return nil
		}
		return s.update(ctx, acpsdk.UpdatePlan(s.plan...))

	case "exit", "abort":
		// How the turn ends, not something the agent did. AgentExitEvent
		// carries the outcome.
		return nil
	}

	s.seq++
	call := openCall{id: acpsdk.ToolCallId(fmt.Sprintf("%s_%d", e.Name, s.seq)), args: e.Args}
	s.open[e.Name] = append(s.open[e.Name], call)

	opts := []acpsdk.ToolCallStartOpt{
		acpsdk.WithStartKind(toolKind(e.Name)),
		acpsdk.WithStartStatus(acpsdk.ToolCallStatusInProgress),
		acpsdk.WithStartRawInput(boundedRawData(e.Args)),
	}
	if path := str(e.Args, "path"); path != "" {
		opts = append(opts, acpsdk.WithStartLocations([]acpsdk.ToolCallLocation{{Path: path}}))
	}

	return s.update(ctx, acpsdk.StartToolCall(call.id, toolTitle(e.Name, e.Args), opts...))
}

func (s *stream) toolEnd(ctx context.Context, e agent.ToolCallEndEvent) error {
	call, ok := s.take(e.Name)
	if !ok {
		return nil
	}

	result, _ := e.Result.(map[string]any)

	// The default tools report failure in-band rather than as an error, so a
	// successful call can still describe a failed operation.
	if success, present := result["success"].(bool); present && !success {
		return s.update(ctx, acpsdk.UpdateToolCall(call.id,
			acpsdk.WithUpdateStatus(acpsdk.ToolCallStatusFailed),
			acpsdk.WithUpdateContent(textContent(str(result, "error"))),
			acpsdk.WithUpdateRawOutput(boundedRawData(e.Result)),
		))
	}

	opts := []acpsdk.ToolCallUpdateOpt{
		acpsdk.WithUpdateStatus(acpsdk.ToolCallStatusCompleted),
		acpsdk.WithUpdateRawOutput(boundedRawData(e.Result)),
	}
	if content := toolContent(e.Name, call.args, result); len(content) > 0 {
		opts = append(opts, acpsdk.WithUpdateContent(content))
	}

	return s.update(ctx, acpsdk.UpdateToolCall(call.id, opts...))
}

func (s *stream) toolError(ctx context.Context, e agent.ToolCallErrorEvent) error {
	call, ok := s.take(e.Name)
	if !ok {
		return nil
	}
	return s.update(ctx, acpsdk.UpdateToolCall(call.id,
		acpsdk.WithUpdateStatus(acpsdk.ToolCallStatusFailed),
		acpsdk.WithUpdateContent(textContent(e.Error)),
	))
}

// take removes and returns the oldest in-flight call for a tool.
func (s *stream) take(name string) (openCall, bool) {
	calls := s.open[name]
	if len(calls) == 0 {
		return openCall{}, false
	}
	call := calls[0]
	if len(calls) == 1 {
		delete(s.open, name)
	} else {
		s.open[name] = calls[1:]
	}
	return call, true
}

// applyProgress folds a progress report into the current plan, reporting whether
// anything changed. Progress without a plan has nothing to update.
func (s *stream) applyProgress(args map[string]any) bool {
	if len(s.plan) == 0 {
		return false
	}

	done := map[string]bool{}
	for _, step := range slice(args["completed"]) {
		if text, ok := step.(string); ok {
			done[text] = true
		}
	}
	current := str(args, "current")

	changed := false
	for i, entry := range s.plan {
		status := acpsdk.PlanEntryStatusPending
		switch {
		case done[entry.Content]:
			status = acpsdk.PlanEntryStatusCompleted
		case entry.Content == current:
			status = acpsdk.PlanEntryStatusInProgress
		}
		if s.plan[i].Status != status {
			s.plan[i].Status = status
			changed = true
		}
	}
	return changed
}

// planEntries converts the plan tool's steps into ACP plan entries.
func planEntries(steps any) []acpsdk.PlanEntry {
	var entries []acpsdk.PlanEntry
	for _, step := range slice(steps) {
		text, ok := step.(string)
		if !ok || strings.TrimSpace(text) == "" {
			continue
		}
		entries = append(entries, acpsdk.PlanEntry{
			Content:  text,
			Priority: acpsdk.PlanEntryPriorityMedium,
			Status:   acpsdk.PlanEntryStatusPending,
		})
	}
	return entries
}

// toolKind maps zot's tools onto the categories clients use to pick icons.
func toolKind(name string) acpsdk.ToolKind {
	switch name {
	case "read":
		return acpsdk.ToolKindRead
	case "write", "edit":
		return acpsdk.ToolKindEdit
	case "exec":
		return acpsdk.ToolKindExecute
	default:
		return acpsdk.ToolKindOther
	}
}

// toolTitle is the one-line description a client shows for a call.
func toolTitle(name string, args map[string]any) string {
	switch name {
	case "read", "write", "edit":
		if path := str(args, "path"); path != "" {
			return name + " " + path
		}
	case "exec":
		if command := str(args, "command"); command != "" {
			return truncate(command, 120)
		}
	}
	return name
}

// toolContent renders a completed call's result for display. Edits become
// diffs; everything else becomes text.
func toolContent(name string, args, result map[string]any) []acpsdk.ToolCallContent {
	switch name {
	case "edit":
		path := str(args, "path")
		if path == "" {
			return nil
		}
		return []acpsdk.ToolCallContent{
			acpsdk.ToolDiffContent(
				path,
				truncate(str(args, "newString"), maxToolContent/2),
				truncate(str(args, "oldString"), maxToolContent/2),
			),
		}

	case "write":
		path := str(args, "path")
		if path == "" {
			return nil
		}
		return []acpsdk.ToolCallContent{
			acpsdk.ToolDiffContent(path, truncate(str(args, "content"), maxToolContent)),
		}

	case "read":
		return textContent(str(result, "content"))

	case "exec":
		out := strings.TrimRight(str(result, "stdout"), "\n")
		if errOut := strings.TrimRight(str(result, "stderr"), "\n"); errOut != "" {
			if out != "" {
				out += "\n"
			}
			out += errOut
		}
		return textContent(out)
	}

	return textContent(str(result, "message"))
}

func textContent(text string) []acpsdk.ToolCallContent {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	return []acpsdk.ToolCallContent{acpsdk.ToolContent(acpsdk.TextBlock(truncate(text, maxToolContent)))}
}

func boundedRawData(value any) any {
	data, err := json.Marshal(value)
	if err != nil {
		return map[string]any{"unavailable": true}
	}
	if len(data) <= maxToolContent {
		return value
	}
	return map[string]any{
		"truncated":     true,
		"originalBytes": len(data),
	}
}

func str(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

func slice(v any) []any {
	s, _ := v.([]any)
	return s
}

// truncate caps a string at max bytes without splitting a rune - tool output is
// arbitrary file content, and half a rune would reach the client as a
// replacement character.
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "\n… truncated"
}
