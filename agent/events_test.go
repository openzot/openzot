package agent

import (
	"testing"

	"github.com/openzot/openzot/internal/loop"
)

// A compaction rewrites the conversation and spends a model call, so it must
// reach the public event stream like retry and runaway do - not be swallowed at
// the boundary. This regressed once: the loop emitted the event but translate
// had no case for it.
func TestCompactionEventReachesTheStream(t *testing.T) {
	got, ok := translate(loop.Event{Kind: loop.EventCompact, Text: "compacted 30 earlier messages"})

	if !ok {
		t.Fatal("a compaction event must be forwarded, not dropped")
	}

	event, isCompaction := got.(CompactionEvent)
	if !isCompaction {
		t.Fatalf("got %T, want CompactionEvent", got)
	}

	if event.Detail != "compacted 30 earlier messages" {
		t.Errorf("the detail must survive translation: %q", event.Detail)
	}
}
