package order

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/openzot/openzot/internal/session"
)

// writeOrderFile writes an order file and loads it.
func writeOrderFile(t *testing.T, path, body string) Order {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	o, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}

	return o
}

// Doneness is derived, never stored on the order: a settled record of this
// exact content satisfies it, and editing the order re-queues it because the
// hash no longer matches - re-queueing is a diff, like everything in the book.
func TestALedgerRecordSatisfiesTheExactOrderContent(t *testing.T) {
	book := t.TempDir()

	path := filepath.Join(book, BookDir, ordersName, "fix-the-bug.yaml")

	o := writeOrderFile(t, path, "objective: fix the bug\n")

	ledger := Ledger{Root: RecordsDir(book)}

	if _, done := ledger.Satisfied(o); done {
		t.Fatal("an order with no records must not be satisfied")
	}

	if err := ledger.Record(o, "20260822-010101", "settled", time.Now(), Evidence{}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	record, done := ledger.Satisfied(o)

	if !done || record.Run != "20260822-010101" {
		t.Fatalf("Satisfied = %+v, %v - a settled record of this content must satisfy", record, done)
	}

	// the receipt lands in the book, under the order's slug
	if _, err := os.Stat(filepath.Join(book, BookDir, recordsName, "fix-the-bug", "20260822-010101.yaml")); err != nil {
		t.Errorf("record file not where the book expects it: %v", err)
	}

	// editing the order changes its hash: no longer satisfied, runs again
	if err := os.WriteFile(path, []byte("objective: fix the bug properly\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, done := ledger.Satisfied(o); done {
		t.Error("an edited order must stop being satisfied - the record is for content that no longer exists")
	}
}

// The ledger no longer derives its location from the order's. An order is
// advisory input and may be read from anywhere - a shared folder of briefs, a
// checkout that is not the project, a temp file a dispatcher wrote - and the
// receipt still belongs in the ledger the operator configured, which may share
// no ancestor with it at all.
func TestTheLedgerIsWhereTheCallerSaysNotBesideTheOrder(t *testing.T) {
	briefs := t.TempDir()
	elsewhere := t.TempDir()

	path := filepath.Join(briefs, "somebody-elses-tree", "the-work.yaml")

	o := writeOrderFile(t, path, "objective: the work\n")

	ledger := Ledger{Root: filepath.Join(elsewhere, "central-ledger")}

	if err := ledger.Record(o, "20260822-030303", "settled", time.Now(), Evidence{}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	if _, err := os.Stat(filepath.Join(elsewhere, "central-ledger", "the-work", "20260822-030303.yaml")); err != nil {
		t.Errorf("the record did not land in the configured ledger: %v", err)
	}

	// nothing was written anywhere near the order
	if entries, err := os.ReadDir(briefs); err == nil {
		for _, entry := range entries {
			if entry.Name() != "somebody-elses-tree" {
				t.Errorf("the ledger wrote %q beside the order; its location is the caller's", entry.Name())
			}
		}
	}

	// and the order counts as done only against that same ledger
	if _, done := ledger.Satisfied(o); !done {
		t.Error("the configured ledger must satisfy the order it recorded")
	}

	if _, done := (Ledger{Root: filepath.Join(elsewhere, "another-ledger")}).Satisfied(o); done {
		t.Error("a different ledger must not see another ledger's records")
	}
}

// Two orders of the same name from different trees are different work. They
// share a slug, so they share a records folder - and the content hash is what
// keeps one from satisfying the other.
func TestSameNamedOrdersDoNotSatisfyEachOther(t *testing.T) {
	root := t.TempDir()

	ledger := Ledger{Root: filepath.Join(root, "ledger")}

	mine := writeOrderFile(t, filepath.Join(root, "mine", "deploy.yaml"), "objective: deploy the api\n")
	theirs := writeOrderFile(t, filepath.Join(root, "theirs", "deploy.yaml"), "objective: deploy the docs\n")

	if err := ledger.Record(mine, "20260822-040404", "settled", time.Now(), Evidence{}); err != nil {
		t.Fatal(err)
	}

	if _, done := ledger.Satisfied(theirs); done {
		t.Error("a different order's record must not satisfy this one, however it is named")
	}
}

// A failed run never satisfies: failure is not doneness.
func TestAFailedRecordDoesNotSatisfy(t *testing.T) {
	book := t.TempDir()

	o := writeOrderFile(t, filepath.Join(book, BookDir, ordersName, "x.yaml"), "objective: x\n")

	ledger := Ledger{Root: RecordsDir(book)}

	if err := ledger.Record(o, "20260822-020202", "error", time.Now(), Evidence{}); err != nil {
		t.Fatal(err)
	}

	if _, done := ledger.Satisfied(o); done {
		t.Error("a failed run must leave the order runnable")
	}
}

// A ledger with no root keeps no history: it records nothing and satisfies
// nothing, which is what an embedding caller tracking its own runs wants. It
// must say so rather than write to a relative path in whatever directory the
// process happens to be standing in.
func TestALedgerWithNoRootRecordsNothing(t *testing.T) {
	o := writeOrderFile(t, filepath.Join(t.TempDir(), "y.yaml"), "objective: y\n")

	var ledger Ledger

	if err := ledger.Record(o, "20260822-050505", "settled", time.Now(), Evidence{}); err == nil {
		t.Error("recording without a records directory must be refused, not written somewhere arbitrary")
	}

	if _, done := ledger.Satisfied(o); done {
		t.Error("a ledger with no root cannot satisfy anything")
	}
}

// An order that never was a file has no content to hash and no slug to file
// under, so it cannot enter the ledger.
func TestAnOrderWithNoPathCannotBeRecorded(t *testing.T) {
	ledger := Ledger{Root: t.TempDir()}

	if err := ledger.Record(Order{Objective: "synthesized"}, "20260822-060606", "settled", time.Now(), Evidence{}); err == nil {
		t.Error("an order with no path must be refused")
	}

	if _, done := ledger.Satisfied(Order{Objective: "synthesized"}); done {
		t.Error("an order with no path cannot be satisfied")
	}
}

// The book is one directory, so a project picks up one dotted folder rather
// than two generic top-level ones.
func TestTheBookLayoutKeepsOrdersAndRecordsTogether(t *testing.T) {
	project := "/srv/api"

	if got, want := OrdersDir(project), filepath.Join(project, ".zot", "orders"); got != want {
		t.Errorf("OrdersDir = %q, want %q", got, want)
	}

	if got, want := RecordsDir(project), filepath.Join(project, ".zot", "records"); got != want {
		t.Errorf("RecordsDir = %q, want %q", got, want)
	}
}

// A receipt has to carry proof, not just a claim. "settled" alone asks to be
// trusted; the run already wrote what it did, so the record shows it - and
// shows it on disk, because a reviewer reads the file, not the struct.
func TestAReceiptCarriesTheRunsOwnEvidence(t *testing.T) {
	book := t.TempDir()

	o := writeOrderFile(t, filepath.Join(book, BookDir, ordersName, "the-work.yaml"), "objective: the work\n")

	ledger := Ledger{Root: RecordsDir(book)}

	proof := Evidence{
		Session:    "20260822-101010",
		Reason:     "settled",
		Summary:    "added the endpoint and a test; go test ./... passes",
		Iterations: 12,
		Calls:      34,
	}

	if err := ledger.Record(o, "20260822-101010", "settled", time.Now(), proof); err != nil {
		t.Fatalf("Record: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(RecordsDir(book), "the-work", "20260822-101010.yaml"))
	if err != nil {
		t.Fatalf("read the receipt: %v", err)
	}

	var written Record

	if err := yaml.Unmarshal(data, &written); err != nil {
		t.Fatalf("the receipt is not readable YAML: %v\n%s", err, data)
	}

	if written.Evidence != proof {
		t.Errorf("evidence on disk = %+v, want %+v\n%s", written.Evidence, proof, data)
	}

	// the claim and its proof travel together
	if written.Run != "20260822-101010" || written.Reason != "settled" || written.OrderSHA256 == "" {
		t.Errorf("the receipt lost part of the claim: %+v", written)
	}

	// a reviewer reads the file: the run's own words have to be legible in it
	if !strings.Contains(string(data), "go test ./... passes") {
		t.Errorf("the run's summary is not in the receipt:\n%s", data)
	}
}

// A receipt with nothing to show must say so. Silence reads exactly like a run
// that did nothing, and a ledger that implies work it cannot evidence is worse
// than one that admits the gap - so nothing here is ever invented.
func TestAReceiptWithNoProofSaysSo(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "20260822-111111.jsonl")

	tests := []struct {
		name    string
		runID   string
		session *session.Session
		err     error
		want    string // what the receipt must own up to
	}{
		{
			name: "no session log at all",
			want: "not recorded to a session log",
		},
		{
			name:  "the log could not be read back",
			runID: "20260822-111111",
			err:   fmt.Errorf("open %s: permission denied", logPath),
			want:  "could not be read back",
		},
		{
			name:    "the run ended without recording an outcome",
			runID:   "20260822-111111",
			session: &session.Session{},
			want:    "recorded no outcome",
		},
		{
			name:    "the log was cut off mid-record",
			runID:   "20260822-111111",
			session: &session.Session{Truncated: true},
			want:    "ends mid-record",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			proof := EvidenceFrom(test.runID, test.session, test.err)

			if proof.Proven() {
				t.Errorf("evidence with nothing behind it claims to be proof: %+v", proof)
			}

			if !strings.Contains(proof.Missing, test.want) {
				t.Errorf("Missing = %q, want it to admit %q", proof.Missing, test.want)
			}

			// nothing invented to fill the gap
			if proof.Summary != "" || proof.Iterations != 0 || proof.Calls != 0 {
				t.Errorf("evidence was fabricated where there was none: %+v", proof)
			}
		})
	}
}

// The evidence is copied from the run's own record, never computed here - that
// is what makes it evidence rather than zot vouching for zot.
func TestEvidenceIsCopiedFromTheSessionResult(t *testing.T) {
	proof := EvidenceFrom("20260822-121212", &session.Session{
		Result: &session.Result{
			Reason:       "settled",
			Message:      "  did the thing  ",
			Iterations:   7,
			Calls:        19,
			InputTokens:  88000,
			OutputTokens: 5400,
		},
	}, nil)

	want := Evidence{
		Session:      "20260822-121212",
		Reason:       "settled",
		Summary:      "did the thing",
		Iterations:   7,
		Calls:        19,
		InputTokens:  88000,
		OutputTokens: 5400,
	}

	if proof != want {
		t.Errorf("EvidenceFrom = %+v, want %+v", proof, want)
	}

	if !proof.Proven() {
		t.Error("a run that recorded its outcome has proof to show")
	}
}

// A record is settled or it is not, and evidence does not change that: an
// unsettled run's receipt still carries its proof, and Satisfied still counts
// only settled records of the exact order content.
func TestEvidenceDoesNotMakeAnUnsettledRunCount(t *testing.T) {
	book := t.TempDir()

	o := writeOrderFile(t, filepath.Join(book, BookDir, ordersName, "the-work.yaml"), "objective: the work\n")

	ledger := Ledger{Root: RecordsDir(book)}

	proof := Evidence{
		Session:    "20260822-131313",
		Reason:     "max_iterations",
		Summary:    "ran out of rounds partway through",
		Iterations: 40,
		Calls:      120,
	}

	if err := ledger.Record(o, "20260822-131313", "max_iterations", time.Now(), proof); err != nil {
		t.Fatal(err)
	}

	if _, done := ledger.Satisfied(o); done {
		t.Error("a run that did not settle must not satisfy the order, however much it did")
	}

	// but the receipt still says what happened - the ledger is a history, not
	// only a list of successes
	data, err := os.ReadFile(filepath.Join(RecordsDir(book), "the-work", "20260822-131313.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(string(data), "ran out of rounds") {
		t.Errorf("an unsettled run's receipt lost its evidence:\n%s", data)
	}
}

// SortRecords orders newest-first, for listing. The ledger on disk is
// append-only and unordered; listing the receipts for an order needs them in
// the order the runs concluded, not the order the filesystem returns them.
func TestSortRecordsNewestFirst(t *testing.T) {
	now := time.Now()

	records := []Record{
		{Run: "old", At: now.Add(-2 * time.Hour)},
		{Run: "new", At: now},
		{Run: "mid", At: now.Add(-1 * time.Hour)},
	}

	sorted := SortRecords(records)

	if sorted[0].Run != "new" || sorted[1].Run != "mid" || sorted[2].Run != "old" {
		t.Errorf("SortRecords = %v, want new, mid, old", recordsOf(sorted))
	}
}

func recordsOf(records []Record) []string {
	names := make([]string, len(records))
	for i, r := range records {
		names[i] = r.Run
	}
	return names
}
