package thread

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Cases the corpus could not carry, and what replaced them.
//
// The corpus is the guarantee that this package answers as the implementation
// it was ported from did, so anything the capture had to leave out is a hole in
// that guarantee. A hole nobody wrote down is indistinguishable from a case
// that was simply forgotten - which is why these are enumerated here, each
// naming the hand-written test that took its place, and why the suite checks
// that test still exists.

// notPortable is a captured case that cannot be expressed as corpus data.
type notPortable struct {
	// ID is the record it would have been, kept so a future capture can tell
	// this case was considered rather than missed.
	ID string

	// Replacement names the hand-written test covering it.
	Replacement string

	// Why explains what made it uncapturable and what the replacement asserts.
	Why string
}

// notPortableCases is the complete set for this package.
//
// All three are the same hazard: a value containing a reference cycle. JSON has
// no way to express one, so they could not be seeded - but the question
// survives the port, because a malformed payload from a provider must not be
// the thing that aborts a run. A cycle check that panics is worse than no cycle
// check.
var notPortableCases = []notPortable{
	{
		ID:          "hasRepeatedSuffix/e577bc4f1b53",
		Replacement: "TestCycleCircularMeta",
		Why: "Asserts the heuristic tolerates a self-referential meta field. Go has no equivalent " +
			"hazard on this path - marshalling a cycle returns an error rather than recursing " +
			"forever - but the underlying question still applies: does a malformed meta abort the " +
			"run. The replacement builds a map containing itself and asserts the check answers " +
			"instead of panicking.",
	},
	{
		ID:          "hasRepeatedResultRun/a5c8fa462b6e",
		Replacement: "TestRepeatedResultRunCircularResult",
		Why: "A tool result containing a cycle, reaching the same hazard through the result-run " +
			"heuristic rather than the suffix one. Hand-written for the same reason: the input " +
			"cannot be written down as JSON.",
	},
	{
		ID:          "hasRepeatedResultRun/a5c8fa462b6e#1",
		Replacement: "TestRepeatedResultRunCircularResult",
		Why: "The second call the same case makes on the same cyclic result, checking the guard is " +
			"still willing to answer after meeting one. Covered by the same replacement, which " +
			"pushes the cyclic result twice and asserts both calls return rather than hang.",
	},
}

// A replacement that no longer exists means the case was quietly dropped while
// the record still reads as though it were handled.
func TestEveryNotPortableCaseHasItsReplacement(t *testing.T) {
	sources, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}

	var body strings.Builder

	for _, source := range sources {
		raw, err := os.ReadFile(source)
		if err != nil {
			t.Fatalf("read %s: %v", source, err)
		}

		body.Write(raw)
	}

	tests := body.String()

	for _, entry := range notPortableCases {
		if entry.Replacement == "" {
			t.Errorf("%s names no replacement test", entry.ID)

			continue
		}

		if !strings.Contains(tests, "func "+entry.Replacement+"(") {
			t.Errorf("%s points at %s, which does not exist", entry.ID, entry.Replacement)
		}
	}
}

// These records were left out of the capture. If one turns up in the corpus,
// either the export changed or the case was portable after all - and the
// hand-written stand-in is now a second, divergent copy of it.
func TestNotPortableCasesAreAbsentFromTheCorpus(t *testing.T) {
	corpus := loadCorpus(t)

	present := map[string]bool{}

	for _, record := range corpus.Records {
		present[record.ID] = true
	}

	for _, entry := range notPortableCases {
		if present[entry.ID] {
			t.Errorf("%s is in the corpus but recorded as not portable; the hand-written %s is now "+
				"a duplicate of it", entry.ID, entry.Replacement)
		}
	}
}

// The reason is what makes the omission auditable later.
func TestEveryNotPortableCaseIsExplained(t *testing.T) {
	seen := map[string]bool{}

	for _, entry := range notPortableCases {
		if entry.ID == "" {
			t.Error("a case names no record")
		}

		if seen[entry.ID] {
			t.Errorf("%s is recorded twice", entry.ID)
		}

		seen[entry.ID] = true

		if len(entry.Why) < 120 {
			t.Errorf("%s: reason is too thin to act on: %q", entry.ID, entry.Why)
		}
	}
}
