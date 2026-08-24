package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A session log has one job: survive the run that wrote it. These tests are
// mostly about the ways a run ends badly - killed mid-write, never finished,
// two runs racing for the same file - because a log that only works when
// everything went well is a log nobody needs.

func TestWriteThenReadBack(t *testing.T) {
	dir := t.TempDir()

	writer, err := Create(dir, "20260805-101500", Meta{
		Task:     "add a health endpoint",
		Model:    "glm-5.2",
		Provider: "zai",
		Workdir:  "/work",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := writer.Message(Message{Type: "user", Text: "add a health endpoint"}); err != nil {
		t.Fatalf("Message: %v", err)
	}

	if err := writer.Event(Event{Kind: "toolStart", Tool: "read", Iteration: 1}); err != nil {
		t.Fatalf("Event: %v", err)
	}

	if err := writer.Message(Message{Type: "assistant", Text: "done"}); err != nil {
		t.Fatalf("Message: %v", err)
	}

	if err := writer.Result(Result{Reason: "stop", Iterations: 2, Calls: 1, InputTokens: 4200, OutputTokens: 310}); err != nil {
		t.Fatalf("Result: %v", err)
	}

	session, err := Load(writer.Path())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if session.Meta.ID != "20260805-101500" || session.Meta.Task != "add a health endpoint" {
		t.Errorf("meta = %+v", session.Meta)
	}

	if session.Meta.Model != "glm-5.2" || session.Meta.Provider != "zai" {
		t.Errorf("meta lost the model or provider: %+v", session.Meta)
	}

	if len(session.Messages) != 2 {
		t.Fatalf("got %d messages, want 2", len(session.Messages))
	}

	if session.Messages[0].Type != "user" || session.Messages[1].Text != "done" {
		t.Errorf("messages = %+v", session.Messages)
	}

	if len(session.Events) != 1 || session.Events[0].Tool != "read" {
		t.Errorf("events = %+v", session.Events)
	}

	if !session.Complete() {
		t.Error("a session that recorded a result must read back as complete")
	}

	if session.Result.Iterations != 2 || session.Result.Calls != 1 {
		t.Errorf("result = %+v", session.Result)
	}

	if session.Result.InputTokens != 4200 || session.Result.OutputTokens != 310 {
		t.Errorf("result lost the token totals: %+v", session.Result)
	}

	if session.Truncated {
		t.Error("a cleanly written log must not read back as truncated")
	}
}

// The log has to be readable while the run is still going - that is the whole
// point of appending line by line rather than writing at the end.
func TestALogIsReadableMidRun(t *testing.T) {
	dir := t.TempDir()

	writer, err := Create(dir, "mid", Meta{Task: "t"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	defer writer.Close()

	_ = writer.Message(Message{Type: "user", Text: "hello"})

	session, err := Load(writer.Path())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if len(session.Messages) != 1 {
		t.Fatalf("an in-flight log should read back what is written so far, got %d", len(session.Messages))
	}

	// no result yet, and that is the signal that the run has not finished
	if session.Complete() {
		t.Error("an in-flight session must not report complete")
	}
}

// A killed process leaves a half-written final line. Everything before it is
// still the record of what happened, and losing it would defeat the purpose.
func TestATruncatedLogKeepsWhatItHas(t *testing.T) {
	dir := t.TempDir()

	writer, _ := Create(dir, "killed", Meta{Task: "t"})

	_ = writer.Message(Message{Type: "user", Text: "first"})
	_ = writer.Message(Message{Type: "assistant", Text: "second"})
	_ = writer.Close()

	raw, err := os.ReadFile(writer.Path())
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	// chop the file mid-record, exactly as SIGKILL during a write would
	chopped := append([]byte{}, raw[:len(raw)-20]...)

	if err := os.WriteFile(writer.Path(), chopped, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	session, err := Load(writer.Path())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if !session.Truncated {
		t.Error("a chopped log must report Truncated")
	}

	if len(session.Messages) != 1 || session.Messages[0].Text != "first" {
		t.Errorf("the intact records must survive, got %+v", session.Messages)
	}
}

func TestBlankLinesAreIgnored(t *testing.T) {
	session, err := Read(strings.NewReader("\n\n{\"kind\":\"meta\",\"meta\":{\"id\":\"x\",\"task\":\"t\"}}\n\n"))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	if session.Meta.ID != "x" {
		t.Errorf("meta = %+v", session.Meta)
	}

	if session.Truncated {
		t.Error("blank lines are not truncation")
	}
}

// A record whose payload is missing is skipped rather than appended as an empty
// message, so a malformed log cannot inject a blank turn into a resumed run.
func TestRecordsWithoutPayloadsAreSkipped(t *testing.T) {
	session, err := Read(strings.NewReader(strings.Join([]string{
		`{"kind":"meta"}`,
		`{"kind":"message"}`,
		`{"kind":"event"}`,
		`{"kind":"result"}`,
		`{"kind":"unknown","message":{"type":"user","text":"x"}}`,
	}, "\n")))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	if len(session.Messages) != 0 || len(session.Events) != 0 || session.Result != nil {
		t.Errorf("payload-less records must not become entries: %+v", session)
	}
}

// Two runs sharing one log would interleave into a conversation neither had.
func TestCreateRefusesAnExistingID(t *testing.T) {
	dir := t.TempDir()

	writer, err := Create(dir, "same", Meta{Task: "first"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	defer writer.Close()

	if _, err := Create(dir, "same", Meta{Task: "second"}); err == nil {
		t.Fatal("a second run must not be able to open the same log")
	}
}

func TestWritingAfterCloseIsAnError(t *testing.T) {
	dir := t.TempDir()

	writer, _ := Create(dir, "closed", Meta{Task: "t"})

	if err := writer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// close is idempotent, because Result closes and defer closes again
	if err := writer.Close(); err != nil {
		t.Errorf("a second Close must be harmless, got %v", err)
	}

	if err := writer.Message(Message{Type: "user"}); err == nil {
		t.Error("writing to a closed log must be reported")
	}
}

func TestNewIDSortsChronologically(t *testing.T) {
	earlier := NewID(time.Date(2026, 8, 5, 9, 0, 0, 0, time.UTC))
	later := NewID(time.Date(2026, 8, 5, 10, 0, 0, 0, time.UTC))

	if !(earlier < later) {
		t.Errorf("ids must sort by time: %q !< %q", earlier, later)
	}

	// the id names the file, so it has to be path-safe
	if strings.ContainsAny(earlier, `/\: `) {
		t.Errorf("id %q is not safe as a filename", earlier)
	}
}

// NewID is given a time rather than reading the clock so a caller in a
// different zone still produces an id that sorts against everyone else's.
func TestNewIDIsUTC(t *testing.T) {
	zone := time.FixedZone("UTC+9", 9*3600)

	if got := NewID(time.Date(2026, 8, 5, 9, 0, 0, 0, zone)); got != "20260805-000000" {
		t.Errorf("NewID = %q, want the UTC rendering", got)
	}
}

func TestListNewestFirst(t *testing.T) {
	dir := t.TempDir()

	for _, id := range []string{"20260805-090000", "20260805-110000", "20260805-100000"} {
		writer, err := Create(dir, id, Meta{Task: "task " + id})
		if err != nil {
			t.Fatalf("Create: %v", err)
		}

		_ = writer.Close()
	}

	// something that is not a session log at all
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	// and a directory, which readdir will hand back just the same
	if err := os.MkdirAll(filepath.Join(dir, "sub.jsonl"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	entries, err := List(dir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if len(entries) != 3 {
		t.Fatalf("got %d entries, want 3: %+v", len(entries), entries)
	}

	if entries[0].ID != "20260805-110000" || entries[2].ID != "20260805-090000" {
		t.Errorf("listing is not newest-first: %+v", entries)
	}

	if entries[0].Task != "task 20260805-110000" {
		t.Errorf("the listing must carry the task: %+v", entries[0])
	}

	if entries[0].Complete {
		t.Error("a run with no recorded result is not complete")
	}
}

func TestListCarriesTheStopReason(t *testing.T) {
	dir := t.TempDir()

	writer, _ := Create(dir, "done", Meta{Task: "t"})

	_ = writer.Result(Result{Reason: "stop"})

	entries, err := List(dir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if len(entries) != 1 || !entries[0].Complete || entries[0].Reason != "stop" {
		t.Errorf("entries = %+v", entries)
	}
}

// A first run has nowhere to look yet, and that is not an error.
func TestListOfAMissingDirectoryIsEmpty(t *testing.T) {
	entries, err := List(filepath.Join(t.TempDir(), "nope"))
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if len(entries) != 0 {
		t.Errorf("entries = %+v", entries)
	}
}

func TestResolve(t *testing.T) {
	dir := t.TempDir()

	for _, id := range []string{"20260805-090000", "20260805-100000"} {
		writer, _ := Create(dir, id, Meta{Task: "t"})

		_ = writer.Close()
	}

	newest := filepath.Join(dir, "20260805-100000.jsonl")

	tests := []struct {
		name      string
		reference string
		want      string
		wantErr   bool
	}{
		{name: "last picks the newest", reference: "last", want: newest},
		{name: "a bare id", reference: "20260805-090000", want: filepath.Join(dir, "20260805-090000.jsonl")},
		{name: "an id with the extension", reference: "20260805-090000.jsonl", want: filepath.Join(dir, "20260805-090000.jsonl")},
		{name: "an explicit path", reference: newest, want: newest},
		{name: "surrounding space", reference: "  last  ", want: newest},
		{name: "nothing", reference: "", wantErr: true},
		{name: "an unknown id", reference: "nope", wantErr: true},
		{name: "a path that does not exist", reference: filepath.Join(dir, "missing.jsonl"), wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := Resolve(dir, test.reference)

			if test.wantErr {
				if err == nil {
					t.Fatalf("Resolve(%q) = %q, want an error", test.reference, got)
				}

				return
			}

			if err != nil {
				t.Fatalf("Resolve(%q): %v", test.reference, err)
			}

			if got != test.want {
				t.Errorf("Resolve(%q) = %q, want %q", test.reference, got, test.want)
			}
		})
	}
}

func TestResolveLastWithNoSessions(t *testing.T) {
	if _, err := Resolve(filepath.Join(t.TempDir(), "empty"), "last"); err == nil {
		t.Fatal(`Resolve("last") with no sessions must be reported`)
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "nope.jsonl")); err == nil {
		t.Fatal("loading a missing log must be reported")
	}
}

// A tool result can be a whole file, and it lands on one line. The default
// scanner buffer would give up at 64KB, which is well inside normal.
func TestAVeryLongRecordSurvives(t *testing.T) {
	dir := t.TempDir()

	writer, _ := Create(dir, "big", Meta{Task: "t"})

	big := strings.Repeat("x", 512*1024)

	if err := writer.Message(Message{Type: "tool", Text: big}); err != nil {
		t.Fatalf("Message: %v", err)
	}

	_ = writer.Close()

	session, err := Load(writer.Path())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if len(session.Messages) != 1 || session.Messages[0].Text != big {
		t.Error("a large tool result must round-trip intact")
	}
}

func TestActivityRoundTrips(t *testing.T) {
	dir := t.TempDir()

	writer, _ := Create(dir, "meta", Meta{Task: "t"})

	_ = writer.Message(Message{Type: "activity", Text: "ok", Activity: &Activity{
		Kind: "response", ID: "call_1", Name: "read", Arguments: `{"path":"a.go"}`, Result: "ok",
	}})
	_ = writer.Close()

	session, err := Load(writer.Path())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	activity := session.Messages[0].Activity

	if activity == nil {
		t.Fatal("the log lost the tool call")
	}

	if activity.Name != "read" || activity.ID != "call_1" || activity.Arguments != `{"path":"a.go"}` {
		t.Errorf("activity = %+v", activity)
	}
}

// Resuming has to record where it came from, or a chain of continued runs is
// impossible to reconstruct afterwards.
func TestResumedFromIsRecorded(t *testing.T) {
	dir := t.TempDir()

	writer, err := Create(dir, "second", Meta{Task: "t", ResumedFrom: "first"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	_ = writer.Close()

	session, err := Load(writer.Path())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if session.Meta.ResumedFrom != "first" {
		t.Errorf("ResumedFrom = %q", session.Meta.ResumedFrom)
	}
}

// Create takes the id it is given, but it still has to be able to make the
// directory - an unwritable parent is reported rather than silently dropped.
func TestCreateReportsAnUnusableDirectory(t *testing.T) {
	file := filepath.Join(t.TempDir(), "a-file")

	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	if _, err := Create(filepath.Join(file, "sessions"), "x", Meta{}); err == nil {
		t.Fatal("creating a log under a file must be reported")
	}
}

// The engine emits events from its own goroutine while the caller records
// messages, so the writer has to hold up under -race.
func TestConcurrentWrites(t *testing.T) {
	dir := t.TempDir()

	writer, _ := Create(dir, "race", Meta{Task: "t"})

	done := make(chan struct{})

	go func() {
		defer close(done)

		for i := 0; i < 50; i++ {
			_ = writer.Event(Event{Kind: "toolStart", Tool: "read", Iteration: i})
		}
	}()

	for i := 0; i < 50; i++ {
		_ = writer.Message(Message{Type: "assistant", Text: "x"})
	}

	<-done

	_ = writer.Close()

	session, err := Load(writer.Path())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if len(session.Messages) != 50 || len(session.Events) != 50 {
		t.Errorf("got %d messages and %d events, want 50 each", len(session.Messages), len(session.Events))
	}

	if session.Truncated {
		t.Error("concurrent writes must not interleave into a corrupt line")
	}
}

// The engine compacts its own history. A log that kept both the pre- and
// post-compaction turns would resume into a conversation that never happened.
func TestResetDiscardsEarlierMessages(t *testing.T) {
	dir := t.TempDir()

	writer, _ := Create(dir, "compacted", Meta{Task: "t"})

	_ = writer.Message(Message{Type: "user", Text: "the original brief"})
	_ = writer.Message(Message{Type: "bot", Text: "a long turn since summarised"})
	_ = writer.Event(Event{Kind: "compaction"})

	if err := writer.Reset(); err != nil {
		t.Fatalf("Reset: %v", err)
	}

	_ = writer.Message(Message{Type: "context", Text: "summary of what came before"})
	_ = writer.Message(Message{Type: "user", Text: "the original brief"})
	_ = writer.Close()

	session, err := Load(writer.Path())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if len(session.Messages) != 2 || session.Messages[0].Type != "context" {
		t.Fatalf("messages after a reset = %+v", session.Messages)
	}

	// events are the narrative of the run rather than its state, so a reset
	// leaves them alone - otherwise the log would forget it ever compacted
	if len(session.Events) != 1 || session.Events[0].Kind != "compaction" {
		t.Errorf("a reset must not discard events: %+v", session.Events)
	}
}

// Two runs started in the same second collide on the time-derived id. One of
// them would otherwise lose its log entirely, which is exactly the run someone
// later wants to look at.
func TestStartAvoidsACollision(t *testing.T) {
	dir := t.TempDir()

	now := time.Date(2026, 8, 5, 10, 15, 0, 0, time.UTC)

	first, err := Start(dir, now, Meta{Task: "first"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	defer first.Close()

	second, err := Start(dir, now, Meta{Task: "second"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	defer second.Close()

	if first.ID() == second.ID() {
		t.Fatalf("both runs claimed %q", first.ID())
	}

	if first.ID() != NewID(now) {
		t.Errorf("the first run should get the plain id, got %q", first.ID())
	}

	entries, err := List(dir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if len(entries) != 2 {
		t.Fatalf("got %d logs, want both runs recorded", len(entries))
	}
}

// A directory that cannot hold a log at all is reported immediately rather than
// retried a hundred times.
func TestStartReportsAnUnusableDirectory(t *testing.T) {
	file := filepath.Join(t.TempDir(), "a-file")

	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	if _, err := Start(filepath.Join(file, "sessions"), time.Now(), Meta{}); err == nil {
		t.Fatal("an unusable directory must be reported")
	}
}
