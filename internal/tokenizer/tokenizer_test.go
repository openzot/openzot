package tokenizer

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestCountPricesRepresentativeInput(t *testing.T) {
	for _, text := range []string{
		"hello",
		"Hello world, this is ordinary prose.",
		`{"tool_calls":[{"name":"shell","arguments":{"command":"go test ./..."}}]}`,
		"４日 동안 비가 내렸다. 안녕하세요 여러분",
		strings.Repeat("identifier", 100),
	} {
		if got := Count("any-provider/model", text); got <= 0 {
			t.Errorf("Count(%.30q) = %d, want a positive estimate", text, got)
		}
	}

	if got := Count("any-provider/model", ""); got != 0 {
		t.Errorf("Count(empty) = %d, want 0", got)
	}
}

func TestNonASCIIInputIsPricedByUTF8Bytes(t *testing.T) {
	text := "你好世界 안녕하세요"
	got := Count("unknown-model", text)

	if runes := utf8.RuneCountInString(text); got < runes {
		t.Errorf("Count = %d, want at least %d for non-ASCII input", got, runes)
	}
}

func TestCountIsConservativeForDenseASCII(t *testing.T) {
	text := strings.Repeat("a", 120)
	minimum := (len(text) + BytesPerToken - 1) / BytesPerToken

	if got := Count("model-a", text); got <= minimum {
		t.Errorf("Count = %d, want safety margin above base estimate %d", got, minimum)
	}
}

func TestCountIsModelIndependent(t *testing.T) {
	const text = "func main() { return nil }"

	if a, b := Count("openai/gpt-5.4", text), Count("anthropic/claude-5", text); a != b {
		t.Errorf("model-independent estimates differ: %d != %d", a, b)
	}
}

func TestCountIsMonotonic(t *testing.T) {
	base := "The quick brown fox jumps over the lazy dog. "
	previous := 0

	for repeat := 1; repeat <= 20; repeat++ {
		got := Count("model", strings.Repeat(base, repeat))
		if got <= previous {
			t.Fatalf("%d repeats counted %d, not more than %d", repeat, got, previous)
		}
		previous = got
	}
}

func TestMessageOverheadIsIncluded(t *testing.T) {
	const text = "a short message"
	bare := Count("model", text)

	if got := CountMessage("model", text); got != bare+MessageOverhead {
		t.Errorf("CountMessage = %d, want %d + %d", got, bare, MessageOverhead)
	}
	if got := CountMessage("model", ""); got != MessageOverhead {
		t.Errorf("empty message = %d, want envelope cost %d", got, MessageOverhead)
	}
}

func TestLiteralControlSequencesRemainPriced(t *testing.T) {
	for _, text := range []string{"<|im_end|>", "<|endoftext|>", "<|endofprompt|>"} {
		if got := Count("model", text); got < 2 {
			t.Errorf("Count(%q) = %d, want literal text priced", text, got)
		}
	}
}

func BenchmarkCount(b *testing.B) {
	text := strings.Repeat("The quick brown fox jumps over the lazy dog. ", 100)
	for b.Loop() {
		Count("model", text)
	}
}
