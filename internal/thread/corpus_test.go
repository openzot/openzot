package thread

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"reflect"
	"testing"
)

// The corpus pins this package's behaviour against the implementation it was
// ported from. Each record is one call: a function, its arguments and the value
// it must return.
//
// Ids are digests rather than names on purpose - the corpus is published and the
// suite it came from is not. To trace a failing id, look it up in the private
// provenance map. See docs/rfcs/zot-native-agent-engine.md.

type corpusFile struct {
	Records []corpusRecord `json:"records"`
}

type corpusRecord struct {
	ID       string            `json:"id"`
	Fn       string            `json:"fn"`
	Args     []json.RawMessage `json:"args"`
	Expected json.RawMessage   `json:"expected"`

	// createRepetitionGuard records are a session rather than a single call
	Pushes    []string `json:"pushes"`
	TrippedAt *int     `json:"trippedAt"`

	// buildThread records carry the behaviour of the callbacks they were given
	Callbacks []corpusCallback `json:"callbacks"`
}

type corpusCallback struct {
	Kind   string            `json:"kind"`
	Args   []json.RawMessage `json:"args"`
	Result json.RawMessage   `json:"result"`
}

func loadCorpus(t *testing.T) corpusFile {
	t.Helper()

	raw, err := os.ReadFile("testdata/corpus.json")
	if err != nil {
		t.Fatalf("read corpus: %v", err)
	}

	var corpus corpusFile

	if err := json.Unmarshal(raw, &corpus); err != nil {
		t.Fatalf("parse corpus: %v", err)
	}

	if len(corpus.Records) == 0 {
		t.Fatal("corpus is empty")
	}

	return corpus
}

func decodeMessages(t *testing.T, raw json.RawMessage) []Message {
	t.Helper()

	var messages []Message

	if err := json.Unmarshal(raw, &messages); err != nil {
		t.Fatalf("decode messages: %v", err)
	}

	return messages
}

// decodeCycleOptions reads the optional second argument of a cycle heuristic.
func decodeCycleOptions(t *testing.T, args []json.RawMessage) CycleOptions {
	t.Helper()

	if len(args) < 2 {
		return CycleOptions{}
	}

	var raw struct {
		MinRepetitions       *int `json:"minRepetitions"`
		MinPatternLength     *int `json:"minPatternLength"`
		MaxPatternLength     *int `json:"maxPatternLength"`
		MinResultRepetitions *int `json:"minResultRepetitions"`
	}

	if err := json.Unmarshal(args[1], &raw); err != nil {
		return CycleOptions{}
	}

	return CycleOptions{
		MinRepetitions:       raw.MinRepetitions,
		MinPatternLength:     raw.MinPatternLength,
		MaxPatternLength:     raw.MaxPatternLength,
		MinResultRepetitions: raw.MinResultRepetitions,
	}
}

func expectBool(t *testing.T, record corpusRecord) bool {
	t.Helper()

	var expected bool

	if err := json.Unmarshal(record.Expected, &expected); err != nil {
		t.Fatalf("%s: expected a boolean: %v", record.ID, err)
	}

	return expected
}

// TestCorpus runs every seeded case.
func TestCorpus(t *testing.T) {
	corpus := loadCorpus(t)

	counts := map[string]int{}

	for _, record := range corpus.Records {
		counts[record.Fn]++

		t.Run(record.ID, func(t *testing.T) {
			switch record.Fn {
			case "hasRepeatedSuffix":
				got := HasRepeatedSuffix(
					decodeMessages(t, record.Args[0]),
					decodeCycleOptions(t, record.Args),
				)

				if want := expectBool(t, record); got != want {
					t.Errorf("HasRepeatedSuffix = %v, want %v", got, want)
				}

			case "hasRepeatedActivityTail":
				got := HasRepeatedActivityTail(
					decodeMessages(t, record.Args[0]),
					decodeCycleOptions(t, record.Args),
				)

				if want := expectBool(t, record); got != want {
					t.Errorf("HasRepeatedActivityTail = %v, want %v", got, want)
				}

			case "hasRepeatedResultRun":
				got := HasRepeatedResultRun(
					decodeMessages(t, record.Args[0]),
					decodeCycleOptions(t, record.Args),
				)

				if want := expectBool(t, record); got != want {
					t.Errorf("HasRepeatedResultRun = %v, want %v", got, want)
				}

			case "isThreadCyclic":
				got := IsThreadCyclic(
					decodeMessages(t, record.Args[0]),
					decodeCycleOptions(t, record.Args),
				)

				if want := expectBool(t, record); got != want {
					t.Errorf("IsThreadCyclic = %v, want %v", got, want)
				}

			case "describeThreadCycle":
				got := DescribeThreadCycle(
					decodeMessages(t, record.Args[0]),
					decodeCycleOptions(t, record.Args),
				)

				var want *string

				if err := json.Unmarshal(record.Expected, &want); err != nil {
					t.Fatalf("decode expected: %v", err)
				}

				if want == nil {
					if got != "" {
						t.Errorf("DescribeThreadCycle = %q, want no cycle", got)
					}
				} else if got != *want {
					t.Errorf("DescribeThreadCycle = %q, want %q", got, *want)
				}

			case "hasRepeatedTextRun":
				var text string

				if err := json.Unmarshal(record.Args[0], &text); err != nil {
					t.Fatalf("decode text: %v", err)
				}

				options := TextRunOptions{}

				if len(record.Args) > 1 {
					var raw struct {
						MinUnits       *int     `json:"minUnits"`
						Window         *int     `json:"window"`
						MaxUniqueRatio *float64 `json:"maxUniqueRatio"`
					}

					if err := json.Unmarshal(record.Args[1], &raw); err == nil {
						options.MinUnits = raw.MinUnits
						options.Window = raw.Window
						options.MaxUniqueRatio = raw.MaxUniqueRatio
					}
				}

				if got, want := HasRepeatedTextRun(text, options), expectBool(t, record); got != want {
					t.Errorf("HasRepeatedTextRun = %v, want %v", got, want)
				}

			case "createRepetitionGuard":
				runGuardRecord(t, record)

			case "buildThread":
				runBuildThreadRecord(t, record)

			default:
				t.Fatalf("unhandled corpus function %q", record.Fn)
			}
		})
	}

	t.Logf("corpus: %d records", len(corpus.Records))

	for fn, count := range counts {
		t.Logf("  %-26s %d", fn, count)
	}
}

func runGuardRecord(t *testing.T, record corpusRecord) {
	t.Helper()

	options := GuardOptions{}

	if len(record.Args) > 0 {
		var raw struct {
			Ngram          *int     `json:"ngram"`
			Window         *int     `json:"window"`
			MaxRepeats     *int     `json:"maxRepeats"`
			MaxUniqueRatio *float64 `json:"maxUniqueRatio"`
			MinChars       *int     `json:"minChars"`
		}

		if err := json.Unmarshal(record.Args[0], &raw); err == nil {
			options.Ngram = raw.Ngram
			options.Window = raw.Window
			options.MaxRepeats = raw.MaxRepeats
			options.MaxUniqueRatio = raw.MaxUniqueRatio
			options.MinChars = raw.MinChars
		}
	}

	guard := NewGuard(options)

	trippedAt := -1

	for index, chunk := range record.Pushes {
		if guard.Push(chunk) && trippedAt < 0 {
			trippedAt = index
		}
	}

	want := -1

	if record.TrippedAt != nil {
		want = *record.TrippedAt
	}

	if trippedAt != want {
		t.Fatalf("tripped at %d, want %d", trippedAt, want)
	}

	var expected *GuardReason

	if len(record.Expected) > 0 && string(record.Expected) != "null" {
		expected = &GuardReason{}

		if err := json.Unmarshal(record.Expected, expected); err != nil {
			t.Fatalf("decode reason: %v", err)
		}
	}

	got := guard.Reason()

	switch {
	case expected == nil && got != nil:
		t.Fatalf("reason = %+v, want none", *got)
	case expected == nil:
		return
	case got == nil:
		t.Fatalf("reason = none, want %+v", *expected)
	}

	if got.Phrase != expected.Phrase || got.Count != expected.Count || got.Text != expected.Text {
		t.Errorf("reason = %+v, want %+v", *got, *expected)
	}

	if !closeEnough(got.UniqueRatio, expected.UniqueRatio) {
		t.Errorf("uniqueRatio = %v, want %v", got.UniqueRatio, expected.UniqueRatio)
	}

	if !closeEnough(got.HapaxRatio, expected.HapaxRatio) {
		t.Errorf("hapaxRatio = %v, want %v", got.HapaxRatio, expected.HapaxRatio)
	}
}

func closeEnough(got, want float64) bool {
	return math.Abs(got-want) < 1e-9
}

func runBuildThreadRecord(t *testing.T, record corpusRecord) {
	t.Helper()

	var raw map[string]any

	if err := json.Unmarshal(record.Args[0], &raw); err != nil {
		t.Fatalf("decode options: %v", err)
	}

	options := BuildOptions{}

	if messages, ok := raw["messages"]; ok {
		encoded, err := json.Marshal(messages)
		if err != nil {
			t.Fatalf("re-encode messages: %v", err)
		}

		options.Messages = decodeMessages(t, encoded)
	}

	if maxTokens, ok := raw["maxTokens"].(float64); ok {
		options.MaxTokens = maxTokens
	}

	if minMessages, ok := raw["minMessages"].(float64); ok {
		options.MinMessages = int(minMessages)
	}

	// callbacks are replayed from their recorded behaviour, matched
	// structurally rather than by encoded key - Go and JavaScript do not agree
	// on object key order, so a string key would never line up

	replay := func(kind string, args ...any) (json.RawMessage, bool) {
		for _, callback := range record.Callbacks {
			if callback.Kind != kind || len(callback.Args) != len(args) {
				continue
			}

			matched := true

			for index, arg := range args {
				var recorded any

				if err := json.Unmarshal(callback.Args[index], &recorded); err != nil {
					matched = false

					break
				}

				encoded, err := json.Marshal(arg)
				if err != nil {
					matched = false

					break
				}

				var normalized any

				if err := json.Unmarshal(encoded, &normalized); err != nil {
					matched = false

					break
				}

				if !reflect.DeepEqual(recorded, normalized) {
					matched = false

					break
				}
			}

			if matched {
				return callback.Result, true
			}
		}

		return nil, false
	}

	if raw["tokenEstimationFunction"] == "@callback" {
		options.Estimate = func(message Message) (Usage, error) {
			result, ok := replay("tokenEstimation", message)
			if !ok {
				return Usage{}, fmt.Errorf("no recorded estimate for %v", message)
			}

			var usage Usage

			if err := json.Unmarshal(result, &usage); err != nil {
				return Usage{}, err
			}

			return usage, nil
		}
	}

	switch inclusive := raw["inclusive"].(type) {
	case string:
		if inclusive == "@callback" {
			options.Inclusive = func(message Message, trimTo float64) (Message, bool, error) {
				result, ok := replay("inclusive", message, trimTo)
				if !ok {
					return nil, false, fmt.Errorf("no recorded trim for %v", message)
				}

				// @note the trimmer signals "drop it after all" by returning
				// false rather than a message
				if string(result) == "false" || string(result) == "null" {
					return nil, false, nil
				}

				var trimmed Message

				if err := json.Unmarshal(result, &trimmed); err != nil {
					return nil, false, err
				}

				return trimmed, true, nil
			}
		}
	case bool:
		options.InclusiveAll = inclusive
	}

	got, err := BuildThread(options)
	if err != nil {
		t.Fatalf("BuildThread: %v", err)
	}

	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("encode result: %v", err)
	}

	var gotValue, wantValue any

	if err := json.Unmarshal(encoded, &gotValue); err != nil {
		t.Fatalf("normalize result: %v", err)
	}

	if err := json.Unmarshal(record.Expected, &wantValue); err != nil {
		t.Fatalf("decode expected: %v", err)
	}

	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Errorf("BuildThread\n got: %s\nwant: %s", encoded, record.Expected)
	}
}
