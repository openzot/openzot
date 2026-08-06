package loop

import (
	"reflect"
	"testing"
)

// These cases are ported from the TypeScript engine's organizeMessages suite.
// They are the shapes that actually reached production - a result whose call
// was trimmed away, two identical calls in one turn, a trigger stranded in the
// middle of a history - and each one is a request a provider rejects outright.

// request builds a tool-call message.
func request(id, name, arguments string) Message {
	return Message{
		Type:     TypeActivity,
		Activity: &Activity{Kind: ActivityRequest, ID: id, Name: name, Arguments: arguments},
	}
}

// response builds the result half of a tool call.
func response(id, name, arguments, result string) Message {
	return Message{
		Type: TypeActivity,
		Text: result,
		Activity: &Activity{
			Kind:      ActivityResponse,
			ID:        id,
			Name:      name,
			Arguments: arguments,
			Result:    result,
		},
	}
}

// trigger builds a trigger activity.
func trigger(name string) Message {
	return Message{
		Type:     TypeActivity,
		Activity: &Activity{Kind: ActivityTrigger, Name: name},
	}
}

// kinds renders a conversation as a comparable shape.
func kinds(messages []Message) []string {
	var out []string

	for _, message := range messages {
		if activity := message.Activity; activity != nil {
			out = append(out, string(message.Type)+"/"+string(activity.Kind)+"/"+activity.ID)

			continue
		}

		out = append(out, string(message.Type)+"/"+message.Text)
	}

	return out
}

func TestOrganizeKeepsAWellFormedConversation(t *testing.T) {
	messages := []Message{
		{Type: TypeInstructions, Text: "you are a coding agent"},
		{Type: TypeUser, Text: "run the tests"},
		request("call_1", "shell", `{"command":"go test"}`),
		response("call_1", "shell", `{"command":"go test"}`, "ok"),
		{Type: TypeBot, Text: "they pass"},
	}

	got := Organize(messages)

	if !reflect.DeepEqual(kinds(got), kinds(messages)) {
		t.Errorf("a valid conversation must survive untouched:\n got %v\nwant %v", kinds(got), kinds(messages))
	}
}

// The rule providers enforce: a result immediately follows the call it answers.
func TestOrganizeClustersASeparatedPair(t *testing.T) {
	got := Organize([]Message{
		request("call_1", "shell", "{}"),
		{Type: TypeBot, Text: "thinking about it"},
		response("call_1", "shell", "{}", "ok"),
	})

	want := []string{"activity/request/call_1", "activity/response/call_1", "bot/thinking about it"}

	if !reflect.DeepEqual(kinds(got), want) {
		t.Errorf("got %v, want %v", kinds(got), want)
	}
}

func TestOrganizeClustersInterleavedPairs(t *testing.T) {
	got := Organize([]Message{
		request("call_1", "read", `{"path":"a"}`),
		request("call_2", "read", `{"path":"b"}`),
		response("call_2", "read", `{"path":"b"}`, "b contents"),
		response("call_1", "read", `{"path":"a"}`, "a contents"),
	})

	want := []string{
		"activity/request/call_1",
		"activity/response/call_1",
		"activity/request/call_2",
		"activity/response/call_2",
	}

	if !reflect.DeepEqual(kinds(got), want) {
		t.Errorf("interleaved pairs were not reunited:\n got %v\nwant %v", kinds(got), want)
	}
}

// Trimming can take either end of a pair, and both leave a request a provider
// will not accept.
func TestOrganizeDropsOrphans(t *testing.T) {
	tests := []struct {
		name     string
		messages []Message
		want     []string
	}{
		{
			name: "a call whose result was trimmed away",
			messages: []Message{
				{Type: TypeUser, Text: "go"},
				request("call_1", "shell", "{}"),
			},
			want: []string{"user/go"},
		},
		{
			name: "a result whose call was trimmed away",
			messages: []Message{
				response("call_1", "shell", "{}", "ok"),
				{Type: TypeBot, Text: "done"},
			},
			want: []string{"bot/done"},
		},
		{
			name: "one good pair and one orphan",
			messages: []Message{
				request("call_1", "shell", "{}"),
				response("call_1", "shell", "{}", "ok"),
				request("call_2", "shell", "{}"),
			},
			want: []string{"activity/request/call_1", "activity/response/call_1"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := kinds(Organize(test.messages)); !reflect.DeepEqual(got, test.want) {
				t.Errorf("got %v, want %v", got, test.want)
			}
		})
	}
}

// Two identical calls in one turn are only distinguishable by id, which is why
// the pairing prefers it over the arguments.
func TestOrganizePairsIdenticalCallsByID(t *testing.T) {
	got := Organize([]Message{
		request("call_1", "read", `{"path":"a"}`),
		request("call_2", "read", `{"path":"a"}`),
		response("call_2", "read", `{"path":"a"}`, "second"),
		response("call_1", "read", `{"path":"a"}`, "first"),
	})

	if len(got) != 4 {
		t.Fatalf("both calls must keep their own result: %v", kinds(got))
	}

	if got[1].Text != "first" || got[3].Text != "second" {
		t.Errorf("results were paired with the wrong calls: %q then %q", got[1].Text, got[3].Text)
	}
}

// A history rebuilt from somewhere that did not keep call ids still has to
// pair up, which is what the TypeScript engine matched on.
func TestOrganizePairsWithoutIDs(t *testing.T) {
	got := Organize([]Message{
		request("", "shell", `{"command":"ls"}`),
		{Type: TypeReasoning, Text: "let me look"},
		response("", "shell", `{"command":"ls"}`, "a\nb"),
	})

	want := []string{"activity/request/", "activity/response/", "reasoning/let me look"}

	if !reflect.DeepEqual(kinds(got), want) {
		t.Errorf("got %v, want %v", kinds(got), want)
	}
}

func TestOrganizeDoesNotPairDifferentCalls(t *testing.T) {
	got := Organize([]Message{
		request("", "read", `{"path":"a"}`),
		response("", "read", `{"path":"b"}`, "b contents"),
	})

	if len(got) != 0 {
		t.Errorf("a result for a different call is not a partner: %v", kinds(got))
	}
}

// Two calls cannot pair with each other, nor two results.
func TestOrganizeRequiresOneOfEach(t *testing.T) {
	got := Organize([]Message{
		request("call_1", "shell", "{}"),
		request("call_1", "shell", "{}"),
	})

	if len(got) != 0 {
		t.Errorf("two calls do not make a pair: %v", kinds(got))
	}

	got = Organize([]Message{
		response("call_1", "shell", "{}", "ok"),
		response("call_1", "shell", "{}", "ok"),
	})

	if len(got) != 0 {
		t.Errorf("two results do not make a pair: %v", kinds(got))
	}
}

// A trigger says "act now". Anywhere but last it describes a moment that has
// already passed.
func TestOrganizeKeepsATriggerOnlyWhenItIsLast(t *testing.T) {
	got := Organize([]Message{
		{Type: TypeUser, Text: "go"},
		trigger("wake"),
	})

	if want := []string{"user/go", "activity/trigger/"}; !reflect.DeepEqual(kinds(got), want) {
		t.Errorf("got %v, want %v", kinds(got), want)
	}

	got = Organize([]Message{
		trigger("wake"),
		{Type: TypeUser, Text: "go"},
	})

	if want := []string{"user/go"}; !reflect.DeepEqual(kinds(got), want) {
		t.Errorf("a stranded trigger must be dropped: %v", kinds(got))
	}
}

// The system prompt is bookkeeping rather than conversation, so a trigger
// followed only by instructions is still last.
func TestOrganizeTriggerIgnoresTrailingInstructions(t *testing.T) {
	got := Organize([]Message{
		{Type: TypeUser, Text: "go"},
		trigger("wake"),
		{Type: TypeInstructions, Text: "you are a coding agent"},
	})

	want := []string{"user/go", "activity/trigger/", "instructions/you are a coding agent"}

	if !reflect.DeepEqual(kinds(got), want) {
		t.Errorf("got %v, want %v", kinds(got), want)
	}
}

// An activity message with nothing in its meta describes no call at all - it
// cannot be paired, rendered or acted on.
func TestOrganizeDropsMalformedActivities(t *testing.T) {
	tests := []struct {
		name    string
		message Message
	}{
		{name: "no activity at all", message: Message{Type: TypeActivity}},
		{name: "an empty activity", message: Message{Type: TypeActivity, Activity: &Activity{}}},
		{
			name:    "no kind",
			message: Message{Type: TypeActivity, Activity: &Activity{ID: "x", Name: "shell"}},
		},
		{
			name:    "a kind nobody recognises",
			message: Message{Type: TypeActivity, Activity: &Activity{Kind: "somethingelse", ID: "x"}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := Organize([]Message{{Type: TypeUser, Text: "go"}, test.message})

			if want := []string{"user/go"}; !reflect.DeepEqual(kinds(got), want) {
				t.Errorf("got %v, want %v", kinds(got), want)
			}
		})
	}
}

func TestOrganizeDropsEmptyMessages(t *testing.T) {
	got := Organize([]Message{
		{Type: TypeUser, Text: "go"},
		{Type: TypeBot, Text: ""},
		{Type: TypeReasoning, Text: ""},
		{Type: TypeBot, Text: "done"},
	})

	want := []string{"user/go", "bot/done"}

	if !reflect.DeepEqual(kinds(got), want) {
		t.Errorf("got %v, want %v", kinds(got), want)
	}
}

// An empty system prompt is a configuration choice rather than an accident, and
// dropping it would change which message comes first.
func TestOrganizeKeepsAnEmptyInstructions(t *testing.T) {
	got := Organize([]Message{{Type: TypeInstructions, Text: ""}, {Type: TypeUser, Text: "go"}})

	if len(got) != 2 || got[0].Type != TypeInstructions {
		t.Errorf("the instructions must survive: %v", kinds(got))
	}
}

// A retried turn or a re-injected notice can land twice. Repetition is also
// what the model imitates.
func TestOrganizeCollapsesConsecutiveDuplicates(t *testing.T) {
	got := Organize([]Message{
		{Type: TypeUser, Text: "go"},
		{Type: TypeUser, Text: "go"},
		{Type: TypeBot, Text: "ok"},
		{Type: TypeUser, Text: "go"},
	})

	want := []string{"user/go", "bot/ok", "user/go"}

	if !reflect.DeepEqual(kinds(got), want) {
		t.Errorf("only consecutive repeats collapse:\n got %v\nwant %v", kinds(got), want)
	}
}

// Two calls with the same arguments are two real calls the model made.
// Collapsing them would hide the repetition the cycle guards exist to catch.
func TestOrganizeDoesNotCollapseRepeatedToolCalls(t *testing.T) {
	got := Organize([]Message{
		request("call_1", "read", `{"path":"a"}`),
		response("call_1", "read", `{"path":"a"}`, "contents"),
		request("call_2", "read", `{"path":"a"}`),
		response("call_2", "read", `{"path":"a"}`, "contents"),
	})

	if len(got) != 4 {
		t.Errorf("repeated calls must stay visible: %v", kinds(got))
	}
}

func TestOrganizeHandlesAnEmptyConversation(t *testing.T) {
	if got := Organize(nil); len(got) != 0 {
		t.Errorf("got %v, want nothing", got)
	}

	if got := Organize([]Message{}); len(got) != 0 {
		t.Errorf("got %v, want nothing", got)
	}
}

// Organize is used on the way to the provider; the engine's own history is the
// record of what happened and must not be rewritten under it.
func TestOrganizeDoesNotMutateItsInput(t *testing.T) {
	messages := []Message{
		request("call_1", "shell", "{}"),
		{Type: TypeBot, Text: "thinking"},
		response("call_1", "shell", "{}", "ok"),
	}

	before := kinds(messages)

	Organize(messages)

	if after := kinds(messages); !reflect.DeepEqual(before, after) {
		t.Errorf("the input was rewritten:\n before %v\n after  %v", before, after)
	}
}

// The whole point, end to end: a history that trimming and interleaving have
// mangled still renders into something a provider accepts.
func TestOrganizeRepairsAHistoryOnTheWire(t *testing.T) {
	chat := toChatMessages([]Message{
		{Type: TypeInstructions, Text: "you are a coding agent"},
		// this result's call fell outside the compaction window
		response("gone", "read", "{}", "old contents"),
		{Type: TypeUser, Text: "run the tests"},
		request("call_1", "shell", `{"command":"go test"}`),
		{Type: TypeReasoning, Text: "waiting on it"},
		response("call_1", "shell", `{"command":"go test"}`, "ok"),
		// and this call never got an answer before the run was interrupted
		request("call_2", "shell", `{"command":"go vet"}`),
	})

	var roles []string

	for _, message := range chat {
		roles = append(roles, message.Role)
	}

	want := []string{"system", "user", "assistant", "tool"}

	if !reflect.DeepEqual(roles, want) {
		t.Errorf("wire roles = %v, want %v", roles, want)
	}

	if len(chat[2].ToolCalls) != 1 || chat[3].ToolCallID != chat[2].ToolCalls[0].ID {
		t.Errorf("the surviving pair must reference the same call id: %+v", chat[2:])
	}
}
