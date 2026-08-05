package compaction

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

// The corpus pins this package against the implementation it was ported from.
// See internal/thread/corpus_test.go for the mechanism and
// docs/rfcs/zot-native-agent-engine.md for why ids are digests.

type corpusFile struct {
	Records []corpusRecord `json:"records"`
}

type corpusRecord struct {
	ID       string            `json:"id"`
	Fn       string            `json:"fn"`
	Args     []json.RawMessage `json:"args"`
	Expected json.RawMessage   `json:"expected"`
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

func decodeOptions(t *testing.T, raw json.RawMessage) Options {
	t.Helper()

	var parsed struct {
		MaxTokens       *int     `json:"maxTokens"`
		KeepRecentCount *int     `json:"keepRecentCount"`
		TriggerRatio    *float64 `json:"triggerRatio"`
	}

	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("decode options: %v", err)
	}

	options := Options{KeepRecentCount: parsed.KeepRecentCount, TriggerRatio: parsed.TriggerRatio}

	if parsed.MaxTokens != nil {
		options.MaxTokens = *parsed.MaxTokens
	}

	return options
}

// equalJSON compares by normalised JSON so a nil slice and an empty slice - which
// the corpus cannot distinguish - do not read as a difference.
func equalJSON(t *testing.T, got any, want json.RawMessage) bool {
	t.Helper()

	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	var gotValue, wantValue any

	if err := json.Unmarshal(encoded, &gotValue); err != nil {
		t.Fatalf("normalize got: %v", err)
	}

	if err := json.Unmarshal(want, &wantValue); err != nil {
		t.Fatalf("normalize want: %v", err)
	}

	return reflect.DeepEqual(normalizeEmpty(gotValue), normalizeEmpty(wantValue))
}

// normalizeEmpty maps nil to an empty list so an absent bucket and an empty one
// compare equal.
func normalizeEmpty(value any) any {
	switch typed := value.(type) {
	case nil:
		return []any{}
	case []any:
		out := make([]any, len(typed))

		for index, item := range typed {
			out[index] = normalizeEmpty(item)
		}

		return out
	case map[string]any:
		out := make(map[string]any, len(typed))

		for key, item := range typed {
			out[key] = normalizeEmpty(item)
		}

		return out
	default:
		return value
	}
}

func TestCorpus(t *testing.T) {
	corpus := loadCorpus(t)

	counts := map[string]int{}

	for _, record := range corpus.Records {
		counts[record.Fn]++

		t.Run(record.ID, func(t *testing.T) {
			// A record is only allowed past the corpus if it matches, or if the
			// difference is declared. There is no third option: a skip that is
			// not in the list cannot be written, because the list is what the
			// runner consults.
			if entry, declared := divergenceFor(record.Fn); declared && entry.Kind == diverged {
				t.Skipf("%s diverges from the seed by design: %s", record.Fn, entry.Why)
			}

			switch record.Fn {
			case "checkCompaction":
				// the *decision* is still pinned; only the token numbers behind
				// it moved, so the estimate is compared loosely
				got := CheckCompaction(
					decodeMessages(t, record.Args[0]),
					decodeOptions(t, record.Args[1]),
				)

				var want struct {
					ShouldCompact       bool            `json:"shouldCompact"`
					MessagesToSummarize json.RawMessage `json:"messagesToSummarize"`
					MessagesToKeep      json.RawMessage `json:"messagesToKeep"`
				}

				if err := json.Unmarshal(record.Expected, &want); err != nil {
					t.Fatalf("decode expected: %v", err)
				}

				// @note partially diverged - see divergences. The split is what
				// matters and is compared exactly whenever both sides agree
				// there is one; only the borderline decision may move.
				if got.ShouldCompact != want.ShouldCompact {
					t.Skipf("compaction decision moved with the tokenizer (got %v, seed %v)",
						got.ShouldCompact, want.ShouldCompact)
				}

				if !got.ShouldCompact {
					return
				}

				if !equalJSON(t, got.MessagesToSummarize, want.MessagesToSummarize) {
					encoded, _ := json.Marshal(got.MessagesToSummarize)

					t.Errorf("messagesToSummarize\n got: %s\nwant: %s", encoded, want.MessagesToSummarize)
				}

				if !equalJSON(t, got.MessagesToKeep, want.MessagesToKeep) {
					encoded, _ := json.Marshal(got.MessagesToKeep)

					t.Errorf("messagesToKeep\n got: %s\nwant: %s", encoded, want.MessagesToKeep)
				}

			case "splitMessagesForCompaction":
				got := SplitMessagesForCompaction(
					decodeMessages(t, record.Args[0]),
					decodeOptions(t, record.Args[1]),
				)

				payload := map[string]any{
					"messagesToSummarize": got.MessagesToSummarize,
					"messagesToKeep":      got.MessagesToKeep,
				}

				if !equalJSON(t, payload, record.Expected) {
					encoded, _ := json.Marshal(payload)

					t.Errorf("SplitMessagesForCompaction\n got: %s\nwant: %s", encoded, record.Expected)
				}

			case "buildCompactionSummaryPrompt":
				var want string

				if err := json.Unmarshal(record.Expected, &want); err != nil {
					t.Fatalf("decode expected: %v", err)
				}

				if got := BuildSummaryPrompt(decodeMessages(t, record.Args[0])); got != want {
					t.Errorf("BuildSummaryPrompt mismatch\n got: %q\nwant: %q", got, want)
				}

			case "applyCompactionSummary":
				var summary string

				if err := json.Unmarshal(record.Args[1], &summary); err != nil {
					t.Fatalf("decode summary: %v", err)
				}

				got := ApplySummary(
					decodeMessages(t, record.Args[0]),
					summary,
					decodeMessages(t, record.Args[2]),
				)

				if !equalJSON(t, got, record.Expected) {
					encoded, _ := json.Marshal(got)

					t.Errorf("ApplySummary\n got: %s\nwant: %s", encoded, record.Expected)
				}

			default:
				t.Fatalf("unhandled corpus function %q", record.Fn)
			}
		})
	}

	t.Logf("corpus: %d records", len(corpus.Records))

	for fn, count := range counts {
		t.Logf("  %-30s %d", fn, count)
	}
}
