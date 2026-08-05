package session

import "github.com/chatbotkit/zot/agent"

// Recorder adapts a session Writer to the agent's recorder interface.
//
// The engine knows nothing about files; it hands over what happened and this
// decides where it lands. That is what keeps a session log an operational
// concern rather than something the loop has to carry.
type Recorder struct {
	writer *Writer
}

// The engine drives this through its own interface, so a signature change on
// either side has to fail at compile time rather than in a test nobody runs
// before pushing.
var _ agent.Recorder = (*Recorder)(nil)

// NewRecorder wraps a writer.
func NewRecorder(writer *Writer) *Recorder {
	return &Recorder{writer: writer}
}

// RecordMessage appends a conversation entry.
func (r *Recorder) RecordMessage(message agent.Message) error {
	if r == nil || r.writer == nil {
		return nil
	}

	entry := Message{Type: string(message.Type), Text: message.Text}

	if activity := message.Activity; activity != nil {
		entry.Activity = &Activity{
			Kind:      string(activity.Kind),
			ID:        activity.ID,
			Name:      activity.Name,
			Arguments: activity.Arguments,
			Result:    activity.Result,
			Failure:   activity.Failure,
		}
	}

	return r.writer.Message(entry)
}

// RecordEvent appends something that happened.
//
// Token-by-token narration is deliberately dropped: it is the same content the
// finished message already carries, and keeping it would make the log an order
// of magnitude larger for nothing.
func (r *Recorder) RecordEvent(kind, tool, text string, iteration int) error {
	if r == nil || r.writer == nil {
		return nil
	}

	switch kind {
	case "token", "reasoningToken":
		return nil
	}

	return r.writer.Event(Event{Kind: kind, Tool: tool, Text: text, Iteration: iteration})
}

// RecordReset discards the messages recorded so far.
func (r *Recorder) RecordReset() error {
	if r == nil || r.writer == nil {
		return nil
	}

	return r.writer.Reset()
}

// RecordResult appends the outcome and closes the log.
func (r *Recorder) RecordResult(summary agent.Summary) error {
	if r == nil || r.writer == nil {
		return nil
	}

	return r.writer.Result(Result{
		Reason:        summary.Reason,
		Message:       summary.Message,
		Code:          summary.Code,
		Iterations:    summary.Iterations,
		Calls:         summary.Calls,
		Continuations: summary.Continuations,
		Cycles:        summary.Cycles,
		Settles:       summary.Settles,
	})
}

// Messages converts a loaded session back into agent messages, ready to seed a
// resumed run.
func (s *Session) AgentMessages() []agent.Message {
	messages := make([]agent.Message, 0, len(s.Messages))

	for _, message := range s.Messages {
		entry := agent.Message{Type: agent.MessageType(message.Type), Text: message.Text}

		if activity := message.Activity; activity != nil {
			entry.Activity = &agent.Activity{
				Kind:      agent.ActivityKind(activity.Kind),
				ID:        activity.ID,
				Name:      activity.Name,
				Arguments: activity.Arguments,
				Result:    activity.Result,
				Failure:   activity.Failure,
			}
		}

		messages = append(messages, entry)
	}

	return messages
}
