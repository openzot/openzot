package tokenizer

import (
	"strings"
	"testing"
)

func TestCountMatchesKnownTokenizations(t *testing.T) {
	// counts verified against the published vocabularies; they are the point of
	// the package, so a change here is a real behavioural change
	tests := []struct {
		name  string
		model string
		text  string
		want  int
	}{
		{"a single word", "gpt-5.4-mini", "hello", 1},
		{"a short sentence", "gpt-5.4-mini", "Hello world, this is a test of the tokenizer.", 11},
		{"empty", "gpt-5.4-mini", "", 0},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := Count(test.model, test.text); got != test.want {
				t.Errorf("Count = %d, want %d", got, test.want)
			}
		})
	}
}

// The case the character heuristic gets badly wrong, and the reason for the
// change: under-counting produces a request the provider rejects.
func TestCountBeatsTheHeuristicOnNonEnglish(t *testing.T) {
	text := "４日 동안 비가 내렸다. 안녕하세요 여러분"

	actual := Count("gpt-5.4-mini", text)
	estimated := Estimate(text)

	if actual <= 0 {
		t.Fatalf("Count = %d, want a real count", actual)
	}

	if estimated >= actual {
		t.Errorf("the heuristic (%d) should under-count this text against the real %d - "+
			"if it no longer does, this test has lost its point", estimated, actual)
	}
}

// Code and JSON are dense in punctuation, which tokenizes differently from
// prose. The engine spends most of its budget on exactly this.
func TestCountHandlesCodeAndJSON(t *testing.T) {
	samples := []string{
		`func main() { fmt.Println("hello") }`,
		`{"tool_calls":[{"index":0,"function":{"name":"shell","arguments":"{\"cmd\":\"ls -la\"}"}}]}`,
		strings.Repeat("a", 500),
	}

	for _, sample := range samples {
		if got := Count("gpt-5.4-mini", sample); got <= 0 {
			t.Errorf("Count(%.30q) = %d, want a positive count", sample, got)
		}
	}
}

// Counting must be monotonic, or trimming a message could paradoxically make a
// thread larger and the builder would loop.
func TestCountIsMonotonic(t *testing.T) {
	base := "The quick brown fox jumps over the lazy dog. "

	previous := 0

	for repeat := 1; repeat <= 20; repeat++ {
		got := Count("gpt-5.4-mini", strings.Repeat(base, repeat))

		if got <= previous {
			t.Fatalf("%d repeats counted %d, not more than %d", repeat, got, previous)
		}

		previous = got
	}
}

func TestEncodingSelection(t *testing.T) {
	tests := map[string]Encoding{
		"gpt-5.4-mini":  O200kBase,
		"gpt-4o":        O200kBase,
		"gpt-4o-mini":   O200kBase,
		"gpt-4":         Cl100kBase,
		"gpt-4-turbo":   Cl100kBase,
		"gpt-3.5-turbo": Cl100kBase,

		// everything else approximates with the current vocabulary
		"glm-5.2":         DefaultEncoding,
		"claude-5-sonnet": DefaultEncoding,
		"who-knows":       DefaultEncoding,
	}

	for model, want := range tests {
		if got := EncodingFor(model); got != want {
			t.Errorf("EncodingFor(%q) = %q, want %q", model, got, want)
		}
	}
}

// Both vocabularies must actually load - a missing one would silently fall back
// to the heuristic for a whole family of models.
// Both vocabularies have to be genuinely usable and genuinely different. A
// codec that loaded but produced the other one's counts would make every
// budgeting decision for GPT-4 wrong in a way nothing else would catch.
func TestBothEncodingsLoadAndDiffer(t *testing.T) {
	counts := map[Encoding]int{}

	// text the two vocabularies segment differently
	const text = "The quick brown fox 你好世界 func main() { return nil }"

	for _, encoding := range []Encoding{O200kBase, Cl100kBase} {
		codec, err := encoder(encoding)
		if err != nil {
			t.Fatalf("encoder(%q): %v", encoding, err)
		}

		ids, _, err := codec.Encode(text)
		if err != nil {
			t.Fatalf("encode with %q: %v", encoding, err)
		}

		if len(ids) == 0 {
			t.Fatalf("%q produced no tokens", encoding)
		}

		counts[encoding] = len(ids)
	}

	if counts[O200kBase] == counts[Cl100kBase] {
		t.Errorf("both vocabularies counted %d tokens; they should segment this text differently",
			counts[O200kBase])
	}

	// and the cache hands back a working codec rather than a stale entry
	if second, err := encoder(O200kBase); err != nil || second == nil {
		t.Errorf("the cached codec is unusable: %v", err)
	}
}

// The wire format costs tokens no message text accounts for. Ignoring it
// under-counts a long thread by exactly the margin that decides whether it fits.
func TestMessageOverheadIsIncluded(t *testing.T) {
	text := "a short message"

	bare := Count("gpt-5.4-mini", text)
	withOverhead := CountMessage("gpt-5.4-mini", text)

	if withOverhead != bare+MessageOverhead {
		t.Errorf("CountMessage = %d, want %d + %d", withOverhead, bare, MessageOverhead)
	}

	// two hundred short messages is where it stops being a rounding error
	if MessageOverhead*200 < 500 {
		t.Error("the overhead constant looks too small to be worth including")
	}
}

func TestEstimateNeverReturnsZeroForRealText(t *testing.T) {
	// a message that costs nothing would let an unbounded number of them into a
	// thread
	for _, text := range []string{"a", "ab", "abc", "hello"} {
		if got := Estimate(text); got < 1 {
			t.Errorf("Estimate(%q) = %d, want at least 1", text, got)
		}
	}

	if got := Estimate(""); got != 0 {
		t.Errorf("Estimate(\"\") = %d, want 0", got)
	}
}

func BenchmarkCount(b *testing.B) {
	text := strings.Repeat("The quick brown fox jumps over the lazy dog. ", 100)

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		Count("gpt-5.4-mini", text)
	}
}

// The TypeScript engine and zot budget against the same providers, so their
// estimates have to agree. These pin the places where they could quietly drift.

// The envelope is what makes a long thread of short turns cost more than its
// text, and getting it wrong is invisible until a request is rejected. A
// conversation of two hundred one-word messages is charged two thousand tokens
// of wire format on top of its two hundred words - that is the margin between
// fitting and not, and it is the number the reference implementation charges.
func TestALongThreadIsPricedAboveItsText(t *testing.T) {
	const messages = 200

	var (
		envelopes int
		text      int
	)

	for i := 0; i < messages; i++ {
		envelopes += CountMessage("gpt-5.4", "word")
		text += Count("gpt-5.4", "word")
	}

	overhead := envelopes - text

	if overhead != messages*MessageOverhead {
		t.Errorf("%d messages carried %d tokens of envelope, want %d",
			messages, overhead, messages*MessageOverhead)
	}

	// the reference implementation charges 4 for the message, 1 for a name, 3
	// for the reply priming and 2 standing in for the rendered name
	if overhead != messages*10 {
		t.Errorf("%d messages carried %d tokens of envelope; the TypeScript engine charges %d",
			messages, overhead, messages*10)
	}
}

// Text an agent reads off disk can contain anything, including the tokenizer's
// own control sequences. The reference implementation passes
// `allowedSpecial: 'all'` so those do not throw; zot has to survive them too,
// and must not silently fall back to the character heuristic when it meets one.
func TestSpecialTokensInInputAreCountedNotFatal(t *testing.T) {
	tests := []string{
		"<|im_end|>",
		"<|endoftext|>",
		"<|endofprompt|>",
		"a file that happens to contain <|im_start|>system somewhere in it",
	}

	for _, text := range tests {
		t.Run(text, func(t *testing.T) {
			got := Count("gpt-5.4", text)

			if got <= 0 {
				t.Fatalf("Count = %d, want a real count", got)
			}

			// the fallback would be the character heuristic; a real encode of a
			// control sequence is several tokens, so a count that happens to
			// equal the heuristic exactly is worth knowing about
			if got == Estimate(text) && len(text) > 20 {
				t.Errorf("Count = %d, exactly the heuristic - the encoder may have bailed", got)
			}
		})
	}
}

// A control sequence must not be counted as one cheap token either: zot never
// sends them deliberately, so if one appears it is literal text the provider
// will charge for in full.
func TestASpecialTokenIsNotCountedAsOne(t *testing.T) {
	if got := Count("gpt-5.4", "<|im_end|>"); got < 2 {
		t.Errorf("Count = %d, want it priced as the literal text it is", got)
	}
}

// The envelope applies whatever the message says, including nothing at all -
// that is the point of charging for it separately.
func TestTheEnvelopeIsChargedOnEveryMessage(t *testing.T) {
	if got := CountMessage("gpt-5.4", ""); got != MessageOverhead {
		t.Errorf("CountMessage(\"\") = %d, want %d", got, MessageOverhead)
	}

	text := "run the tests"

	if got, want := CountMessage("gpt-5.4", text), Count("gpt-5.4", text)+MessageOverhead; got != want {
		t.Errorf("CountMessage = %d, want %d", got, want)
	}
}
