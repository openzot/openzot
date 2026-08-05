// Package tokenizer counts tokens the way a model actually does.
//
// Every budgeting decision in the engine rests on this number: how much history
// fits in a request, when to compact, how much room is left for an answer. A
// character-count heuristic gets it wrong in both directions and the two
// failures are not symmetric - over-counting compacts early and wastes context,
// while under-counting produces a request the provider rejects outright, which
// costs a round trip and looks to the user like a failed run.
//
// The heuristic this replaces (characters ÷ 4, ×1.25 for safety) is roughly
// right for English prose and badly wrong for everything else. A CJK string of
// 23 characters is 11 tokens; the heuristic predicts 8. Code, JSON and long
// identifiers skew the other way.
//
// Encodings are compiled in, so counting works offline and adds no startup cost
// beyond the first use.
package tokenizer

import (
	"strings"
	"sync"

	"github.com/tiktoken-go/tokenizer"
)

// Encoding identifies a byte-pair vocabulary.
type Encoding string

const (
	// O200kBase is the current OpenAI vocabulary, used by GPT-4o onwards and
	// the reasoning models.
	O200kBase Encoding = "o200k_base"

	// Cl100kBase is the older OpenAI vocabulary, used by GPT-4 and GPT-3.5.
	Cl100kBase Encoding = "cl100k_base"
)

// DefaultEncoding is used for models the mapping does not recognise.
//
// o200k_base rather than cl100k_base because every recent model - OpenAI's and
// the open-weight families that copied its tokenizer design - is closer to it,
// and because it is the more conservative of the two for non-English text.
const DefaultEncoding = O200kBase

var (
	mu       sync.Mutex
	encoders = map[Encoding]tokenizer.Codec{}
)

// encoder returns a cached codec for an encoding.
//
// Building one is expensive enough to be worth caching and cheap enough not to
// warrant eager loading, so it is done once, lazily, per encoding.
func encoder(encoding Encoding) (tokenizer.Codec, error) {
	mu.Lock()

	defer mu.Unlock()

	if codec, ok := encoders[encoding]; ok {
		return codec, nil
	}

	name := tokenizer.O200kBase

	if encoding == Cl100kBase {
		name = tokenizer.Cl100kBase
	}

	codec, err := tokenizer.Get(name)
	if err != nil {
		return nil, err
	}

	encoders[encoding] = codec

	return codec, nil
}

// EncodingFor picks the vocabulary a model uses.
//
// Only OpenAI publishes its tokenizer, so for everyone else this is an
// approximation - but a far closer one than counting characters, because the
// open-weight families overwhelmingly use vocabularies of a similar size and
// segmentation. Where a model is genuinely unknown the default applies.
func EncodingFor(model string) Encoding {
	name := strings.ToLower(model)

	// the older OpenAI vocabulary, still used by the GPT-4 and GPT-3.5 families
	for _, prefix := range []string{"gpt-4-", "gpt-4o", "gpt-3.5", "gpt-4t"} {
		if strings.HasPrefix(name, prefix) {
			if strings.HasPrefix(name, "gpt-4o") {
				return O200kBase
			}

			return Cl100kBase
		}
	}

	if name == "gpt-4" {
		return Cl100kBase
	}

	return DefaultEncoding
}

// Count returns the number of tokens in a string, for the given model.
//
// A failure to build the encoder falls back to the character estimate rather
// than erroring: a token count that is approximately right always beats a run
// that will not start.
func Count(model, text string) int {
	if text == "" {
		return 0
	}

	codec, err := encoder(EncodingFor(model))
	if err != nil {
		return Estimate(text)
	}

	ids, _, err := codec.Encode(text)
	if err != nil {
		return Estimate(text)
	}

	return len(ids)
}

// CharsPerToken and SafetyMargin are the fallback heuristic's constants.
const (
	// CharsPerToken is the rough character-to-token ratio for English prose.
	CharsPerToken = 4

	// SafetyMargin is applied on top, because the heuristic is only ever used
	// when the real count is unavailable and under-counting is the expensive
	// direction.
	SafetyMargin = 1.25
)

// Estimate is the character heuristic, used only when the tokenizer cannot be
// built. Exported so the fallback is testable and its bias is visible.
func Estimate(text string) int {
	runes := len([]rune(text))

	estimate := float64(runes) / CharsPerToken * SafetyMargin

	if estimate > 0 && estimate < 1 {
		return 1
	}

	return int(estimate + 0.999999)
}

// The per-message costs of the chat wire format.
//
// Every message carries a role, delimiters and control tokens that never appear
// in its text. Ignoring them under-counts a long conversation by exactly the
// margin that matters - a thread of two hundred short messages is off by more
// than two thousand tokens, which is the difference between fitting and being
// rejected.
//
// These are the numbers the TypeScript engine uses, which are in turn OpenAI's
// own from the token-counting cookbook. Kept identical rather than re-derived:
// the two implementations budget against the same providers, and an estimate
// that drifts is one that eventually disagrees about whether a conversation
// fits.
//
// @see https://github.com/openai/openai-cookbook/blob/main/examples/How_to_count_tokens_with_tiktoken.ipynb
const (
	// TokensPerMessage covers the `<|start|>{role/name}\n{content}<|end|>\n`
	// envelope every message is wrapped in.
	TokensPerMessage = 4

	// TokensPerName is the cost of a participant name on a message.
	TokensPerName = 1

	// TokensPerReply covers the `<|start|>assistant<|message|>` every reply is
	// primed with.
	TokensPerReply = 3

	// TokensPerNameEstimate stands in for the rendered name itself, which is not
	// known at counting time. Over-estimating here is deliberate: the whole
	// point of the envelope cost is that under-counting gets a request rejected.
	TokensPerNameEstimate = 2
)

// MessageOverhead is the total per-message cost of the wire format.
const MessageOverhead = TokensPerMessage + TokensPerName + TokensPerReply + TokensPerNameEstimate

// CountMessage returns a message's token cost including wire-format overhead.
func CountMessage(model, text string) int {
	return Count(model, text) + MessageOverhead
}
