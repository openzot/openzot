package tui

import "fmt"

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
