package session

import (
	"testing"

	"github.com/chatbotkit/zot/agent"
)

// The recorder is the seam between the engine and the disk. What matters is
// that it satisfies the engine's interface, that it never breaks a run, and
// that what it writes reads back as something a run can resume from.

func TestRecorderRoundTripsARun(t *testing.T) {
	dir := t.TempDir()

	writer, err := Create(dir, "run", Meta{Task: "add a health endpoint"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	recorder := NewRecorder(writer)

	if err := recorder.RecordMessage(agent.Message{Type: agent.TypeUser, Text: "add a health endpoint"}); err != nil {
		t.Fatalf("RecordMessage: %v", err)
	}

	if err := recorder.RecordEvent("toolStart", "shell", "go test ./...", 1); err != nil {
		t.Fatalf("RecordEvent: %v", err)
	}

	if err := recorder.RecordMessage(agent.Message{
		Type: agent.TypeActivity,
		Text: "ok",
		Activity: &agent.Activity{
			Kind:      agent.ActivityResponse,
			ID:        "call_1",
			Name:      "shell",
			Arguments: `{"command":"go test ./..."}`,
			Result:    "ok",
		},
	}); err != nil {
		t.Fatalf("RecordMessage: %v", err)
	}

	if err := recorder.RecordResult(agent.Summary{
		Reason:     "stop",
		Message:    "finished",
		Code:       0,
		Iterations: 3,
		Calls:      2,
		Cycles:     1,
		Settles:    1,
	}); err != nil {
		t.Fatalf("RecordResult: %v", err)
	}

	session, err := Load(writer.Path())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	messages := session.AgentMessages()

	if len(messages) != 2 {
		t.Fatalf("got %d messages, want 2", len(messages))
	}

	// the type has to survive as a type, not as a string that happens to match:
	// a resumed run replays these straight into the engine
	if messages[0].Type != agent.TypeUser || messages[1].Type != agent.TypeActivity {
		t.Errorf("message types = %q, %q", messages[0].Type, messages[1].Type)
	}

	// the whole call has to survive, not just a label: a resumed run replays
	// these into the engine, which pairs them by id and replays the arguments
	// to the provider verbatim
	activity := messages[1].Activity

	if activity == nil {
		t.Fatal("the tool call was lost")
	}

	if activity.Kind != agent.ActivityResponse || activity.ID != "call_1" || activity.Name != "shell" {
		t.Errorf("activity = %+v", activity)
	}

	if activity.Arguments != `{"command":"go test ./..."}` || activity.Result != "ok" {
		t.Errorf("the call's payload was lost: %+v", activity)
	}

	if session.Result == nil {
		t.Fatal("the outcome must be recorded")
	}

	if session.Result.Reason != "stop" || session.Result.Iterations != 3 || session.Result.Settles != 1 {
		t.Errorf("result = %+v", session.Result)
	}

	if len(session.Events) != 1 || session.Events[0].Tool != "shell" {
		t.Errorf("events = %+v", session.Events)
	}
}

// Token events are the same text the finished message already carries. Keeping
// them would multiply the size of every log for nothing.
func TestTokenNarrationIsNotRecorded(t *testing.T) {
	dir := t.TempDir()

	writer, _ := Create(dir, "tokens", Meta{Task: "t"})

	recorder := NewRecorder(writer)

	for _, kind := range []string{"token", "reasoningToken"} {
		if err := recorder.RecordEvent(kind, "", "hello", 1); err != nil {
			t.Fatalf("RecordEvent(%q): %v", kind, err)
		}
	}

	_ = recorder.RecordEvent("iteration", "", "", 1)

	_ = writer.Close()

	session, err := Load(writer.Path())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if len(session.Events) != 1 || session.Events[0].Kind != "iteration" {
		t.Errorf("events = %+v", session.Events)
	}
}

// A recorder with nowhere to write is the no-session case, and it has to be
// silent rather than an error the run has to handle.
func TestANilRecorderIsHarmless(t *testing.T) {
	var recorder *Recorder

	if err := recorder.RecordMessage(agent.Message{Type: agent.TypeUser}); err != nil {
		t.Errorf("RecordMessage: %v", err)
	}

	if err := recorder.RecordEvent("iteration", "", "", 1); err != nil {
		t.Errorf("RecordEvent: %v", err)
	}

	if err := recorder.RecordResult(agent.Summary{}); err != nil {
		t.Errorf("RecordResult: %v", err)
	}

	empty := NewRecorder(nil)

	if err := empty.RecordMessage(agent.Message{Type: agent.TypeUser}); err != nil {
		t.Errorf("RecordMessage on an empty recorder: %v", err)
	}

	if err := empty.RecordResult(agent.Summary{}); err != nil {
		t.Errorf("RecordResult on an empty recorder: %v", err)
	}
}

func TestAgentMessagesOfAnEmptySession(t *testing.T) {
	session := &Session{}

	if got := session.AgentMessages(); len(got) != 0 {
		t.Errorf("AgentMessages = %+v, want empty", got)
	}
}
