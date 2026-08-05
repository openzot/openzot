package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/chatbotkit/go-sdk/agent"
)

func TestModelRunErrorReportsFailedAgentExit(t *testing.T) {
	m := newModel("task", "model", "cbk", "/tmp", false)
	m.handleEvent(agent.AgentExitEvent{Code: 7, Message: "verification failed"})

	err := m.runError()
	if err == nil {
		t.Fatal("runError() = nil, want a failed agent exit")
	}

	var exitErr *AgentExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("runError() = %T, want *AgentExitError", err)
	}
	if exitErr.Code != 7 || !strings.Contains(exitErr.Error(), "verification failed") {
		t.Errorf("exit error = %+v, want code 7 and the agent message", exitErr)
	}
}

func TestModelRunErrorReportsStreamError(t *testing.T) {
	want := errors.New("backend unavailable")
	m := newModel("task", "model", "cbk", "/tmp", false)
	m.err = want

	if got := m.runError(); !errors.Is(got, want) {
		t.Errorf("runError() = %v, want %v", got, want)
	}
}

func TestModelRunErrorRejectsEarlyViewerExit(t *testing.T) {
	m := newModel("task", "model", "cbk", "/tmp", false)

	if err := m.runError(); err == nil {
		t.Fatal("runError() = nil while the agent is still running")
	}
}
