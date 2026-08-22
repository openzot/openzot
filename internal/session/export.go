package session

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/openzot/openzot/internal/provider"
)

// Trajectory is a session rendered for consumption outside zot: the
// conversation in the chat shape the rest of the ecosystem speaks - system,
// user, assistant with tool_calls, tool - plus the facts about the run that
// make one trajectory comparable to the next.
//
// The shape is the OpenAI messages convention because it is what training and
// evaluation tooling already loads: a file of these is a dataset, not a format
// somebody has to write a parser for. Where zot knows more than that shape can
// hold - which of its own message types a turn was, the model's reasoning, the
// images a tool produced - it is carried in additive fields a loader that does
// not know them simply drops.
type Trajectory struct {
	// ID is the session this trajectory was exported from - the last of its
	// chain, which carries the whole conversation.
	ID string `json:"id"`

	// Chain lists the sessions behind this one, oldest first and ending in ID:
	// a run continued across interruptions is one trajectory, not several.
	Chain []string `json:"chain"`

	Task     string `json:"task"`
	Model    string `json:"model"`
	Provider string `json:"provider"`
	Driver   string `json:"driver"`
	Workdir  string `json:"workdir,omitempty"`

	Started time.Time `json:"started"`
	Ended   time.Time `json:"ended"`

	// Outcome is how the run ended; nil when it did not.
	Outcome *Result `json:"outcome,omitempty"`

	// Complete is whether an outcome was recorded, and Truncated whether the
	// log ended mid-record - a run that was killed rather than one that stopped.
	Complete  bool `json:"complete"`
	Truncated bool `json:"truncated,omitempty"`

	// Messages is the conversation as it stood at the end: what a resume would
	// replay. After compaction that is a summary plus the recent turns, not
	// every turn that happened.
	Messages []ChatMessage `json:"messages"`

	// Snapshots are the earlier states of the conversation that compaction or
	// a resume superseded, oldest first, each a complete conversation as it
	// stood at that point. Only filled when asked for: it is where the turns
	// that Messages no longer holds live, at the price of repeating the ones
	// it does.
	Snapshots [][]ChatMessage `json:"snapshots,omitempty"`

	// Images lists the image files this trajectory refers to, relative to the
	// trajectory file, in the order they were first shown.
	Images []string `json:"images,omitempty"`

	// Events counts what happened by kind - iterations, nudges, retries - for
	// filtering without walking the conversation.
	Events map[string]int `json:"events,omitempty"`
}

// ChatMessage is one turn in the exported conversation.
type ChatMessage struct {
	// Role is the chat-convention role: system, user, assistant or tool.
	Role string `json:"role"`

	// Type is zot's own message type, which says more than the role does: a
	// "checkpoint" and an "instructions" turn are both system, but one is a
	// compaction summary and the other the run's brief.
	Type string `json:"type"`

	// Content is a string, or a list of ContentPart when the turn carries
	// images beside its text.
	Content any `json:"content"`

	// Reasoning is the model's scratchpad for the turn, when the provider
	// surfaced it.
	Reasoning string `json:"reasoning,omitempty"`

	// ToolCalls are the calls an assistant turn made.
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`

	// ToolCallID and Name identify which call a tool turn answers.
	ToolCallID string `json:"tool_call_id,omitempty"`
	Name       string `json:"name,omitempty"`
}

// ToolCall is one call in an assistant turn, in the OpenAI shape.
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}

// FunctionCall names the tool and carries its arguments verbatim.
type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ContentPart is one piece of a multi-part turn.
type ContentPart struct {
	Type string `json:"type"`

	// Text is set on a "text" part.
	Text string `json:"text,omitempty"`

	// Image is the path of an "image" part, relative to the trajectory file.
	Image string `json:"image,omitempty"`
}

// ExportOptions shape an export.
type ExportOptions struct {
	// ImageDir is where image blobs are copied to; empty keeps images out of
	// the export (their turns keep the text that described them). Paths in the
	// trajectory are written relative to RelativeTo, which defaults to
	// ImageDir's parent.
	ImageDir   string
	RelativeTo string

	// Snapshots includes the superseded conversations - see
	// Trajectory.Snapshots.
	Snapshots bool
}

// Export renders a session as a trajectory.
//
// Chain holds the sessions this one continued, oldest first; it may be empty.
// Their messages are not needed - a resumed session re-records the history it
// continues, so the last log carries the whole conversation - but their
// discarded states are, when snapshots are asked for, and their ids are the
// provenance either way.
func Export(s *Session, chain []*Session, options ExportOptions) (*Trajectory, error) {
	if s == nil {
		return nil, fmt.Errorf("session: nothing to export")
	}

	exporter := &exporter{options: options}

	trajectory := &Trajectory{
		ID:        s.Meta.ID,
		Task:      s.Meta.Task,
		Model:     s.Meta.Model,
		Provider:  s.Meta.Provider,
		Driver:    s.Meta.Driver,
		Workdir:   s.Meta.Workdir,
		Started:   s.Started,
		Ended:     s.Ended,
		Outcome:   s.Result,
		Complete:  s.Complete(),
		Truncated: s.Truncated,
		Events:    map[string]int{},
	}

	for _, previous := range chain {
		trajectory.Chain = append(trajectory.Chain, previous.Meta.ID)

		if !previous.Started.IsZero() && (trajectory.Started.IsZero() || previous.Started.Before(trajectory.Started)) {
			trajectory.Started = previous.Started
		}

		for _, event := range previous.Events {
			trajectory.Events[event.Kind]++
		}

		if options.Snapshots {
			for _, discarded := range previous.Discarded {
				trajectory.Snapshots = append(trajectory.Snapshots, exporter.convert(previous, discarded))
			}

			if len(previous.Messages) > 0 {
				trajectory.Snapshots = append(trajectory.Snapshots, exporter.convert(previous, previous.Messages))
			}
		}
	}

	trajectory.Chain = append(trajectory.Chain, s.Meta.ID)

	for _, event := range s.Events {
		trajectory.Events[event.Kind]++
	}

	if options.Snapshots {
		for _, discarded := range s.Discarded {
			trajectory.Snapshots = append(trajectory.Snapshots, exporter.convert(s, discarded))
		}
	}

	trajectory.Messages = exporter.convert(s, s.Messages)
	trajectory.Images = exporter.images

	if exporter.err != nil {
		return trajectory, exporter.err
	}

	return trajectory, nil
}

// exporter carries the state one export accumulates across conversations: the
// images already copied, so a screenshot shown in three snapshots is one file.
type exporter struct {
	options ExportOptions
	images  []string
	copied  map[string]string
	err     error
}

// convert renders one conversation.
//
// The turns zot records are finer than the chat shape: a turn's reasoning, its
// text and each of its tool calls are separate records, and the chat shape
// wants them as one assistant message. Consecutive assistant-side records are
// folded into one message until something else - a tool result, a user turn -
// closes it.
func (e *exporter) convert(s *Session, messages []Message) []ChatMessage {
	var (
		out     []ChatMessage
		pending *ChatMessage
	)

	flush := func() {
		if pending != nil {
			out = append(out, *pending)
			pending = nil
		}
	}

	assistant := func(typ string) *ChatMessage {
		if pending == nil {
			pending = &ChatMessage{Role: "assistant", Type: typ, Content: ""}
		}

		return pending
	}

	for _, message := range messages {
		switch message.Type {
		case "instructions", "checkpoint":
			flush()
			out = append(out, ChatMessage{Role: "system", Type: message.Type, Content: message.Text})

		case "user":
			flush()
			out = append(out, ChatMessage{Role: "user", Type: message.Type, Content: message.Text})

		case "attachment":
			flush()
			out = append(out, ChatMessage{Role: "user", Type: message.Type, Content: e.parts(s, message)})

		case "reasoning":
			turn := assistant("bot")

			if turn.Reasoning != "" {
				turn.Reasoning += "\n\n"
			}

			turn.Reasoning += message.Text

		case "bot":
			turn := assistant("bot")

			// a second answer in one turn does not happen; if a log holds one,
			// it is a new turn
			if text, _ := turn.Content.(string); text != "" || len(turn.ToolCalls) > 0 {
				flush()
				turn = assistant("bot")
			}

			turn.Content = message.Text

		case "activity":
			activity := message.Activity
			if activity == nil {
				continue
			}

			switch activity.Kind {
			case "request":
				turn := assistant("bot")
				turn.ToolCalls = append(turn.ToolCalls, ToolCall{
					ID:       activity.ID,
					Type:     "function",
					Function: FunctionCall{Name: activity.Name, Arguments: activity.Arguments},
				})

				// the reasoning state travels on the first call of a turn, so
				// that is where it is read back from
				if turn.Reasoning == "" && len(activity.ReasoningItems) > 0 {
					turn.Reasoning = reasoningText(activity.ReasoningItems)
				}

			case "response":
				flush()
				out = append(out, ChatMessage{
					Role:       "tool",
					Type:       message.Type,
					Content:    toolContent(activity),
					ToolCallID: activity.ID,
					Name:       activity.Name,
				})

			default:
				// a trigger is an instruction to act, which is a user turn in
				// every chat convention
				flush()
				out = append(out, ChatMessage{Role: "user", Type: message.Type, Content: message.Text})
			}

		default:
			// a type this build does not know is still a turn; a user turn is
			// the one that loses nothing
			flush()
			out = append(out, ChatMessage{Role: "user", Type: message.Type, Content: message.Text})
		}
	}

	flush()

	return out
}

// parts renders an attachment's text and images.
func (e *exporter) parts(s *Session, message Message) []ContentPart {
	parts := []ContentPart{{Type: "text", Text: message.Text}}

	for _, image := range message.Images {
		if path := e.store(s, image); path != "" {
			parts = append(parts, ContentPart{Type: "image", Image: path})
		}
	}

	return parts
}

// store copies an image out of the session's blobs and returns the path to
// write in its place, empty when there is nothing to copy or nowhere to put it.
//
// A missing blob is not an error: the attachment's text is written to stand on
// its own. A blob that cannot be copied is - the export was asked for images
// and could not deliver one, which the caller should hear about.
func (e *exporter) store(s *Session, image provider.Image) string {
	if e.options.ImageDir == "" {
		return ""
	}

	loaded := s.LoadImage(image)

	if len(loaded.Bytes) == 0 && loaded.Data != "" {
		if data, err := base64.StdEncoding.DecodeString(loaded.Data); err == nil {
			loaded.Bytes = data
		}
	}

	if len(loaded.Bytes) == 0 {
		return ""
	}

	name := blobName(loaded)

	if e.copied == nil {
		e.copied = map[string]string{}
	}

	if path, ok := e.copied[name]; ok {
		return path
	}

	if err := os.MkdirAll(e.options.ImageDir, 0o700); err != nil {
		e.err = fmt.Errorf("create image directory: %w", err)

		return ""
	}

	target := filepath.Join(e.options.ImageDir, name)

	if err := os.WriteFile(target, loaded.Bytes, 0o600); err != nil {
		e.err = fmt.Errorf("write image %s: %w", name, err)

		return ""
	}

	base := e.options.RelativeTo
	if base == "" {
		base = filepath.Dir(e.options.ImageDir)
	}

	path, err := filepath.Rel(base, target)
	if err != nil {
		path = target
	}

	path = filepath.ToSlash(path)

	e.copied[name] = path
	e.images = append(e.images, path)

	return path
}

// toolContent renders what a tool returned the way the model read it: a string
// as itself, anything else as JSON, a failure as its text.
func toolContent(activity *Activity) string {
	if activity.Failure != "" {
		return activity.Failure
	}

	switch result := activity.Result.(type) {
	case nil:
		return ""
	case string:
		return result
	default:
		encoded, err := json.Marshal(result)
		if err != nil {
			return fmt.Sprint(result)
		}

		return string(encoded)
	}
}

// reasoningText flattens the reasoning items a turn carried into the text a
// reader can use: the summaries, which are what the provider let through.
// Encrypted state contributes nothing; the field is a convenience, not the
// wire state.
func reasoningText(items []provider.ReasoningItem) string {
	var texts []string

	for _, item := range items {
		for _, entry := range item.Summary {
			part, ok := entry.(map[string]any)
			if !ok {
				continue
			}

			if text, ok := part["text"].(string); ok && strings.TrimSpace(text) != "" {
				texts = append(texts, strings.TrimSpace(text))
			}
		}
	}

	return strings.Join(texts, "\n\n")
}
