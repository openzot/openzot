package tui

import "fmt"

// Outcome is the run's recorded ending: the stop reason and the message the
// terminal tool carried. For most runs it is display material the caller
// already watched; for a run whose deliverable IS its recorded outcome - a
// draft run delivering criteria through the success summary - it is the
// return value.
type Outcome struct {
	// Reason is the machine-readable stop reason (see the agent.Reason
	// constants).
	Reason string

	// Message is the prose the ending carried - a success summary, a failure
	// reason, or a guard's explanation.
	Message string
}

// AgentExitError reports an agent-declared failed run to callers so the CLI can
// return a non-zero process status.
type AgentExitError struct {
	Code    int
	Message string
}

func (e *AgentExitError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("agent exited with code %d", e.Code)
	}
	return fmt.Sprintf("agent exited with code %d: %s", e.Code, e.Message)
}

func (m model) runError() error {
	if m.err != nil {
		return m.err
	}
	if m.exitCode != 0 {
		return &AgentExitError{Code: m.exitCode, Message: m.exitMsg}
	}
	if m.status == statusRunning {
		return fmt.Errorf("agent run ended before completion")
	}
	return nil
}

func (m model) outcome() Outcome {
	return Outcome{Reason: m.exitReason, Message: m.exitMsg}
}
