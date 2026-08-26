package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// writeLog writes a raw log body as a file named id.jsonl.
func writeLog(t *testing.T, dir, id, body string) {
	t.Helper()

	if err := os.WriteFile(filepath.Join(dir, id+".jsonl"), []byte(body), 0o600); err != nil {
		t.Fatalf("write log %s: %v", id, err)
	}
}

// entryFromLoad derives what a listing would say about a log by reading it
// whole - the definition of correct the fast listing is held to.
func entryFromLoad(path string) Entry {
	loaded, err := Load(path)
	if err != nil {
		return Entry{}
	}

	entry := Entry{
		Task:        loaded.Meta.Task,
		Complete:    loaded.Complete(),
		ResumedFrom: loaded.Meta.ResumedFrom,
	}

	if loaded.Result != nil {
		entry.Reason = loaded.Result.Reason
		entry.InputTokens = loaded.Result.InputTokens
		entry.OutputTokens = loaded.Result.OutputTokens

		// the outcome's own timestamp is the ending; the length is measured
		// from the log's opening record, which is where every run begins
		if !loaded.Started.IsZero() && !loaded.Ended.IsZero() {
			entry.Ended = loaded.Ended
			entry.Duration = loaded.Ended.Sub(loaded.Started)
		}
	}

	return entry
}

// The fast listing must say exactly what reading each log whole says, whatever
// shape the log has - concluded or not, resumed, compacted, crashed mid-line,
// or written by something other than this package's encoder. A divergence here
// is a mislisted history, not an approximation.
func TestListMatchesAFullRead(t *testing.T) {
	dir := t.TempDir()

	writer, err := Create(dir, "20260805-090000", Meta{
		Task:     "settled run",
		Provider: "zai",
		Driver:   "openai",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	_ = writer.Message(Message{Type: "user", Text: "begin"})
	_ = writer.Event(Event{Kind: "iteration", Iteration: 1})
	_ = writer.Reset()
	_ = writer.Message(Message{Type: "user", Text: "again"})
	_ = writer.Result(Result{Reason: "settled", Message: "done", InputTokens: 11})
	_ = writer.Close()

	writeLog(t, dir, "20260805-100000",
		`{"kind":"meta","at":"2026-08-05T10:00:00Z","meta":{"id":"20260805-100000","task":"failed run","resumedFrom":"20260805-090000"}}
		 {"kind":"message","at":"2026-08-05T10:00:01Z","message":{"type":"user","text":"go"}}
		 {"kind":"result","at":"2026-08-05T10:30:00Z","result":{"reason":"failed","message":"gave up","code":1,"inputTokens":12345,"outputTokens":678}}`)

	// no result at all: the run never concluded
	writeLog(t, dir, "20260805-110000",
		`{"kind":"meta","at":"2026-08-05T11:00:00Z","meta":{"id":"20260805-110000","task":"killed run"}}
		 {"kind":"message","at":"2026-08-05T11:00:01Z","message":{"type":"user","text":"go"}}
		 {"kind":"event","at":"2026-08-05T11:00:02Z","event":{"kind":"tool_end","tool":"shell"}}`)

	// a crash leaves a half-written final line behind
	writeLog(t, dir, "20260805-120000",
		`{"kind":"meta","at":"2026-08-05T12:00:00Z","meta":{"id":"20260805-120000","task":"truncated run"}}
		 {"kind":"result","at":"2026-08-05T12:30:00Z","result":{"reason":"settled","message":"done"}
		 {"kind":"mes`)

	// not written by this package's encoder: keys reordered, spaces around the
	// colons. The head-of-line kind check must not silently miss its records.
	writeLog(t, dir, "20260805-130000",
		`{"meta": {"id": "20260805-130000", "task": "reformatted run"}, "at": "2026-08-05T13:00:00Z", "kind": "meta"}
		 {"result": {"reason": "aborted", "inputTokens": 500}, "at": "2026-08-05T13:30:00Z", "kind": "result"}`)

	entries, err := List(dir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if len(entries) != 5 {
		t.Fatalf("got %d entries, want 5: %+v", len(entries), entries)
	}

	for _, got := range entries {
		want := entryFromLoad(got.Path)

		if got.Task != want.Task {
			t.Errorf("%s: task = %q, want %q", got.ID, got.Task, want.Task)
		}

		if got.Complete != want.Complete || got.Reason != want.Reason {
			t.Errorf("%s: outcome = (%v, %q), want (%v, %q)", got.ID, got.Complete, got.Reason, want.Complete, want.Reason)
		}

		if got.ResumedFrom != want.ResumedFrom {
			t.Errorf("%s: resumedFrom = %q, want %q", got.ID, got.ResumedFrom, want.ResumedFrom)
		}

		if !got.Ended.Equal(want.Ended) || got.Duration != want.Duration {
			t.Errorf("%s: ending = (%v, %v), want (%v, %v)", got.ID, got.Ended, got.Duration, want.Ended, want.Duration)
		}

		if got.InputTokens != want.InputTokens || got.OutputTokens != want.OutputTokens {
			t.Errorf("%s: tokens = (%d, %d), want (%d, %d)",
				got.ID, got.InputTokens, got.OutputTokens, want.InputTokens, want.OutputTokens)
		}
	}
}

// The listing reads the kind off the head of a line rather than decoding it,
// which is what keeps months of message payloads out of the decoder. Every
// shape this package writes must be recognised, and anything else must read as
// "unknown" so the caller decodes the line whole instead of guessing.
func TestKindOfReadsTheKindOffTheHead(t *testing.T) {
	tests := []struct {
		name string
		line string
		want Kind
	}{
		{"meta", `{"kind":"meta","at":"x"}`, KindMeta},
		{"message", `{"kind":"message","at":"x","message":{"type":"user","text":"hi"}}`, KindMessage},
		{"event", `{"kind":"event","at":"x"}`, KindEvent},
		{"result", `{"kind":"result","at":"x"}`, KindResult},
		{"reset", `{"kind":"reset","at":"x"}`, KindReset},
		{"spaces the decoder's way, not the writer's", `{"kind": "meta"}`, ""},
		{"unknown kind", `{"kind":"something-new"}`, ""},
		{"kind not first", `{"at":"x","kind":"meta"}`, ""},
		{"escaped quote in the name", `{"kind":"me\"ta"}`, ""},
		{"not an object", `[1,2,3]`, ""},
		{"garbage", `not json at all`, ""},
		{"empty", ``, ""},
	}

	for _, test := range tests {
		if got := kindOf([]byte(test.line)); got != test.want {
			t.Errorf("%s: kindOf(%q) = %q, want %q", test.name, test.line, got, test.want)
		}
	}
}

// The point of scanning is that a listing's cost follows the number of runs,
// not the size of them. If a listing ever goes back to reading each log whole,
// every payload is decoded into messages again and the cost of `zot sessions`
// - and of every dispatch that checks for an unfinished run - climbs with the
// history instead of with the number of files. This fails long before wall
// clock would: allocations are counted, not timed.
func TestListDoesNotDecodePayloadsWhileListing(t *testing.T) {
	dir := t.TempDir()

	const (
		logs      = 8
		payloads  = 150
		textBytes = 2048
	)

	payload := strings.Repeat("x", textBytes)

	for i := range logs {
		id := fmt.Sprintf("20260805-%06d", i)

		var b strings.Builder

		meta, _ := json.Marshal(listingRecord{Kind: KindMeta, Meta: &Meta{ID: id, Task: "run " + id}})
		b.Write(meta)
		b.WriteString("\n")

		for range payloads {
			fmt.Fprintf(&b, `{"kind":"event","at":"x","event":{"kind":"tool_end","text":"%s"}}`+"\n", payload)
		}

		result, _ := json.Marshal(listingRecord{Kind: KindResult, Result: &Result{Reason: "settled"}})
		b.Write(result)
		b.WriteString("\n")

		writeLog(t, dir, id, b.String())
	}

	var before, after runtime.MemStats

	runtime.GC()
	runtime.ReadMemStats(&before)

	entries, err := List(dir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	runtime.ReadMemStats(&after)

	if len(entries) != logs {
		t.Fatalf("got %d entries, want %d", len(entries), logs)
	}

	allocated := after.TotalAlloc - before.TotalAlloc

	// Decoding every payload would copy each 2 KiB text out of its record:
	// logs*payloads*textBytes of string bytes alone, several times that with
	// the scaffolding around them. Scanning reads only two small records per
	// log, so the ceiling sits far below even one log's worth of payloads.
	if allocated > logs*payloads*textBytes {
		t.Errorf("listing allocated %.1f MB, want well under the %.1f MB of payloads it must skip",
			float64(allocated)/(1<<20), float64(logs*payloads*textBytes)/(1<<20))
	}

	for _, entry := range entries {
		if !entry.Complete || entry.Reason != "settled" {
			t.Errorf("%s: outcome = (%v, %q), want the result record read", entry.ID, entry.Complete, entry.Reason)
		}
	}
}

// A listing shows when a run started; that stays the file's mod time, so a
// copied archive sorts by when it happened, not by what is inside.
func TestListStartedIsTheFileTime(t *testing.T) {
	dir := t.TempDir()

	writer, _ := Create(dir, "20260805-090000", Meta{Task: "t"})
	_ = writer.Close()

	stamp := time.Date(2026, 8, 5, 9, 1, 2, 0, time.UTC)
	if err := os.Chtimes(filepath.Join(dir, "20260805-090000.jsonl"), stamp, stamp); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	entries, err := List(dir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if len(entries) != 1 || !entries[0].Started.Equal(stamp) {
		t.Errorf("started = %v, want the file's mod time %v", entries[0].Started, stamp)
	}
}

// A listing must carry what a run's outcome records about itself - when it
// concluded, how long it went from its opening record to that outcome, and
// what it billed - because `zot sessions` is where an operator reads cost and
// length without opening every log. A run with no outcome has none of these:
// zeros there mean "the log says nothing", never a measured zero.
func TestListLiftsWhatTheResultRecords(t *testing.T) {
	dir := t.TempDir()

	writeLog(t, dir, "20260805-090000",
		`{"kind":"meta","at":"2026-08-05T09:00:00Z","meta":{"id":"20260805-090000","task":"long run"}}
		 {"kind":"result","at":"2026-08-05T09:04:40Z","result":{"reason":"settled","code":0,"inputTokens":12345,"outputTokens":678}}`)

	// no result: the run never concluded, so the listing has no ending, no
	// length and no cost to state
	writeLog(t, dir, "20260805-100000",
		`{"kind":"meta","at":"2026-08-05T10:00:00Z","meta":{"id":"20260805-100000","task":"killed run"}}`)

	entries, err := List(dir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}

	byID := map[string]Entry{}
	for _, entry := range entries {
		byID[entry.ID] = entry
	}

	settled := byID["20260805-090000"]

	wantEnded := time.Date(2026, 8, 5, 9, 4, 40, 0, time.UTC)
	if !settled.Ended.Equal(wantEnded) {
		t.Errorf("ended = %v, want %v", settled.Ended, wantEnded)
	}

	if settled.Duration != 280*time.Second {
		t.Errorf("duration = %v, want 4m40s", settled.Duration)
	}

	if settled.InputTokens != 12345 || settled.OutputTokens != 678 {
		t.Errorf("tokens = (%d, %d), want (12345, 678)", settled.InputTokens, settled.OutputTokens)
	}

	killed := byID["20260805-100000"]
	if killed.Complete || !killed.Ended.IsZero() || killed.Duration != 0 ||
		killed.InputTokens != 0 || killed.OutputTokens != 0 {
		t.Errorf("unfinished run listed as (%v, %v, %v, %d, %d), want all zero/false",
			killed.Complete, killed.Ended, killed.Duration, killed.InputTokens, killed.OutputTokens)
	}
}
