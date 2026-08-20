package order

import (
	"testing"
)

// The full journey of the reply that killed a real draft on 2026-08-20: parse
// the summary, scaffold it, and load the scaffold back as a runnable order.
func TestDraftedReplyScaffoldsAndReloads(t *testing.T) {
	reply := `acceptance:
- "go test ./internal/tokenizer/... -count=1" exits 0, verifying real tokenization counts match known values
- provider-reported usage is preferred over the local estimate, checked in internal/loop
constraints:
- do not change the tokenizer's public API
`

	drafted, err := ParseDraft("ensure that the token counting is done properly", reply)
	if err != nil {
		t.Fatalf("ParseDraft: %v", err)
	}

	path, err := Scaffold(t.TempDir(), drafted)
	if err != nil {
		t.Fatalf("Scaffold: %v", err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("the drafted scaffold does not load: %v", err)
	}

	if len(loaded.Acceptance) != 2 || loaded.Acceptance[0] != drafted.Acceptance[0] {
		t.Errorf("Acceptance = %q, want the drafted criteria surviving the round trip", loaded.Acceptance)
	}
}
