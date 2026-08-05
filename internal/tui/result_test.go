package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/chatbotkit/zot/agent"
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

// The exit error is what the CLI prints and what the process status is derived
// from, so it has to read sensibly with and without an explanation.
func TestAgentExitErrorMessage(t *testing.T) {
	tests := []struct {
		name string
		err  *AgentExitError
		want string
	}{
		{
			name: "with an explanation",
			err:  &AgentExitError{Code: 2, Message: "could not build the project"},
			want: "agent exited with code 2: could not build the project",
		},
		{
			name: "without one",
			err:  &AgentExitError{Code: 3},
			want: "agent exited with code 3",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.err.Error(); got != test.want {
				t.Errorf("Error() = %q, want %q", got, test.want)
			}
		})
	}
}

// A stream that ends with no exit event is a failure, not a success. It is what
// an iteration cap or a killed provider connection looks like, and reporting it
// as a clean run would hide a run that did not finish.
func TestAStreamEndingWithoutAnExitIsAFailure(t *testing.T) {
	m := sized(t, 80, 24)

	updated, _ := m.Update(agentDoneMsg{})

	final := updated.(model)

	if final.status != statusFailed {
		t.Errorf("status = %v, want failed", final.status)
	}

	if final.runError() == nil {
		t.Error("a stream that ended without an exit must be reported as an error")
	}
}

// The same message arriving after a clean exit must not turn a finished run
// into a failed one.
func TestDoneAfterAnExitLeavesTheStatusAlone(t *testing.T) {
	m := sized(t, 80, 24)

	m.status = statusDone

	updated, _ := m.Update(agentDoneMsg{})

	if got := updated.(model).status; got != statusDone {
		t.Errorf("status = %v, want it left alone", got)
	}

	if updated.(model).runError() != nil {
		t.Error("a finished run must stay finished")
	}
}

// An error arriving after the run already ended must not overwrite the outcome.
func TestAnErrorAfterTheRunEndedIsIgnored(t *testing.T) {
	m := sized(t, 80, 24)

	m.status = statusDone

	updated, _ := m.Update(agentErrMsg{err: errAfterTheFact})

	if updated.(model).runError() != nil {
		t.Error("a late error must not resurrect a finished run as a failure")
	}
}

var errAfterTheFact = errors.New("too late")
