package session

import "github.com/openzot/openzot/agent"

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

	// the bytes go to a blob beside the log; the record keeps the shape
	for _, image := range message.Images {
		stored, err := r.writer.StoreImage(image)

		if err != nil {
			// a log that cannot hold an image is not a reason to fail the run:
			// the model has already seen it, and the text still describes it
			stored = image
			stored.Bytes = nil
		}

		entry.Images = append(entry.Images, stored)
	}

	if activity := message.Activity; activity != nil {
		entry.Activity = &Activity{
			Kind:             string(activity.Kind),
			ID:               activity.ID,
			Name:             activity.Name,
			Arguments:        activity.Arguments,
			Result:           activity.Result,
			Failure:          activity.Failure,
			ReasoningItems:   activity.ReasoningItems,
			ReasoningDetails: activity.ReasoningDetails,
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

// RecordFailure writes the failing exchange to disk immediately, so a run
// killed mid-retry still leaves it behind. Dev-gated, like the end-of-run
// dump: the request body is the whole prompt.
func (r *Recorder) RecordFailure(failure *agent.Failure) error {
	if r == nil || r.writer == nil {
		return nil
	}

	r.dumpFailure(failure)

	return nil
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

	var failure *Failure

	if summary.Failure != nil {
		failure = &Failure{
			Status:       summary.Failure.Status,
			ResponseBody: summary.Failure.ResponseBody,
			RequestBytes: summary.Failure.RequestBytes,
		}

		// A developer build dumps the exact refused exchange next to the
		// session log - the one artifact that makes an opaque upstream 400
		// diagnosable. Gated on the build, like .env loading: the request body
		// is the whole prompt, not something a released binary should spill to
		// disk beside every failed run. The session record keeps only the
		// bounded response; the full request lives solely in the dump.
		r.dumpFailure(summary.Failure)
	}

	return r.writer.Result(Result{
		Reason:        summary.Reason,
		Message:       summary.Message,
		Error:         summary.Error,
		Failure:       failure,
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

		for _, image := range message.Images {
			entry.Images = append(entry.Images, s.LoadImage(image))
		}

		if activity := message.Activity; activity != nil {
			entry.Activity = &agent.Activity{
				Kind:             agent.ActivityKind(activity.Kind),
				ID:               activity.ID,
				Name:             activity.Name,
				Arguments:        activity.Arguments,
				Result:           activity.Result,
				Failure:          activity.Failure,
				ReasoningItems:   activity.ReasoningItems,
				ReasoningDetails: activity.ReasoningDetails,
			}
		}

		messages = append(messages, entry)
	}

	return messages
}
