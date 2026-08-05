package loop

import (
	"strings"
	"testing"

	"github.com/openzot/openzot/internal/provider"
)

// activity builds one half of a tool-call pair.
func activity(kind ActivityKind, id, name, arguments string, result any) Message {
	entry := &Activity{Kind: kind, ID: id, Name: name, Arguments: arguments}

	if kind == ActivityResponse {
		entry.Result = result
	}

	return Message{Type: TypeActivity, Text: entry.ResultText(), Activity: entry}
}

func TestToChatMessagesPairsToolCalls(t *testing.T) {
	messages := []Message{
		{Type: TypeUser, Text: "list the files"},
		activity(ActivityRequest, "c1", "shell", `{"command":"ls"}`, nil),
		activity(ActivityResponse, "c1", "shell", `{"command":"ls"}`, "README.md"),
		{Type: TypeBot, Text: "there is a README"},
	}

	chat := toChatMessages(messages)

	if len(chat) != 4 {
		t.Fatalf("got %d messages, want 4: %+v", len(chat), chat)
	}

	if chat[0].Role != provider.RoleUser {
		t.Errorf("chat[0] role = %q, want user", chat[0].Role)
	}

	if chat[1].Role != provider.RoleAssistant || len(chat[1].ToolCalls) != 1 {
		t.Fatalf("chat[1] should be an assistant turn carrying one tool call: %+v", chat[1])
	}

	if got := chat[1].ToolCalls[0].ID; got != "c1" {
		t.Errorf("tool call id = %q, want c1", got)
	}

	if chat[2].Role != provider.RoleTool || chat[2].ToolCallID != "c1" {
		t.Errorf("chat[2] should be a tool result referencing c1: %+v", chat[2])
	}

	if chat[2].Content != "README.md" {
		t.Errorf("tool result content = %q, want the handler output", chat[2].Content)
	}
}

// A tool result whose request was trimmed away would be rejected by the
// provider, so it must not be sent on its own.
func TestToChatMessagesDropsOrphanedResult(t *testing.T) {
	messages := []Message{
		{Type: TypeUser, Text: "go"},
		activity(ActivityResponse, "c1", "shell", `{}`, "output"),
	}

	chat := toChatMessages(messages)

	for _, message := range chat {
		if message.Role == provider.RoleTool {
			t.Fatalf("an orphaned tool result must be dropped: %+v", message)
		}
	}
}

// The mirror case: a request whose result never arrived leaves the conversation
// invalid, so the assistant turn goes too.
func TestToChatMessagesDropsDanglingRequest(t *testing.T) {
	messages := []Message{
		{Type: TypeUser, Text: "go"},
		activity(ActivityRequest, "c1", "shell", `{}`, nil),
	}

	chat := toChatMessages(messages)

	for _, message := range chat {
		if len(message.ToolCalls) > 0 {
			t.Fatalf("a request with no result must be dropped: %+v", message)
		}
	}

	if len(chat) != 1 {
		t.Errorf("got %d messages, want just the user turn", len(chat))
	}
}

func TestToChatMessagesRoleMapping(t *testing.T) {
	messages := []Message{
		{Type: TypeBackstory, Text: "you are an agent"},
		{Type: TypeContext, Text: "summary of earlier turns"},
		{Type: TypeReasoning, Text: "thinking out loud"},
		{Type: TypeBot, Text: "the answer"},
		{Type: TypeUser, Text: "a question"},
	}

	chat := toChatMessages(messages)

	// reasoning is the model's scratchpad and providers reject their own
	// reasoning content on the way back in, so it is not replayed
	for _, message := range chat {
		if message.Content == "thinking out loud" {
			t.Fatal("reasoning must not be replayed to the provider")
		}
	}

	want := []string{
		provider.RoleSystem,
		provider.RoleSystem,
		provider.RoleAssistant,
		provider.RoleUser,
	}

	if len(chat) != len(want) {
		t.Fatalf("got %d messages, want %d: %+v", len(chat), len(want), chat)
	}

	for index, role := range want {
		if chat[index].Role != role {
			t.Errorf("chat[%d] role = %q, want %q", index, chat[index].Role, role)
		}
	}
}

func TestToChatMessagesEncodesStructuredResults(t *testing.T) {
	messages := []Message{
		activity(ActivityRequest, "c1", "search", `{}`, nil),
		activity(ActivityResponse, "c1", "search", `{}`, map[string]any{"records": []any{}}),
	}

	chat := toChatMessages(messages)

	if len(chat) != 2 {
		t.Fatalf("got %d messages, want 2", len(chat))
	}

	if chat[1].Content != `{"records":[]}` {
		t.Errorf("structured result = %q, want it JSON-encoded", chat[1].Content)
	}
}

// An activity message that describes no call is not renderable: there is no
// tool to name and no id to reference, so it must not reach the wire.
func TestMalformedActivitiesDoNotReachTheWire(t *testing.T) {
	cases := []Message{
		{Type: TypeActivity},
		{Type: TypeActivity, Activity: &Activity{}},
		{Type: TypeActivity, Activity: &Activity{Kind: "somethingelse", ID: "c1"}},
	}

	for index, message := range cases {
		if chat := toChatMessages([]Message{message}); len(chat) != 0 {
			t.Errorf("case %d: a malformed activity reached the wire as %+v", index, chat)
		}
	}
}

// The cycle heuristics read the platform's map shape, because their corpus is
// JSON captured from the TypeScript implementation. A conversion that loses the
// call silently stops detecting loops.
func TestThreadRoundTripPreservesTheCall(t *testing.T) {
	original := []Message{activity(ActivityResponse, "c1", "search", `{"q":"x"}`, "none")}

	round := fromThreadMessages(toThreadMessages(original))

	if len(round) != 1 || round[0].Activity == nil {
		t.Fatalf("round trip lost the activity: %+v", round)
	}

	got := round[0].Activity

	if got.Kind != ActivityResponse || got.ID != "c1" || got.Name != "search" {
		t.Errorf("round trip = %+v", got)
	}

	if got.Arguments != `{"q":"x"}` || got.Result != "none" {
		t.Errorf("round trip lost the payload: %+v", got)
	}
}

// What compaction needs is the price of a message, so the payload has to
// survive the trip - a request half is otherwise free.
func TestCompactionRoundTripCarriesThePayload(t *testing.T) {
	original := []Message{activity(ActivityRequest, "c1", "write", `{"path":"a.go"}`, nil)}

	converted := toCompactionMessages(original)

	if len(converted) != 1 || converted[0].Payload == "" {
		t.Fatalf("the payload did not reach compaction: %+v", converted)
	}

	if !strings.Contains(converted[0].Payload, "a.go") {
		t.Errorf("payload = %q, want the arguments in it", converted[0].Payload)
	}
}
