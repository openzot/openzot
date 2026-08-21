// Package tokenizer estimates the token cost of model input.
//
// Zot talks to providers whose tokenizers are private, change independently,
// or differ from OpenAI's vocabulary. A model-specific vocabulary would make
// one provider look precise while adding several megabytes and remaining an
// approximation everywhere else. The estimator therefore prices UTF-8 bytes
// conservatively and lets provider-reported usage drive compaction after the
// first request.
package tokenizer

const (
	// BytesPerToken deliberately sits below the roughly four bytes per token of
	// ordinary English. Code, JSON and identifiers are denser, while UTF-8 makes
	// non-ASCII scripts consume multiple bytes per rune.
	BytesPerToken = 3

	// SafetyNumerator / SafetyDenominator adds 25% headroom to the byte estimate.
	SafetyNumerator   = 5
	SafetyDenominator = 4
)

// Count estimates a string's token cost. model is accepted because callers
// know which model they are budgeting for, but the estimate is intentionally
// model-independent: Zot cannot know the tokenizer behind every provider.
func Count(_ string, text string) int {
	return Estimate(text)
}

// Estimate prices text at one token per three UTF-8 bytes, then adds 25%.
// Each division rounds up because under-counting can make a provider reject a
// request, whereas over-counting merely compacts a little early.
func Estimate(text string) int {
	if text == "" {
		return 0
	}

	base := (len(text) + BytesPerToken - 1) / BytesPerToken

	return (base*SafetyNumerator + SafetyDenominator - 1) / SafetyDenominator
}

// The per-message costs of the chat wire format. Providers wrap every message
// in role and control data that does not appear in its text. Ten tokens is a
// deliberately conservative fixed allowance; provider-reported input usage
// corrects the compaction trigger once a request succeeds.
const (
	TokensPerMessage      = 4
	TokensPerName         = 1
	TokensPerReply        = 3
	TokensPerNameEstimate = 2
)

// MessageOverhead is the estimated fixed cost of one wire message.
const MessageOverhead = TokensPerMessage + TokensPerName + TokensPerReply + TokensPerNameEstimate

// CountMessage returns a message's estimated text and wire-envelope cost.
func CountMessage(model, text string) int {
	return Count(model, text) + MessageOverhead
}
