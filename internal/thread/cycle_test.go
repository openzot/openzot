package thread

import "testing"

// Cases the corpus cannot carry. See notPortableCases in divergence_test.go for
// which captured records these stand in for.
//
// Both concern values containing a reference cycle. JSON has no way to express
// one, so these could not be seeded - but the underlying question survives the
// port: a malformed payload from a provider must not be the thing that aborts a
// run. A cycle check that panics is worse than no cycle check.

// cyclicValue returns a map that contains itself, which json.Marshal rejects.
func cyclicValue() map[string]any {
	value := map[string]any{"kind": "cyclic"}

	value["self"] = value

	return value
}

func TestCycleCircularMeta(t *testing.T) {
	messages := []Message{
		{"type": "user", "text": "A", "meta": cyclicValue()},
		{"type": "bot", "text": "B"},
		{"type": "user", "text": "A", "meta": cyclicValue()},
		{"type": "bot", "text": "B"},
	}

	// must not panic, and must still reach a verdict

	got := HasRepeatedSuffix(messages, CycleOptions{})

	// @note both cyclic metas collapse to the same sentinel, so the two halves
	// fingerprint identically and the pair reads as a cycle. That is the
	// intended trade-off in safeStringify: a degraded comparison beats none.
	if !got {
		t.Errorf("HasRepeatedSuffix = false, want true (cyclic meta must degrade, not disable)")
	}
}

func TestRepeatedResultRunCircularResult(t *testing.T) {
	activity := func(result any) Message {
		return Message{
			"type": "activity",
			"text": "",
			"meta": map[string]any{
				"activity": map[string]any{
					"type": "response",
					"function": map[string]any{
						"name":      "search",
						"arguments": map[string]any{"q": "same"},
						"result":    result,
					},
				},
			},
		}
	}

	messages := []Message{
		activity(cyclicValue()),
		activity(cyclicValue()),
		activity(cyclicValue()),
	}

	if !HasRepeatedResultRun(messages, CycleOptions{}) {
		t.Error("HasRepeatedResultRun = false, want true (identical cyclic results are still a loop)")
	}

	// a genuinely different result must still break the run, even alongside a
	// cyclic one

	mixed := []Message{
		activity(cyclicValue()),
		activity(cyclicValue()),
		activity(map[string]any{"records": []any{"something"}}),
	}

	if HasRepeatedResultRun(mixed, CycleOptions{}) {
		t.Error("HasRepeatedResultRun = true, want false (a differing result breaks the run)")
	}
}

// TestDescribeAttributesTheHeuristic pins that attribution reports which check
// fired, not merely that one did.
func TestDescribeAttributesTheHeuristic(t *testing.T) {
	messages := []Message{
		{"type": "user", "text": "hello"},
		{"type": "bot", "text": "hi"},
		{"type": "user", "text": "hello"},
		{"type": "bot", "text": "hi"},
	}

	if got, want := DescribeThreadCycle(messages, CycleOptions{}), "repeated_suffix"; got != want {
		t.Errorf("DescribeThreadCycle = %q, want %q", got, want)
	}

	if got := DescribeThreadCycle(nil, CycleOptions{}); got != "" {
		t.Errorf("DescribeThreadCycle = %q, want empty for an empty thread", got)
	}
}
