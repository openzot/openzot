package agent

import "github.com/chatbotkit/zot/internal/loop"

// AgentEvent is one thing that happened during a run.
//
// The concrete types mirror the SDK's, so a consumer's type switch keeps
// compiling across the migration.
type AgentEvent interface {
	agentEventType() string
}

// TokenAgentEvent is a fragment of the answer as it streams.
type TokenAgentEvent struct {
	Token string
}

func (TokenAgentEvent) agentEventType() string { return "token" }

// ReasoningTokenAgentEvent is a fragment of the model's reasoning channel.
//
// Not every model emits one, and it is the model's scratchpad rather than its
// answer - render it differently, or not at all.
type ReasoningTokenAgentEvent struct {
	Token string
}

func (ReasoningTokenAgentEvent) agentEventType() string { return "reasoningToken" }

// MessageAgentEvent is a completed message.
type MessageAgentEvent struct {
	// Type is what kind of message this is.
	Type MessageType

	// Text is the message body.
	Text string

	// Activity is the tool call this message carries, where it carries one.
	Activity *Activity
}

func (MessageAgentEvent) agentEventType() string { return "message" }

// IterationEvent marks the start of an agentic round.
type IterationEvent struct {
	Iteration int
}

func (IterationEvent) agentEventType() string { return "iteration" }

// ToolCallStartEvent is emitted before a tool runs.
type ToolCallStartEvent struct {
	Name string

	// Args are the decoded arguments. Nil when the model emitted JSON that does
	// not parse, which surfaces as a ToolCallErrorEvent rather than aborting the
	// run - so a consumer must tolerate a nil map here.
	Args map[string]any
}

func (ToolCallStartEvent) agentEventType() string { return "toolCallStart" }

// ToolCallEndEvent is emitted after a tool succeeds.
type ToolCallEndEvent struct {
	Name string

	// Result is whatever the handler returned.
	Result any
}

func (ToolCallEndEvent) agentEventType() string { return "toolCallEnd" }

// ToolCallErrorEvent is emitted when a tool fails.
//
// The run continues: the failure is fed back to the model, which is usually
// capable of recovering from it. Only the caller decides whether a failed tool
// is fatal.
type ToolCallErrorEvent struct {
	Name  string
	Error string
}

func (ToolCallErrorEvent) agentEventType() string { return "toolCallError" }

// RetryEvent is emitted when a transient provider failure is being retried.
type RetryEvent struct {
	Error string
}

func (RetryEvent) agentEventType() string { return "retry" }

// RunawayEvent is emitted when the streaming guard cut a degenerate turn short.
//
// Phrase is what the model got stuck repeating, which is worth surfacing: it
// usually explains the loop better than any diagnostic.
type RunawayEvent struct {
	Phrase string
}

func (RunawayEvent) agentEventType() string { return "runaway" }

// ResultAgentEvent carries the final answer and why the run ended.
type ResultAgentEvent struct {
	Text string

	// EndReason is the stop reason: "settled", "stop", "iterations", "calls",
	// "cycle", "empty", "unsettled", "aborted", "error".
	EndReason string
}

func (ResultAgentEvent) agentEventType() string { return "result" }

// AgentExitEvent is always the last event.
type AgentExitEvent struct {
	// Code is 0 when the agent reached a conclusion, non-zero when the run was
	// cut short by a budget or a failure.
	Code int

	// Reason is the machine-readable stop reason: "settled", "stop",
	// "iterations", "calls", "cycle", "empty", "unsettled", "aborted", "error".
	Reason string

	// Message explains the ending in prose.
	Message string

	// Messages is the conversation as it ended.
	Messages []Message
}

func (AgentExitEvent) agentEventType() string { return "exit" }

// translate converts an internal event into its public form.
func translate(event loop.Event) (AgentEvent, bool) {
	switch event.Kind {
	case loop.EventToken:
		return TokenAgentEvent{Token: event.Text}, true
	case loop.EventReasoningToken:
		return ReasoningTokenAgentEvent{Token: event.Text}, true
	case loop.EventMessage:
		return MessageAgentEvent{Type: event.MessageType, Text: event.Text, Activity: event.Activity}, true
	case loop.EventIteration:
		return IterationEvent{Iteration: event.Iteration}, true
	case loop.EventToolCallStart:
		return ToolCallStartEvent{Name: event.Tool, Args: event.Args}, true
	case loop.EventToolCallEnd:
		return ToolCallEndEvent{Name: event.Tool, Result: event.Result}, true
	case loop.EventToolCallError:
		return ToolCallErrorEvent{Name: event.Tool, Error: event.Text}, true
	case loop.EventRetry:
		return RetryEvent{Error: event.Text}, true
	case loop.EventRunaway:
		return RunawayEvent{Phrase: event.Text}, true
	default:
		return nil, false
	}
}
