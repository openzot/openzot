package compaction

import (
	"strings"
	"testing"
)

// Where this package deliberately answers differently from the implementation
// it was ported from.
//
// The corpus is unforgiving by design: every captured call must produce the
// captured answer, and a mismatch fails. That is only worth something if the
// exceptions are enumerated rather than scattered - a `t.Skip` with a comment
// is one person's private decision, invisible in aggregate and impossible to
// audit. This is the list, and the corpus runner drives its skips from it, so
// there is one place a difference can be declared and no way to skip a record
// without declaring it.

// divergenceKind is why a record does not match.
type divergenceKind int

const (
	// diverged means zot answers differently on purpose.
	diverged divergenceKind = iota

	// partiallyDiverged means part of the answer is still pinned exactly and
	// only the rest may move. The runner compares what it can.
	partiallyDiverged
)

// divergence is one recorded difference.
type divergence struct {
	// Fn is the captured function it covers.
	Fn string

	// Kind is why it differs.
	Kind divergenceKind

	// Why has to explain the decision well enough that someone re-reading it in
	// a year can tell whether it still holds. This is the whole point of the
	// list: a reason nobody can act on is the same as no record at all.
	Why string
}

// divergences is the complete set for this package.
var divergences = []divergence{
	{
		Fn:   "estimateMessageTokens",
		Kind: diverged,
		Why: "The seed counted tokens as characters divided by four with a 1.25 safety margin. " +
			"zot counts with the model's own byte-pair vocabulary instead, so the numbers no longer " +
			"match and cannot be expected to - that is the point of the change. Every budgeting " +
			"decision rests on this number and the heuristic is wrong by enough to matter in both " +
			"directions: a 23-character CJK string is 11 tokens where the heuristic predicts 8, and " +
			"under-counting is the expensive direction because the provider rejects the request " +
			"outright. Per-message wire overhead is included too, which the seed ignored - the same " +
			"10 tokens the TypeScript engine charges. A tool call's payload is counted with the " +
			"message text, as estimateMessageUsage does for an activity: a request half carries no " +
			"text at all, so counting text alone priced a write of a whole file as an empty message. " +
			"What still has to hold is that counting is monotonic and never zero for real text, " +
			"which this package's own tests cover.",
	},
	{
		Fn:   "estimateMessagesTokens",
		Kind: diverged,
		Why: "The sum of estimateMessageTokens over a list, so it inherits that divergence " +
			"entirely - a sum of changed numbers is a changed number. Nothing about how the sum is " +
			"taken has changed: still one call per message, added in order, with no deduplication " +
			"or caching between them.",
	},
	{
		Fn:   "checkCompaction",
		Kind: partiallyDiverged,
		Why: "The split is still pinned exactly; only the estimatedTokens figure and, for a case " +
			"sitting right on the trigger threshold, the shouldCompact decision can move. Both " +
			"follow from the tokenizer change above rather than from any change to the compaction " +
			"logic. The threshold is a fraction of a token count, so a different tokenizer can " +
			"legitimately carry a borderline case across it.",
	},
}

// divergenceFor returns the recorded difference for a captured function.
func divergenceFor(fn string) (divergence, bool) {
	for _, entry := range divergences {
		if entry.Fn == fn {
			return entry, true
		}
	}

	return divergence{}, false
}

// A declaration that covers nothing is worse than none: it reads as a known,
// accepted difference while exempting no actual record, so the next real
// regression in that area looks accounted for.
func TestEveryDivergenceStillCoversSomething(t *testing.T) {
	corpus := loadCorpus(t)

	for _, entry := range divergences {
		covered := 0

		for _, record := range corpus.Records {
			if record.Fn == entry.Fn {
				covered++
			}
		}

		if covered == 0 {
			t.Errorf("%s is declared divergent but the corpus has no records for it - a stale "+
				"declaration waves through the next real regression in that area", entry.Fn)
		}
	}
}

// The reason is the only thing that makes a difference auditable later. A
// one-liner is a note to nobody.
func TestEveryDivergenceIsExplained(t *testing.T) {
	seen := map[string]bool{}

	for _, entry := range divergences {
		if entry.Fn == "" {
			t.Error("a divergence names no function")
		}

		if seen[entry.Fn] {
			t.Errorf("%s is declared twice", entry.Fn)
		}

		seen[entry.Fn] = true

		if len(entry.Why) < 120 {
			t.Errorf("%s: reason is too thin to act on: %q", entry.Fn, entry.Why)
		}

		// a reason that only restates the change explains nothing; it has to say
		// why the difference is correct
		if !strings.ContainsAny(entry.Why, ".") {
			t.Errorf("%s: reason is not a sentence", entry.Fn)
		}
	}
}
