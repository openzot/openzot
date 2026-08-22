// Package agent runs autonomous, tool-using conversations against any
// OpenAI-compatible model provider.
//
// It is the public surface of zot's engine. Everything happens locally: what to
// keep in context, when to summarise, whether the model is stuck, and when the
// work is actually done. The only thing that leaves the machine is the request
// to the model provider the caller configured.
//
// Runs are autonomous by construction. There is no interactive mode and no
// half-measure: an agent either records an outcome or exhausts a budget trying.
// See ExecuteWithTools.
//
//	client, _ := agent.NewClient(agent.ClientOptions{
//	    Provider: "zai",
//	    Model:    "glm-5.2",
//	    APIKey:   os.Getenv("ZAI_API_KEY"),
//	})
//
//	events, errs := agent.ExecuteWithTools(ctx, client, agent.ExecuteWithToolsOptions{
//	    Text:  []string{"summarise the README"},
//	    Tools: agent.DefaultTools(),
//	})
//
// The run ends when the model calls a terminal tool, or when a budget runs out.
// It does not end because the model stopped talking.
package agent

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/openzot/openzot/internal/loop"
	"github.com/openzot/openzot/internal/provider"
)

// Client is a configured connection to a model provider.
type Client struct {
	inner *provider.Client
}

// ClientOptions configures a Client.
type ClientOptions struct {
	// Provider names the model provider: "openai", "anthropic", "groq", "mistral",
	// "ollama" and so on. Defaults to "openai".
	//
	// Anything else that speaks the OpenAI chat-completions API works too - name
	// it "custom" and give it a BaseURL.
	Provider string

	// Model is the provider's own model name.
	Model string

	// APIKey authenticates against the provider. Not required for local
	// providers such as Ollama.
	APIKey string

	// BaseURL overrides the provider's default endpoint, for gateways and
	// self-hosted deployments. Must be https unless it is loopback.
	BaseURL string

	// Headers are merged into every request.
	Headers map[string]string
}

// NewClient validates the options and returns a client.
func NewClient(options ClientOptions) (*Client, error) {
	inner, err := provider.New(provider.Config{
		Provider: options.Provider,
		Model:    options.Model,
		APIKey:   options.APIKey,
		BaseURL:  options.BaseURL,
		Headers:  options.Headers,
	})
	if err != nil {
		return nil, err
	}

	return &Client{inner: inner}, nil
}

// Model returns the resolved model name the client will send.
func (c *Client) Model() string {
	return c.inner.Config().Model
}

// Provider returns the resolved provider identifier.
func (c *Client) Provider() string {
	return c.inner.Config().Provider
}

// BaseURL returns the endpoint the client will call.
func (c *Client) BaseURL() string {
	return c.inner.Config().BaseURL
}

// Providers lists the recognised provider identifiers.
func Providers() []string {
	return provider.Providers()
}

// MessageType identifies what a message is. See the constants below.
type MessageType = loop.MessageType

// The message types a conversation is built from.
const (
	// TypeUser is input from the operator.
	TypeUser = loop.TypeUser

	// TypeBot is the model's answer.
	TypeBot = loop.TypeBot

	// TypeReasoning is the model's scratchpad, where it emits one.
	TypeReasoning = loop.TypeReasoning

	// TypeActivity is one half of a tool-call pair; the call itself is in Meta.
	TypeActivity = loop.TypeActivity

	// TypeInstructions is system context.
	TypeInstructions = loop.TypeInstructions

	// TypeCheckpoint is a compaction summary, preserved verbatim and never
	// re-summarised.
	TypeCheckpoint = loop.TypeCheckpoint
)

// Recorder receives a run's messages and events as they happen.
//
// Both methods may be called from the engine's goroutine, so an implementation
// has to be safe for that. Errors are ignored by design.
type Recorder interface {
	RecordMessage(Message) error
	RecordEvent(kind, tool, text string, iteration int) error
	RecordResult(Summary) error

	// RecordFailure persists a provider failure as it happens, so a run killed
	// mid-retry still leaves the failing exchange behind. Called once per
	// failure; a later one supersedes the record of an earlier.
	RecordFailure(*Failure) error

	// RecordReset discards everything recorded so far, because the engine has
	// rewritten its own history and the earlier record no longer describes the
	// conversation the run is actually holding.
	RecordReset() error
}

// Summary is how a run ended and what it spent.
type Summary struct {
	// Reason is why the run stopped - the same value AgentExitEvent carries.
	Reason string

	// Message explains the reason in prose.
	Message string

	// Error is the underlying failure on an error ending, in the provider's
	// own words. Empty for every other ending.
	Error string

	// Failure is the wire evidence behind Error, when the failure was a
	// provider response: the status, the raw body, and the size of the request
	// that was refused. Error says what the loop concluded; this is what an
	// operator troubleshoots with.
	Failure *Failure

	// Code is the process exit code the reason maps to.
	Code int

	Iterations    int
	Calls         int
	Continuations int
	Cycles        int
	Settles       int
}

// Message is one entry in a conversation.
type Message struct {
	// Type is what kind of message this is.
	Type MessageType `json:"type"`

	// Text is the message body.
	Text string `json:"text"`

	// Activity is the tool call this message carries, on a TypeActivity
	// message. Nil on every other type.
	//
	// Typed rather than a map: zot has exactly one thing to put here, and a
	// `map[string]any` would make every read a type assertion that can fail
	// silently and every key a runtime spelling test.
	Activity *Activity `json:"activity,omitempty"`
}

// Activity is one half of a tool-call pair. See the loop package for detail.
type Activity = loop.Activity

// ActivityKind is which half of a tool call a message carries.
type ActivityKind = loop.ActivityKind

// The halves of a tool call.
const (
	// ActivityRequest is the model asking for a tool to be run.
	ActivityRequest = loop.ActivityRequest

	// ActivityResponse is what the tool returned.
	ActivityResponse = loop.ActivityResponse

	// ActivityTrigger is an instruction to act now, carrying no call.
	ActivityTrigger = loop.ActivityTrigger
)

// FunctionParameters is a JSON Schema describing a tool's arguments.
type FunctionParameters = map[string]any

// ToolHandler executes a tool call and returns its result.
type ToolHandler func(ctx context.Context, args map[string]any) (any, error)

// ToolDefinition describes a tool the model may call.
type ToolDefinition struct {
	Description string
	Parameters  FunctionParameters
	Handler     ToolHandler
}

// Tools is a set of tools keyed by name.
type Tools map[string]ToolDefinition

// The context-window strategies for ExecuteWithToolsOptions.ContextStrategy.
// Empty is treated as StrategyCompact.
const (
	// StrategyCompact summarises older history into a checkpoint as the window
	// fills. The default.
	StrategyCompact = "compact"

	// StrategyTruncate drops the oldest messages to fit the window.
	StrategyTruncate = "truncate"
)

// ExecuteWithToolsOptions configures a run.
type ExecuteWithToolsOptions struct {
	// Instructions is the system prompt.
	Instructions string

	// Text seeds the conversation with user messages.
	Text []string

	// Messages seeds the conversation directly. Combined with Text, which is
	// appended after.
	Messages []Message

	// Tools the model may call.
	Tools Tools

	// Skills are described in the system prompt so the model knows they exist
	// and where to read their instructions.
	Skills []SkillDefinition

	// Recorder, when set, is handed every message and event as the run goes.
	//
	// The engine does not know what a session log is; it hands over what
	// happened and the caller decides whether that lands in a file, a database
	// or nowhere. A recorder that errors is ignored - losing a log entry must
	// never end a run that is otherwise going fine.
	Recorder Recorder

	// MaxIterations bounds agentic rounds. Zero uses the default.
	MaxIterations int

	// MaxCalls bounds total tool calls. Zero is unbounded - only the iteration
	// count is a hard default backstop.
	MaxCalls int

	// MaxContinuations bounds recovery attempts within a run - a truncated
	// response, an empty turn, or a retriable provider error. Zero uses the
	// default.
	MaxContinuations int

	// RetryBackoff is the pause before the first retry of a retriable provider
	// failure, doubling per consecutive retry up to a cap. Zero uses the default.
	// Negative disables the wait, which spends the whole continuation budget in
	// milliseconds - only a test driving an outage should ask for that.
	RetryBackoff time.Duration

	// MaxCycles bounds how many times the loop nudges the model out of a detected
	// repetition before giving up. Zero uses the default. A safety guard: the
	// default encodes a real failure mode.
	MaxCycles int

	// MaxEmpties bounds consecutive turns that produce neither text nor a tool
	// call before the run bails. Zero uses the default.
	MaxEmpties int

	// MaxDuration bounds the wall-clock time of a run, checked at each iteration
	// boundary. Zero is unbounded.
	MaxDuration time.Duration

	// MaxSettles bounds how many times the model is nudged to record an outcome
	// before the run is surfaced as unsettled. Zero uses the default.
	//
	// @note there is no switch to turn settlement off. zot exists to run
	// unattended, and an unattended run needs an unambiguous ending - a terminal
	// tool call, not prose that happens to sound final. The SDK this replaces
	// decided a task was complete when the answer contained the word
	// "completed", which is exactly the failure mode settlement removes.
	MaxSettles int

	// LimitCheckpoints are the percentages of a bounded limit at which the model
	// is told it is approaching that limit. Nil uses the default; an empty slice
	// disables the notices.
	LimitCheckpoints []int

	// MaxTokens bounds a single response. Nil is unbounded - the request carries
	// no cap, so the model produces its full output.
	//
	// @note there is deliberately no Temperature. Reasoning models ignore or
	// reject it, providers disagree on its range, and a coding agent wants the
	// model's default behaviour rather than a sampling knob nobody tunes
	// deliberately.
	MaxTokens *int

	// ContextStrategy selects what happens as the conversation approaches the
	// model's context window: StrategyCompact summarises older history into a
	// checkpoint with a model call, StrategyTruncate drops the oldest messages to
	// fit. Empty means StrategyCompact - the default - so the library and the CLI
	// agree without the caller having to opt in. CompactMinTokens,
	// CompactMinMessages and CompactTriggerRatio tune when compaction fires; zero
	// uses the built-in default for each.
	ContextStrategy     string
	CompactMinTokens    int
	CompactMinMessages  int
	CompactTriggerRatio float64

	// ContextWindow overrides the model's total context window, in tokens, for
	// a serving endpoint whose real ceiling is smaller than the model's card.
	// Zero uses the catalogue.
	ContextWindow int
}

// exitEventGrace is how long a cancelled run holds the exit event for a
// consumer that is still draining the channel. A draining consumer receives
// within microseconds; the window only elapses in full when the channel has
// been abandoned, and it is what bounds the leak that abandonment used to be.
const exitEventGrace = time.Second

// ExecuteWithTools runs an autonomous conversation.
//
// Events stream on the first channel until the run ends, at which point both
// channels close. A terminal failure arrives on the second. The run always
// concludes with an AgentExitEvent, so a consumer that only watches events still
// learns how it ended.
func ExecuteWithTools(
	ctx context.Context,
	client *Client,
	options ExecuteWithToolsOptions,
) (<-chan AgentEvent, <-chan error) {
	events := make(chan AgentEvent)
	errs := make(chan error, 1)

	go func() {
		defer close(events)
		defer close(errs)

		if client == nil {
			errs <- errors.New("agent: no client")

			return
		}

		recorder := options.Recorder

		// The seed is recorded before the run starts so a session that dies in
		// its first turn still says what it was asked to do.
		seed := fromLoopMessages(toLoopMessages(options))

		if recorder != nil {
			for _, message := range seed {
				_ = recorder.RecordMessage(message)
			}
		}

		log := &conversationLog{recorder: recorder, recorded: seed}

		engine, err := loop.New(loop.Options{
			Client:              client.inner,
			Instructions:        options.Instructions,
			Messages:            toLoopMessages(options),
			Tools:               toLoopTools(withSkillTool(options.Tools, options.Skills)),
			Skills:              toLoopSkills(options.Skills),
			MaxIterations:       options.MaxIterations,
			MaxCalls:            options.MaxCalls,
			MaxContinuations:    options.MaxContinuations,
			RetryBackoff:        options.RetryBackoff,
			MaxCycles:           options.MaxCycles,
			MaxEmpties:          options.MaxEmpties,
			MaxDuration:         options.MaxDuration,
			MaxSettles:          settleBudget(options),
			MaxTokens:           options.MaxTokens,
			LimitCheckpoints:    options.LimitCheckpoints,
			Compact:             options.ContextStrategy != StrategyTruncate,
			CompactMinTokens:    options.CompactMinTokens,
			CompactMinMessages:  options.CompactMinMessages,
			CompactTriggerRatio: options.CompactTriggerRatio,
			ContextWindow:       options.ContextWindow,

			OnConversation: func(messages []loop.Message) {
				log.sync(fromLoopMessages(messages))
			},
		})
		if err != nil {
			errs <- err

			return
		}

		result := engine.Run(ctx, func(event loop.Event) {
			if recorder != nil {
				// a usage event carries its numbers in dedicated fields the
				// recorder interface does not have; render them into the text,
				// or the log records a usage event that says nothing - and
				// "why did this run cost 567k tokens" is exactly the question
				// a session has to answer after the fact
				text := event.Text

				if event.Kind == loop.EventUsage && text == "" {
					text = fmt.Sprintf("input %d output %d", event.InputTokens, event.OutputTokens)
				}

				_ = recorder.RecordEvent(string(event.Kind), event.Tool, text, event.Iteration)

				// A provider failure is persisted the instant it happens, not at
				// the run's end - a kill mid-retry would never reach the end, and
				// the failing exchange is exactly what the operator wants then.
				if failure := failureOf(event.Failure); failure != nil {
					_ = recorder.RecordFailure(failure)
				}
			}

			// The send stays synchronous - a slow consumer throttles the run -
			// but must not outlive it: an embedder that cancels the run and
			// walks away from the channel would otherwise park this goroutine
			// on a send nobody will ever receive, leaking it (and the run's
			// resources) for the life of the process. Once ctx is done the run
			// is aborting anyway, so a dropped trailing event loses nothing.
			if translated, ok := translate(event); ok {
				select {
				case events <- translated:
				case <-ctx.Done():
				}
			}
		})

		// the run's last turn happened after the final boundary hand-over, so the
		// ending is written down here
		log.sync(fromLoopMessages(result.Messages))

		if recorder != nil {
			_ = recorder.RecordResult(Summary{
				Reason:        string(result.Reason),
				Message:       result.Message,
				Error:         errText(result.Err),
				Failure:       failureOf(result.Err),
				Code:          exitCode(result.Reason),
				Iterations:    result.Budget.Iterations,
				Calls:         result.Budget.Calls,
				Continuations: result.Budget.Continuations,
				Cycles:        result.Budget.Cycles,
				Settles:       result.Budget.Settles,
			})
		}

		// The exit event is the contract - "the run always concludes with an
		// AgentExitEvent" - so a consumer draining to close must receive it even
		// when the run was cancelled. Cancellation alone cannot distinguish a
		// consumer that is draining from one that walked away, so after ctx is
		// done the send gets a grace window: a draining consumer takes the event
		// within it, and only an abandoned channel forfeits it - which is what
		// lets this goroutine end instead of leaking.
		exit := AgentExitEvent{
			Code:     exitCode(result.Reason),
			Reason:   string(result.Reason),
			Message:  result.Message,
			Messages: fromLoopMessages(result.Messages),
		}

		select {
		case events <- exit:
		case <-ctx.Done():
			select {
			case events <- exit:
			case <-time.After(exitEventGrace):
			}
		}

		if result.Err != nil {
			errs <- result.Err
		}
	}()

	return events, errs
}

// conversationLog keeps a Recorder in step with a conversation the engine
// rewrites underneath it.
//
// The log is written as the run goes, so a run that dies at iteration 500 leaves
// 500 iterations of work behind rather than just the brief it started from. The
// complication that made it tempting to write only at the end is compaction: the
// engine summarises its own history, so the conversation's prefix changes. That
// is what the reset is for - when the history no longer extends what was
// recorded, the record is discarded and rewritten, leaving one coherent
// conversation rather than turns that no longer exist.
type conversationLog struct {
	recorder Recorder
	recorded []Message
}

func (c *conversationLog) sync(current []Message) {
	if c.recorder == nil {
		return
	}

	if !sharePrefix(c.recorded, current) {
		_ = c.recorder.RecordReset()

		c.recorded = nil
	}

	if len(current) == len(c.recorded) {
		return
	}

	for _, message := range current[min(len(c.recorded), len(current)):] {
		_ = c.recorder.RecordMessage(message)
	}

	c.recorded = append([]Message(nil), current...)
}

// settleBudget resolves the settle-nudge allowance. Always positive: settlement
// is not optional.
func settleBudget(options ExecuteWithToolsOptions) int {
	if options.MaxSettles > 0 {
		return options.MaxSettles
	}

	return loop.DefaultMaxSettles
}

// Failure is the wire evidence of a provider refusal.
type Failure struct {
	// Status is the HTTP status of the refusal.
	Status int

	// ResponseBody is the raw (bounded) body the provider returned.
	ResponseBody string

	// RequestBytes is the size of the request that was refused - against a
	// suspected context ceiling, the number that turns a correlation into a
	// diagnosis.
	RequestBytes int

	// RequestBody is the JSON that was refused. Populated for the developer
	// dump; empty otherwise.
	RequestBody string
}

// failureOf extracts the wire evidence from an error ending, when there is any.
func failureOf(err error) *Failure {
	var providerErr *provider.Error

	if !errors.As(err, &providerErr) || providerErr.Status == 0 {
		return nil
	}

	return &Failure{
		Status:       providerErr.Status,
		ResponseBody: providerErr.Body,
		RequestBytes: providerErr.RequestBytes,
		RequestBody:  providerErr.RequestBody,
	}
}

// errText renders an error for the record, tolerating nil.
func errText(err error) string {
	if err == nil {
		return ""
	}

	return err.Error()
}

// exitCode maps a stop reason onto a process-style exit code.
//
// Zero means the agent reached a conclusion it stands behind and that conclusion
// was success - it settled, or it finished talking in a non-settle run. Everything
// else means the task did not get done: either the model declared it could not be
// done (StopFailed) or the run was cut short by a guard. A caller scripting
// against zot needs to tell those apart without parsing prose.
func exitCode(reason loop.StopReason) int {
	switch reason {
	case loop.StopSettled, loop.StopStop:
		return 0
	default:
		return 1
	}
}

func toLoopMessages(options ExecuteWithToolsOptions) []loop.Message {
	messages := make([]loop.Message, 0, len(options.Messages)+len(options.Text))

	for _, message := range options.Messages {
		messages = append(messages, loop.Message{
			Type:     message.Type,
			Text:     message.Text,
			Activity: message.Activity,
		})
	}

	for _, text := range options.Text {
		messages = append(messages, loop.Message{Type: loop.TypeUser, Text: text})
	}

	return messages
}

// sharePrefix reports whether the run still begins with the messages it started
// with. False means the engine rewrote its own history - the compaction case.
func sharePrefix(seed, final []Message) bool {
	if len(seed) > len(final) {
		return false
	}

	for index, message := range seed {
		if message.Type != final[index].Type || message.Text != final[index].Text {
			return false
		}
	}

	return true
}

func fromLoopMessages(messages []loop.Message) []Message {
	converted := make([]Message, 0, len(messages))

	for _, message := range messages {
		converted = append(converted, Message{
			Type:     message.Type,
			Text:     message.Text,
			Activity: message.Activity,
		})
	}

	return converted
}

func toLoopTools(tools Tools) map[string]loop.ToolDefinition {
	converted := make(map[string]loop.ToolDefinition, len(tools))

	for name, definition := range tools {
		handler := definition.Handler

		converted[name] = loop.ToolDefinition{
			Name:        name,
			Description: definition.Description,
			Parameters:  definition.Parameters,
			Handler: func(ctx context.Context, args map[string]any) (any, error) {
				if handler == nil {
					return nil, fmt.Errorf("tool %q has no handler", name)
				}

				return handler(ctx, args)
			},
		}
	}

	return converted
}

func toLoopSkills(skills []SkillDefinition) []loop.Skill {
	converted := make([]loop.Skill, 0, len(skills))

	for _, skill := range skills {
		converted = append(converted, loop.Skill{
			Name:        skill.Name,
			Description: skill.Description,
			Path:        skill.Path,
			Hint:        skill.Hint(),
		})
	}

	return converted
}

// withSkillTool adds the `skill` tool when any skill is embedded.
//
// Registered automatically rather than left to the caller: an embedded skill is
// unreachable without it, and a caller who merely passed Skills would have no
// reason to know that.
func withSkillTool(tools Tools, skills []SkillDefinition) Tools {
	result := &SkillsResult{Skills: skills}

	definition := result.Tool()

	if definition == nil {
		return tools
	}

	combined := make(Tools, len(tools)+1)

	for name, tool := range tools {
		combined[name] = tool
	}

	// a caller's own `skill` tool wins - overriding a built-in must stay possible
	if _, taken := combined["skill"]; !taken {
		combined["skill"] = *definition
	}

	return combined
}
