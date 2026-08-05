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

	"github.com/openzot/openzot/internal/loop"
	"github.com/openzot/openzot/internal/provider"
)

// Client is a configured connection to a model provider.
type Client struct {
	inner *provider.Client
}

// ClientOptions configures a Client.
type ClientOptions struct {
	// Provider names the backend: "openai", "anthropic", "groq", "mistral",
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

	// TypeBackstory is system context.
	TypeBackstory = loop.TypeBackstory

	// TypeContext is injected context, including a compaction summary.
	TypeContext = loop.TypeContext
)

// Recorder receives a run's messages and events as they happen.
//
// Both methods may be called from the engine's goroutine, so an implementation
// has to be safe for that. Errors are ignored by design.
type Recorder interface {
	RecordMessage(Message) error
	RecordEvent(kind, tool, text string, iteration int) error
	RecordResult(Summary) error

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

// ExecuteWithToolsOptions configures a run.
type ExecuteWithToolsOptions struct {
	// Backstory is the system prompt.
	Backstory string

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

	// MaxCalls bounds total tool calls. Zero uses the default.
	MaxCalls int

	// MaxSettles bounds how many times the model is nudged to record an outcome
	// before the run is surfaced as unsettled. Zero uses the default.
	//
	// @note there is no switch to turn settlement off. zot exists to run
	// unattended, and an unattended run needs an unambiguous ending - a terminal
	// tool call, not prose that happens to sound final. The SDK this replaces
	// decided a task was complete when the answer contained the word
	// "completed", which is exactly the failure mode settlement removes.
	MaxSettles int

	// MaxTokens bounds a single response. Zero lets the provider decide.
	//
	// @note there is deliberately no Temperature. Reasoning models ignore or
	// reject it, providers disagree on its range, and a coding agent wants the
	// model's default behaviour rather than a sampling knob nobody tunes
	// deliberately.
	MaxTokens *int
}

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

		engine, err := loop.New(loop.Options{
			Client:        client.inner,
			Backstory:     options.Backstory,
			Messages:      toLoopMessages(options),
			Tools:         toLoopTools(withSkillTool(options.Tools, options.Skills)),
			Skills:        toLoopSkills(options.Skills),
			MaxIterations: options.MaxIterations,
			MaxCalls:      options.MaxCalls,
			MaxSettles:    settleBudget(options),
			MaxTokens:     options.MaxTokens,
		})
		if err != nil {
			errs <- err

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

		result := engine.Run(ctx, func(event loop.Event) {
			if recorder != nil {
				_ = recorder.RecordEvent(string(event.Kind), event.Tool, event.Text, event.Iteration)
			}

			if translated, ok := translate(event); ok {
				events <- translated
			}
		})

		// @note the conversation is recorded once the run ends rather than turn by
		// turn: the loop rewrites its own history when it compacts, so an
		// incremental log would keep turns that no longer exist and resume into a
		// conversation the agent never actually had. When the ending differs from
		// the seed - which is exactly what compaction looks like from here - the
		// record is reset first so what remains is one coherent history.
		if recorder != nil {
			final := fromLoopMessages(result.Messages)

			if !sharePrefix(seed, final) {
				_ = recorder.RecordReset()

				seed = nil
			}

			for _, message := range final[min(len(seed), len(final)):] {
				_ = recorder.RecordMessage(message)
			}
		}

		if recorder != nil {
			_ = recorder.RecordResult(Summary{
				Reason:        string(result.Reason),
				Message:       result.Message,
				Code:          exitCode(result.Reason),
				Iterations:    result.Budget.Iterations,
				Calls:         result.Budget.Calls,
				Continuations: result.Budget.Continuations,
				Cycles:        result.Budget.Cycles,
				Settles:       result.Budget.Settles,
			})
		}

		events <- AgentExitEvent{
			Code:     exitCode(result.Reason),
			Reason:   string(result.Reason),
			Message:  result.Message,
			Messages: fromLoopMessages(result.Messages),
		}

		if result.Err != nil {
			errs <- result.Err
		}
	}()

	return events, errs
}

// settleBudget resolves the settle-nudge allowance. Always positive: settlement
// is not optional.
func settleBudget(options ExecuteWithToolsOptions) int {
	if options.MaxSettles > 0 {
		return options.MaxSettles
	}

	return loop.DefaultMaxSettles
}

// exitCode maps a stop reason onto a process-style exit code.
//
// Zero means the agent reached a conclusion it stands behind - it settled, or it
// finished talking in a non-settle run. Everything else means the run was cut
// short, and a caller scripting against zot needs to be able to tell those apart
// without parsing prose.
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
