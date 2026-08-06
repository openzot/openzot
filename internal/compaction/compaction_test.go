package compaction

import (
	"strings"
	"testing"
)

// Token accounting is the number every budgeting decision rests on, and the
// failure is asymmetric: an over-estimate costs one early compaction, an
// under-estimate gets the request rejected mid-run. The TypeScript engine's
// estimator tests assert exactly one property - the estimate must come in above
// what the provider then charges - and these are the same property, applied to
// the message shapes zot actually produces.

const testModel = "gpt-5.4"

func TestEveryMessageTypeIsPriced(t *testing.T) {
	tests := []struct {
		name    string
		message Message
	}{
		{name: "user", message: Message{Type: TypeUser, Text: "run the tests"}},
		{name: "bot", message: Message{Type: TypeBot, Text: "they pass"}},
		{name: "instructions", message: Message{Type: TypeInstructions, Text: "you are a coding agent"}},
		{name: "context", message: Message{Type: TypeContext, Text: "a summary of earlier work"}},
		{name: "reasoning", message: Message{Type: TypeReasoning, Text: "let me look at the failing case"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := CountMessageTokens(testModel, test.message); got <= 0 {
				t.Errorf("CountMessageTokens = %d, want a positive cost", got)
			}
		})
	}
}

// The case that matters most and is easiest to miss: a tool call carries no
// text at all. Counting text alone prices a write of a whole file the same as
// an empty message.
func TestAToolCallCostsWhatItCarries(t *testing.T) {
	payload := strings.Repeat("some source code that is being written to a file\n", 40)

	call := Message{
		Type:    TypeActivity,
		Payload: `{"kind":"request","id":"call_1","name":"write","arguments":"{\"path\":\"main.go\",\"content\":\"` + payload + `\"}"}`,
	}

	empty := Message{
		Type:    TypeActivity,
		Payload: `{"kind":"request","id":"call_2","name":"write","arguments":"{}"}`,
	}

	priced := CountMessageTokens(testModel, call)
	baseline := CountMessageTokens(testModel, empty)

	if priced <= baseline {
		t.Fatalf("a large tool call priced at %d, an empty one at %d", priced, baseline)
	}

	// it has to be in the right ballpark, not merely non-zero: the whole point
	// is that the estimate tracks what the provider will charge
	if priced < 200 {
		t.Errorf("a %d-character payload priced at %d tokens", len(payload), priced)
	}
}

// The engine writes a response's result into both the text and the metadata.
// Counting both would double the largest payloads in a run.
func TestAToolResultIsNotCountedTwice(t *testing.T) {
	result := strings.Repeat("output line\n", 50)

	response := Message{Type: TypeActivity, Text: result}

	plain := Message{Type: TypeBot, Text: result}

	priced := CountMessageTokens(testModel, response)
	once := CountMessageTokens(testModel, plain)

	if priced != once {
		t.Errorf("a result already in the text must be priced once: %d against %d", priced, once)
	}
}

// A result that never made it into the text still has to be priced, which is
// the shape a history loaded from a log can have.
func TestAResultOnlyInThePayloadIsStillPriced(t *testing.T) {
	result := strings.Repeat("output line\n", 50)

	message := Message{Type: TypeActivity, Payload: result}

	if got := CountMessageTokens(testModel, message); got < 50 {
		t.Errorf("CountMessageTokens = %d for a %d-character result", got, len(result))
	}
}

// A handler can return anything. It reaches the model as JSON, so that is what
// it must be priced as.
func TestAStructuredResultIsPricedAsItIsRendered(t *testing.T) {
	message := Message{
		Type:    TypeActivity,
		Payload: `{"kind":"response","id":"call_1","name":"list","result":{"count":3,"entries":["alpha.go","beta.go","gamma.go"]}}`,
	}

	if got := CountMessageTokens(testModel, message); got < 10 {
		t.Errorf("CountMessageTokens = %d, want the rendered JSON priced", got)
	}
}

// A message with no payload is priced on its text alone.
func TestNoPayloadIsPricedOnTextAlone(t *testing.T) {
	message := Message{Type: TypeBot, Text: "hello"}

	withPayload := Message{Type: TypeBot, Text: "hello", Payload: "some extra detail"}

	if CountMessageTokens(testModel, withPayload) <= CountMessageTokens(testModel, message) {
		t.Error("a payload must add to the cost")
	}
}

// An empty message still costs the wire envelope. Pricing it at nothing is how
// a long thread of short turns quietly overruns its window.
func TestAnEmptyMessageStillCostsItsEnvelope(t *testing.T) {
	if got := CountMessageTokens(testModel, Message{Type: TypeUser}); got <= 0 {
		t.Errorf("CountMessageTokens = %d, want the envelope priced", got)
	}
}

func TestCountingIsAdditive(t *testing.T) {
	messages := []Message{
		{Type: TypeUser, Text: "run the tests"},
		{Type: TypeBot, Text: "they pass"},
	}

	total := CountMessagesTokens(testModel, messages)

	var summed int

	for _, message := range messages {
		summed += CountMessageTokens(testModel, message)
	}

	if total != summed {
		t.Errorf("CountMessagesTokens = %d, want %d", total, summed)
	}

	if got := CountMessagesTokens(testModel, nil); got != 0 {
		t.Errorf("an empty conversation costs %d, want 0", got)
	}
}

// More text is never fewer tokens. The estimator is compared against a budget,
// so a non-monotonic count would make compaction oscillate.
func TestCountingIsMonotonic(t *testing.T) {
	previous := 0

	for length := 0; length < 500; length += 50 {
		message := Message{Type: TypeUser, Text: strings.Repeat("word ", length)}

		got := CountMessageTokens(testModel, message)

		if got < previous {
			t.Fatalf("%d words priced at %d, fewer than the %d before it", length, got, previous)
		}

		previous = got
	}
}

// An unknown model must still be priced, or an unrecognised name would make
// every conversation look free and compaction would never fire.
func TestAnUnknownModelIsStillPriced(t *testing.T) {
	message := Message{Type: TypeUser, Text: "run the tests"}

	if got := CountMessageTokens("some-model-nobody-has-heard-of", message); got <= 0 {
		t.Errorf("CountMessageTokens = %d, want a positive cost", got)
	}
}
